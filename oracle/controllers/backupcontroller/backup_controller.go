// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	snapv1 "github.com/kubernetes-csi/external-snapshotter/client/v4/apis/volumesnapshot/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

var (
	backupName           = "%s-%s-%s-%d"
	verifyExistsInterval = time.Minute * 5
	requeueInterval      = time.Second
	statusCheckInterval  = time.Minute
	msgSep               = "; "
	timeNow              = time.Now
	reconcileTimeout     = 3 * time.Minute
)

// BackupReconciler reconciles a Backup object.
type BackupReconciler struct {
	client.Client
	Log                 logr.Logger
	Scheme              *runtime.Scheme
	Recorder            record.EventRecorder
	InstanceLocks       *sync.Map
	OracleBackupFactory oracleBackupFactory
	BackupCtrl          backupControl

	DatabaseClientFactory controllers.DatabaseClientFactory
}

type backupControl interface {
	ValidateBackupSpec(backup *v1alpha1.Backup) bool
	GetBackup(name, namespace string) (*v1alpha1.Backup, error)
	GetInstance(name, namespace string) (*v1alpha1.Instance, error)
	LoadConfig(namespace string) (*v1alpha1.Config, error)
	UpdateStatus(obj client.Object) error
	UpdateBackup(obj client.Object) error
}

// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=backups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=backups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=instances,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="snapshot.storage.k8s.io",resources=volumesnapshotclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups="snapshot.storage.k8s.io",resources=volumesnapshots,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumes,verbs=get;list;watch;create;update;patch;delete

func backupSubType(st string) controllers.PhysicalBackupRequest_Type {
	switch st {
	case "Instance":
		return controllers.PhysicalBackupRequest_INSTANCE
	case "Database":
		return controllers.PhysicalBackupRequest_DATABASE
	case "Tablespace":
		return controllers.PhysicalBackupRequest_TABLESPACE
	case "Datafile":
		return controllers.PhysicalBackupRequest_DATAFILE
	}

	// If backup sub type is unknown default to Instance.
	// Defaulting to Instance seems more user friendly
	// (at the expense of silently swallowing a potential user error).
	return controllers.PhysicalBackupRequest_INSTANCE
}

// updateBackupStatus updates the phase of Backup and Instance objects to the required state.
func (r *BackupReconciler) updateBackupStatus(ctx context.Context, backup *v1alpha1.Backup, inst *v1alpha1.Instance) error {
	readyCond := k8s.FindCondition(backup.Status.Conditions, k8s.Ready)
	if k8s.ConditionReasonEquals(readyCond, k8s.BackupPending) {
		backup.Status.Phase = commonv1alpha1.BackupPending
	} else if k8s.ConditionReasonEquals(readyCond, k8s.BackupInProgress) {
		backup.Status.Phase = commonv1alpha1.BackupInProgress
	} else if k8s.ConditionReasonEquals(readyCond, k8s.BackupFailed) {
		backup.Status.Phase = commonv1alpha1.BackupFailed
	} else if k8s.ConditionReasonEquals(readyCond, k8s.BackupReady) {
		backup.Status.Phase = commonv1alpha1.BackupSucceeded
		if err := r.BackupCtrl.UpdateStatus(backup); err != nil {
			return err
		}
		inst.Status.BackupID = backup.Status.BackupID
		return r.BackupCtrl.UpdateStatus(inst)
	} else {
		// No handlers found for current set of conditions
		backup.Status.Phase = ""
	}

	return r.BackupCtrl.UpdateStatus(backup)
}

func (r *BackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, recErr error) {
	ctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	log := r.Log.WithValues("Backup", req.String())

	log.Info("reconciling backup requests")

	backup, err := r.BackupCtrl.GetBackup(req.Name, req.Namespace)
	if err != nil {
		log.Error(err, "get backup request error")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if backup.Spec.Mode == v1alpha1.VerifyExists {
		return r.reconcileVerifyExists(ctx, backup, log)
	}

	if !backup.DeletionTimestamp.IsZero() {
		return r.reconcileBackupDeletion(ctx, backup, log)
	}

	return r.reconcileBackupCreation(ctx, backup, log)
}

func (r *BackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mgr.GetFieldIndexer().IndexField(
		context.TODO(),
		&snapv1.VolumeSnapshot{}, ".spec.name",
		func(obj client.Object) []string {
			snapName := obj.(*snapv1.VolumeSnapshot).Name
			if snapName == "" {
				return nil
			}
			return []string{snapName}
		})

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Backup{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.PersistentVolume{}).
		Owns(&snapv1.VolumeSnapshotClass{}).
		Owns(&snapv1.VolumeSnapshot{}).
		Complete(r)
}

// reconcileVerifyExists verifies the existence of a backup and updates the result to backup status.
func (r *BackupReconciler) reconcileVerifyExists(ctx context.Context, backup *v1alpha1.Backup, log logr.Logger) (ctrl.Result, error) {
	var errMsgs []string
	if backup.Spec.Type != commonv1alpha1.BackupTypePhysical {
		errMsgs = append(errMsgs, fmt.Sprintf("%v backup does not support VerifyExists mode", backup.Spec.Type))
	}

	if controllers.GetBackupGcsPath(backup) == "" {
		errMsgs = append(errMsgs, fmt.Sprintf("Either .spec.gcsPath or .spec.gcsDir must be specified, VerifyExists mode only support GCS based physical backup"))
	}

	if len(errMsgs) > 0 {
		backup.Status.Phase = commonv1alpha1.BackupFailed
		msg := strings.Join(errMsgs, msgSep)
		r.Recorder.Event(backup, corev1.EventTypeWarning, k8s.NotSupported, msg)
		backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.NotSupported, msg)
		return ctrl.Result{}, r.BackupCtrl.UpdateStatus(backup)
	}

	// controller can run in different namespaces, hence different k8s service account.
	// it is better to verify physical backup in data plane.
	// In the future, we may consider deploying an independent pod to help verify a backup,
	// so that verification does not depend on the instance pod.
	inst, err := r.instReady(ctx, backup.Namespace, backup.Spec.Instance)
	// ensure data plane is ready
	if err != nil {
		log.Error(err, "instance not ready")
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	log.Info("Verifying the existence of a backup")

	req := &controllers.VerifyPhysicalBackupRequest{
		GcsPath: backup.Spec.GcsPath,
	}
	resp, err := controllers.VerifyPhysicalBackup(ctx, r, r.DatabaseClientFactory, inst.Namespace, inst.Name, *req)
	if err != nil {
		log.Error(err, "failed to verify a physical backup")
		// retry
		return ctrl.Result{Requeue: true}, nil
	}
	if len(resp.ErrMsgs) == 0 {
		backup.Status.Phase = commonv1alpha1.BackupSucceeded
		msg := "verified the existence of a physical backup"
		r.Recorder.Event(backup, corev1.EventTypeNormal, "BackupVerified", msg)
		backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionTrue, k8s.BackupReady, msg)
	} else {
		backup.Status.Phase = commonv1alpha1.BackupFailed
		msg := fmt.Sprintf("Failed to verify the existence of a physical backup: %s", strings.Join(resp.ErrMsgs, msgSep))
		r.Recorder.Event(backup, corev1.EventTypeWarning, "BackupVerifyFailed", msg)
		backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupFailed, msg)
	}
	return ctrl.Result{RequeueAfter: verifyExistsInterval}, r.BackupCtrl.UpdateStatus(backup)
}

// reconcileBackupCreation creates a backup and updates the result to backup status.
func (r *BackupReconciler) reconcileBackupCreation(ctx context.Context, backup *v1alpha1.Backup, log logr.Logger) (ctrl.Result, error) {
	if v := r.BackupCtrl.ValidateBackupSpec(backup); !v {
		return ctrl.Result{}, r.BackupCtrl.UpdateStatus(backup)
	}

	state := ""
	backupReadyCond := k8s.FindCondition(backup.Status.Conditions, k8s.Ready)
	if backupReadyCond != nil {
		state = backupReadyCond.Reason
	}
	switch state {
	case "":
		backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupPending, "Waiting for the instance to be ready.")
		backup.Status.Phase = commonv1alpha1.BackupPending
		log.Info("reconcileBackupCreation: ->BackupPending")
		return ctrl.Result{RequeueAfter: requeueInterval}, r.BackupCtrl.UpdateStatus(backup)

	case k8s.BackupPending:
		inst, err := r.instReady(ctx, backup.Namespace, backup.Spec.Instance)
		// ensure the inst is ready to create a backup
		if err != nil {
			msg := fmt.Sprintf("instance not ready: %v", err)
			r.Recorder.Event(backup, corev1.EventTypeWarning, k8s.BackupFailed, msg)
			backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupFailed, msg)
			backup.Status.Phase = commonv1alpha1.BackupFailed
			log.Info("reconcileBackupCreation: BackupPending->BackupFailed")
			return ctrl.Result{}, r.BackupCtrl.UpdateStatus(backup)
		}
		// backup type is validated in validateBackupSpec
		b := r.OracleBackupFactory.newOracleBackup(r, backup, inst, log)
		if backup.Status.BackupID == "" || backup.Status.BackupTime == "" || backup.Status.StartTime == nil {
			backup.Status.BackupID = b.generateID()
			backup.Status.BackupTime = timeNow().Format("20060102150405")
			startTime := metav1.NewTime(timeNow())
			backup.Status.StartTime = &startTime
			log.Info("backup started at:", "StartTime", backup.Status.StartTime)
			// commit backup id and time
			return ctrl.Result{RequeueAfter: requeueInterval}, r.updateBackupStatus(ctx, backup, inst)
		}

		if err := r.addBackupMetadata(ctx, backup, &oracleBackupMetadata{
			incarnation:       inst.Status.CurrentDatabaseIncarnation,
			parentIncarnation: inst.Status.LastDatabaseIncarnation,
			databaseImage:     inst.Status.ActiveImages["service"],
		}); err != nil {
			return ctrl.Result{}, err
		}

		if err := b.create(ctx); err != nil {
			// default retry
			return ctrl.Result{}, err
		}
		backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupInProgress, "Starting to create a backup.")
		log.Info("reconcileBackupCreation: BackupPending->BackupInProgress")
		return ctrl.Result{RequeueAfter: requeueInterval}, r.updateBackupStatus(ctx, backup, inst)

	case k8s.BackupInProgress:
		inst, err := r.BackupCtrl.GetInstance(backup.Spec.Instance, backup.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		// backup type is validated in validateBackupSpec
		b := r.OracleBackupFactory.newOracleBackup(r, backup, inst, log)
		done, err := b.status(ctx)
		if err != nil && strings.Contains(err.Error(), "code = NotFound") {
			// The backup was interrupted and the LRO is lost.
			backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupFailed, "Backup interrupted")
			return ctrl.Result{}, r.updateBackupStatus(ctx, backup, inst)
		}

		if done {
			if err == nil {
				r.Recorder.Eventf(backup, corev1.EventTypeNormal, "BackupCompleted", "BackupId:%v, Elapsed time: %v", backup.Status.BackupID, k8s.ElapsedTimeFromLastTransitionTime(k8s.FindCondition(backup.Status.Conditions, k8s.Ready), time.Second))
				backupMetadata, err := b.metadata(ctx)
				if err != nil {
					return ctrl.Result{}, err
				}
				if err := r.addBackupMetadata(ctx, backup, backupMetadata); err != nil {
					return ctrl.Result{}, err
				}
				backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionTrue, k8s.BackupReady, "")
				duration := metav1.Duration{Duration: metav1.Now().Sub(backup.Status.StartTime.Time)}
				backup.Status.Duration = &duration
				backup.Status.GcsPath = controllers.GetBackupGcsPath(backup)
				log.Info("reconcileBackupCreation: BackupInProgress->BackupReady")
			} else {
				r.Recorder.Event(backup, corev1.EventTypeWarning, "BackupFailed", err.Error())
				backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupFailed, err.Error())
				log.Info("reconcileBackupCreation: BackupInProgress->BackupFailed")
			}
			log.Info("reconciling backup creation: DONE")

			return ctrl.Result{}, r.updateBackupStatus(ctx, backup, inst)
		}
		log.Info("reconciling backup creation: InProgress")
		return ctrl.Result{RequeueAfter: statusCheckInterval}, nil
	case k8s.BackupReady:
		// Add finalizer to clean backup data in case of deletion.
		if !controllerutil.ContainsFinalizer(backup, controllers.FinalizerName) {
			log.Info("Adding backup finalizer.")
			controllerutil.AddFinalizer(backup, controllers.FinalizerName)
			// Immediately return to update the object and do the rest of work in the next reconcile cycle.
			return ctrl.Result{}, r.Update(ctx, backup)
		}
		return ctrl.Result{}, nil
	default:
		log.Info("no action needed", "backupReady", backupReadyCond)
		return ctrl.Result{}, nil
	}
}

// reconcileBackupDeletion cleanup backup data when backup object is deleted.
func (r *BackupReconciler) reconcileBackupDeletion(ctx context.Context, backup *v1alpha1.Backup, log logr.Logger) (ctrl.Result, error) {
	log.Info("Reconciling backup delete...")

	if !controllerutil.ContainsFinalizer(backup, controllers.FinalizerName) {
		return ctrl.Result{}, nil
	}

	if backup.Status.Phase != k8s.BackupDeleting {
		backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupDeleting, "Backup delete in progress.")
		backup.Status.Phase = k8s.BackupDeleting
		// return to make the update taking effect immediately.
		return ctrl.Result{}, r.Status().Update(ctx, backup)
	}

	// Remove the backup finalizer if an associated Instance doesn't exist.
	_, err := r.BackupCtrl.GetInstance(backup.Spec.Instance, backup.Namespace)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		log.Info("Parent Instance not found. Removing backup finalizer.")
		controllerutil.RemoveFinalizer(backup, controllers.FinalizerName)
		return ctrl.Result{}, r.Update(ctx, backup)
	}

	var b oracleBackup
	if backup.Spec.Type == commonv1alpha1.BackupTypeSnapshot {
		b = &snapshotBackup{
			r:      r,
			log:    log,
			backup: backup,
		}
	} else {
		b = &physicalBackup{
			r:      r,
			log:    log,
			backup: backup,
		}
	}

	if err := b.delete(ctx); err != nil {
		log.Error(err, "error delete backup", "error", err)
		return ctrl.Result{}, fmt.Errorf("error deleting backup - %v", err)
	}

	// Remove the finalizer.
	log.Info("Removing backup finalizer.")
	controllerutil.RemoveFinalizer(backup, controllers.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, backup)
}

// instReady returns non-nil error if instance is not in ready state.
func (r *BackupReconciler) instReady(ctx context.Context, ns, instName string) (*v1alpha1.Instance, error) {
	inst, err := r.BackupCtrl.GetInstance(instName, ns)
	if err != nil {
		r.Log.Error(err, "error finding instance for backup validation")
		return nil, fmt.Errorf("error finding instance - %v", err)
	}
	if !k8s.ConditionStatusEquals(k8s.FindCondition(inst.Status.Conditions, k8s.Ready), v1.ConditionTrue) {
		r.Log.Error(fmt.Errorf("instance not in ready state"), "Instance not in ready state for backup", "inst.Status.Conditions", inst.Status.Conditions)
		return nil, errors.New("instance is not in a ready state")
	}
	return inst, nil
}

func lroOperationID(backup *v1alpha1.Backup) string {
	return fmt.Sprintf("Backup_%s", backup.GetUID())
}

// addBackupMetadata adds non-zero metadata to backup's annotation/label.
func (r *BackupReconciler) addBackupMetadata(ctx context.Context, backup *v1alpha1.Backup, backupMetadata *oracleBackupMetadata) error {
	if backupMetadata == nil {
		return nil
	}
	if backup.Labels == nil {
		backup.Labels = map[string]string{}
	}
	if backup.Annotations == nil {
		backup.Annotations = map[string]string{}
	}
	if !backupMetadata.timestamp.IsZero() {
		backup.Annotations[controllers.TimestampAnnotation] = backupMetadata.timestamp.Format(time.RFC3339)
	}
	if backupMetadata.incarnation != "" {
		backup.Labels[controllers.IncarnationLabel] = backupMetadata.incarnation
	}
	if backupMetadata.parentIncarnation != "" {
		backup.Labels[controllers.ParentIncarnationLabel] = backupMetadata.parentIncarnation
	}
	if backupMetadata.scn != "" {
		backup.Annotations[controllers.SCNAnnotation] = backupMetadata.scn
	}
	if backupMetadata.databaseImage != "" {
		backup.Annotations[controllers.DatabaseImageAnnotation] = backupMetadata.databaseImage
	}
	return r.BackupCtrl.UpdateBackup(backup)
}
