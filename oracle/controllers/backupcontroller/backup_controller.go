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
	"time"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/pkg/utils"
	"github.com/go-logr/logr"
	snapv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	capb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

var (
	backupName           = "%s-%s-%s-%d"
	verifyExistsInterval = time.Minute * 5
	requeueInterval      = time.Second
	statusCheckInterval  = time.Minute
	msgSep               = "; "
)

// BackupReconciler reconciles a Backup object.
type BackupReconciler struct {
	client.Client
	Log           logr.Logger
	Scheme        *runtime.Scheme
	ClientFactory controllers.ConfigAgentClientFactory
	Recorder      record.EventRecorder
}

type oracleBackup interface {
	create(ctx context.Context) error
	status(ctx context.Context) (done bool, err error)
	generateID() string
}

type snapshotBackup struct {
	r      *BackupReconciler
	log    logr.Logger
	backup *v1alpha1.Backup
	inst   *v1alpha1.Instance
}

func (b *snapshotBackup) create(ctx context.Context) error {
	// Load default preferences (aka "config") if provided by a customer.
	config, err := b.r.loadConfig(ctx, b.backup.Namespace)
	if err != nil {
		return err
	}

	var configSpec *commonv1alpha1.ConfigSpec
	if config != nil {
		configSpec = &config.Spec.ConfigSpec
		b.log.Info("customer config loaded", "config", config)
	} else {
		b.log.Info("no customer specific config found, assuming all defaults")
	}

	vsc, err := utils.FindVolumeSnapshotClassName(b.backup.Spec.VolumeSnapshotClass, configSpec, utils.PlatformGCP)
	if err != nil || vsc == "" {
		return fmt.Errorf("failed to identify a volumeSnapshotClassName for instance: %q", b.backup.Spec.Instance)
	}
	b.log.Info("VolumeSnapshotClass", "volumeSnapshotClass", vsc)

	getPvcNames := func(spec commonv1alpha1.DiskSpec) (string, string, string) {
		shortPVCName, mount := controllers.GetPVCNameAndMount(b.inst.Name, spec.Name)
		fullPVCName := fmt.Sprintf("%s-%s-0", shortPVCName, fmt.Sprintf(controllers.StsName, b.inst.Name))
		snapshotName := fmt.Sprintf("%s-%s", b.backup.Status.BackupID, mount)
		return fullPVCName, snapshotName, vsc
	}
	applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("backup-controller")}

	return utils.SnapshotDisks(ctx, controllers.DiskSpecs(b.inst, config), b.backup, b.r.Client, b.r.Scheme, getPvcNames, applyOpts)
}

func (b *snapshotBackup) status(ctx context.Context) (done bool, err error) {
	b.log.Info("found a backup request in-progress")
	ns := b.backup.Namespace
	sel := labels.NewSelector()
	vsLabels := []string{b.backup.Status.BackupID + "-u02", b.backup.Status.BackupID + "-u03"}
	req1, err := labels.NewRequirement("name", selection.In, vsLabels)
	if err != nil {
		return false, err
	}
	sel.Add(*req1)

	req2, err := labels.NewRequirement("namespace", selection.Equals, []string{ns})
	if err != nil {
		return false, err
	}
	sel.Add(*req2)

	listOpts := []client.ListOption{
		client.InNamespace(ns),
		client.MatchingLabelsSelector{Selector: sel},
	}

	var volSnaps snapv1.VolumeSnapshotList
	if err := b.r.List(ctx, &volSnaps, listOpts...); err != nil {
		b.log.Error(err, "failed to get a volume snapshot")
		return false, err
	}
	b.log.Info("list of found volume snapshots", "volSnaps", volSnaps)

	if len(volSnaps.Items) < 1 {
		b.log.Info("no volume snapshots found for a backup request marked as in-progress.", "backup.Status", b.backup.Status)
		return false, errors.New("no volume snapshots found")
	}
	b.log.Info("found a volume snapshot(s) for a backup request in-progress")

	vsStatus := make(map[string]bool)
	for i, vs := range volSnaps.Items {
		b.log.Info("iterating over volume snapshots", "VolumeSnapshot#", i, "name", vs.Name)
		vsStatus[vs.Name] = false

		if vs.Status == nil {
			b.log.Info("not yet ready: Status missing for Volume Snapshot", "namespace", vs.Namespace, "volumeSnapshotName", vs.Name, "volumeSnapshotStatus", vs.Status)
			return false, nil
		}

		if vs.Status.Error != nil {
			b.log.Error(errors.New("the volumeSnapshot is failed"), "namespace", vs.Namespace, "volumeSnapshotName", vs.Name, "volumeSnapshotStatus", vs.Status, "VolumeSnapshotError", vs.Status.Error)
			return true, fmt.Errorf("volumeSnapshot %s/%s failed with: %s", vs.Namespace, vs.Name, *vs.Status.Error.Message)
		}

		if !*vs.Status.ReadyToUse {
			b.log.Info("not yet ready: Status found, but it's not flipped to DONE yet for VolumeSnapshot", "namespace", vs.Namespace, "volumeSnapshotName", vs.Name, "volumeSnapshotStatus", vs.Status)
			return false, nil
		}

		b.log.Info("ready to use status", "VolumeSnapshot#", i, "name", vs, "status", *vs.Status.ReadyToUse)
		vsStatus[vs.Name] = true
	}
	b.log.Info("summary of VolumeSnapshot statuses", "vsStatus", vsStatus)

	return true, nil
}

func (b *snapshotBackup) generateID() string {
	return fmt.Sprintf(backupName, b.backup.Spec.Instance, time.Now().Format("20060102"), "snap", time.Now().Nanosecond())
}

type physicalBackup struct {
	r      *BackupReconciler
	log    logr.Logger
	backup *v1alpha1.Backup
}

func (b *physicalBackup) create(ctx context.Context) error {
	timeLimitMinutes := controllers.PhysBackupTimeLimitDefault
	if b.backup.Spec.TimeLimitMinutes != 0 {
		timeLimitMinutes = time.Duration(b.backup.Spec.TimeLimitMinutes) * time.Minute
	}

	dop := int32(1)
	if b.backup.Spec.Dop != 0 {
		dop = b.backup.Spec.Dop
	}

	// the default is backupset true, not image copy
	backupset := pointer.Bool(true)
	if b.backup.Spec.Backupset != nil {
		backupset = b.backup.Spec.Backupset
	}

	ctxBackup, cancel := context.WithTimeout(ctx, timeLimitMinutes)
	defer cancel()

	caClient, closeConn, err := b.r.ClientFactory.New(ctxBackup, b.r, b.backup.Namespace, b.backup.Spec.Instance)
	if err != nil {
		b.log.Error(err, "failed to create config agent client")
		return err
	}
	defer closeConn()

	if _, err := caClient.PhysicalBackup(ctxBackup, &capb.PhysicalBackupRequest{
		BackupSubType: backupSubType(b.backup.Spec.Subtype),
		BackupItems:   b.backup.Spec.BackupItems,
		Backupset:     *backupset,
		CheckLogical:  b.backup.Spec.CheckLogical,
		Compressed:    b.backup.Spec.Compressed,
		Dop:           dop,
		Level:         b.backup.Spec.Level,
		Filesperset:   b.backup.Spec.Filesperset,
		SectionSize:   b.backup.SectionSize(),
		LocalPath:     b.backup.Spec.LocalPath,
		GcsPath:       b.backup.Spec.GcsPath,
		LroInput:      &capb.LROInput{OperationId: lroOperationID(b.backup)},
	}); err != nil && !controllers.IsAlreadyExistsError(err) {
		return fmt.Errorf("failed on PhysicalBackup gRPC call: %v", err)
	}
	return nil
}

func (b *physicalBackup) status(ctx context.Context) (done bool, err error) {
	id := lroOperationID(b.backup)
	operation, err := controllers.GetLROOperation(b.r.ClientFactory, ctx, b.r, b.backup.Namespace, id, b.backup.Spec.Instance)
	if err != nil {
		b.log.Error(err, "GetLROOperation error")
		return false, err
	}

	if operation.Done {
		b.log.Info("LRO is DONE", "id", id)
		if operation.GetError() != nil {
			err = errors.New(operation.GetError().GetMessage())
		}
		if err := controllers.DeleteLROOperation(b.r.ClientFactory, ctx, b.r, b.backup.Namespace, id, b.backup.Spec.Instance); err != nil {
			b.log.Error(err, "failed to delete a LRO ")
		}
		return true, err
	}
	b.log.Info("LRO is in progress", "id", id)
	return false, nil
}

func (b *physicalBackup) generateID() string {
	return fmt.Sprintf(backupName, b.backup.Spec.Instance, time.Now().Format("20060102"), "phys", time.Now().Nanosecond())
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

func backupSubType(st string) capb.PhysicalBackupRequest_Type {
	switch st {
	case "Instance":
		return capb.PhysicalBackupRequest_INSTANCE
	case "Database":
		return capb.PhysicalBackupRequest_DATABASE
	case "Tablespace":
		return capb.PhysicalBackupRequest_TABLESPACE
	case "Datafile":
		return capb.PhysicalBackupRequest_DATAFILE
	}

	// If backup sub type is unknown default to Instance.
	// Defaulting to Instance seems more user friendly
	// (at the expense of silently swallowing a potential user error).
	return capb.PhysicalBackupRequest_INSTANCE
}

// loadConfig attempts to find a customer specific Operator config
// if it's been provided. There should be at most one config.
// If no config is provided by a customer, no errors are raised and
// all defaults are assumed.
func (r *BackupReconciler) loadConfig(ctx context.Context, ns string) (*v1alpha1.Config, error) {
	var configs v1alpha1.ConfigList
	if err := r.List(ctx, &configs, client.InNamespace(ns)); err != nil {
		return nil, err
	}

	if len(configs.Items) == 0 {
		return nil, nil
	}

	if len(configs.Items) > 1 {
		return nil, fmt.Errorf("this release only supports a single customer provided config (received %d)", len(configs.Items))
	}

	return &configs.Items[0], nil
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
		if err := r.Status().Update(ctx, backup); err != nil {
			return err
		}
		inst.Status.BackupID = backup.Status.BackupID
		return r.Status().Update(ctx, inst)
	} else {
		// No handlers found for current set of conditions
		backup.Status.Phase = ""
	}

	return r.Status().Update(ctx, backup)
}

func (r *BackupReconciler) Reconcile(_ context.Context, req ctrl.Request) (result ctrl.Result, recErr error) {
	ctx := context.Background()
	log := r.Log.WithValues("Backup", req.NamespacedName)

	log.Info("reconciling backup requests")

	var backup v1alpha1.Backup
	if err := r.Get(ctx, req.NamespacedName, &backup); err != nil {
		log.Error(err, "get backup request error")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if backup.Spec.Mode == v1alpha1.VerifyExists {
		return r.reconcileVerifyExists(ctx, &backup, log)
	}

	return r.reconcileBackupCreation(ctx, &backup, log)
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

	if backup.Spec.GcsPath == "" {
		errMsgs = append(errMsgs, fmt.Sprintf(".spec.gcsPath must be specified, VerifyExists mode only support GCS based physical backup"))
	}

	if len(errMsgs) > 0 {
		backup.Status.Phase = commonv1alpha1.BackupFailed
		msg := strings.Join(errMsgs, msgSep)
		r.Recorder.Event(backup, corev1.EventTypeWarning, k8s.NotSupported, msg)
		backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.NotSupported, msg)
		return ctrl.Result{}, r.Status().Update(ctx, backup)
	}

	// controller can run in different namespaces, hence different k8s service account.
	// it is better to verify physical backup in data plane.
	// In the future, we may consider deploying an independent pod to help verify a backup,
	// so that verification does not depend on the instance pod.
	inst := &v1alpha1.Instance{}
	// ensure data plane is ready
	if err := r.instReady(ctx, backup.Namespace, backup.Spec.Instance, inst, log); err != nil {
		log.Error(err, "instance not ready")
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	log.Info("Verifying the existence of a backup")

	caClient, closeConn, err := r.ClientFactory.New(ctx, r, backup.Namespace, inst.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create config agent client: %w", err)
	}
	defer closeConn()
	resp, err := caClient.VerifyPhysicalBackup(ctx, &capb.VerifyPhysicalBackupRequest{
		GcsPath: backup.Spec.GcsPath,
	})
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
		msg := fmt.Sprintf("Failed to verify the existence of a physical backup: %s", strings.Join(resp.GetErrMsgs(), msgSep))
		r.Recorder.Event(backup, corev1.EventTypeWarning, "BackupVerifyFailed", msg)
		backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupFailed, msg)
	}
	return ctrl.Result{RequeueAfter: verifyExistsInterval}, r.Status().Update(ctx, backup)
}

func validateBackupCreationSpec(backup *v1alpha1.Backup) bool {
	var errMsgs []string
	if backup.Spec.Type != commonv1alpha1.BackupTypeSnapshot && backup.Spec.Type != commonv1alpha1.BackupTypePhysical {
		errMsgs = append(errMsgs, fmt.Sprintf("backup does not support type %q", backup.Spec.Type))
	}
	if backup.Spec.Type == commonv1alpha1.BackupTypeSnapshot && backup.Spec.Subtype != "" && backup.Spec.Subtype != "Instance" {
		errMsgs = append(errMsgs, fmt.Sprintf("%s backup only support .spec.subtype 'Instance'", backup.Spec.Type))
	}
	if backup.Spec.Instance == "" {
		errMsgs = append(errMsgs, fmt.Sprintf("spec.Instance is not set in the backup request: %v", backup))
	}
	if len(errMsgs) > 0 {
		reason := ""
		brc := k8s.FindCondition(backup.Status.Conditions, k8s.Ready)
		if brc != nil {
			// do not change condition reason
			reason = brc.Reason
		}
		backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionUnknown, reason, strings.Join(errMsgs, msgSep))
		return false
	}
	return true
}

// reconcileBackupCreation creates a backup and updates the result to backup status.
func (r *BackupReconciler) reconcileBackupCreation(ctx context.Context, backup *v1alpha1.Backup, log logr.Logger) (ctrl.Result, error) {
	if v := validateBackupCreationSpec(backup); !v {
		return ctrl.Result{}, r.Status().Update(ctx, backup)
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
		return ctrl.Result{RequeueAfter: requeueInterval}, r.Status().Update(ctx, backup)

	case k8s.BackupPending:
		inst := &v1alpha1.Instance{}
		// ensure the inst is ready to create a backup
		if err := r.instReady(ctx, backup.Namespace, backup.Spec.Instance, inst, log); err != nil {
			msg := fmt.Sprintf("instance not ready: %v", err)
			r.Recorder.Event(backup, corev1.EventTypeWarning, k8s.BackupFailed, msg)
			backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupFailed, msg)
			backup.Status.Phase = commonv1alpha1.BackupFailed
			log.Info("reconcileBackupCreation: BackupPending->BackupFailed")
			return ctrl.Result{}, r.Status().Update(ctx, backup)
		}
		// backup type is validated in validateBackupSpec
		var b oracleBackup
		if backup.Spec.Type == commonv1alpha1.BackupTypeSnapshot {
			b = &snapshotBackup{
				r:      r,
				log:    log,
				backup: backup,
				inst:   inst,
			}
		} else {
			b = &physicalBackup{
				r:      r,
				log:    log,
				backup: backup,
			}
		}
		if backup.Status.BackupID == "" || backup.Status.BackupTime == "" || backup.Status.StartTime == nil {
			backup.Status.BackupID = b.generateID()
			backup.Status.BackupTime = time.Now().Format("20060102150405")
			startTime := metav1.Now()
			backup.Status.StartTime = &startTime
			log.Info("backup started at:", "StartTime", backup.Status.StartTime)
			// commit backup id and time
			return ctrl.Result{RequeueAfter: requeueInterval}, r.updateBackupStatus(ctx, backup, inst)
		}

		if err := b.create(ctx); err != nil {
			// default retry
			return ctrl.Result{}, err
		}
		backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupInProgress, "Starting to create a backup.")
		log.Info("reconcileBackupCreation: BackupPending->BackupInProgress")
		return ctrl.Result{RequeueAfter: requeueInterval}, r.updateBackupStatus(ctx, backup, inst)

	case k8s.BackupInProgress:
		inst := &v1alpha1.Instance{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: backup.Spec.Instance}, inst); err != nil {
			return ctrl.Result{}, err
		}
		// backup type is validated in validateBackupSpec
		var b oracleBackup
		if backup.Spec.Type == commonv1alpha1.BackupTypeSnapshot {
			b = &snapshotBackup{
				r:      r,
				log:    log,
				backup: backup,
				inst:   inst,
			}
		} else {
			b = &physicalBackup{
				r:      r,
				log:    log,
				backup: backup,
			}
		}
		done, err := b.status(ctx)
		if done {
			if err == nil {
				r.Recorder.Eventf(backup, corev1.EventTypeNormal, "BackupCompleted", "BackupId:%v, Elapsed time: %v", backup.Status.BackupID, k8s.ElapsedTimeFromLastTransitionTime(k8s.FindCondition(backup.Status.Conditions, k8s.Ready), time.Second))
				backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionTrue, k8s.BackupReady, "")
				duration := metav1.Duration{Duration: metav1.Now().Sub(backup.Status.StartTime.Time)}
				backup.Status.Duration = &duration
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

	default:
		log.Info("no action needed", "backupReady", backupReadyCond)
		return ctrl.Result{}, nil
	}
}

// instReady returns non-nil error if instance is not in ready state.
func (r *BackupReconciler) instReady(ctx context.Context, ns, instName string, inst *v1alpha1.Instance, log logr.Logger) error {
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: instName}, inst); err != nil {
		log.Error(err, "error finding instance for backup validation")
		return fmt.Errorf("error finding instance - %v", err)
	}
	if !k8s.ConditionStatusEquals(k8s.FindCondition(inst.Status.Conditions, k8s.Ready), v1.ConditionTrue) {
		log.Error(fmt.Errorf("instance not in ready state"), "Instance not in ready state for backup", "inst.Status.Conditions", inst.Status.Conditions)
		return errors.New("instance is not in a ready state")
	}
	return nil
}

func lroOperationID(backup *v1alpha1.Backup) string {
	return fmt.Sprintf("Backup_%s", backup.GetUID())
}

var preflightCheck = func(ctx context.Context, r *BackupReconciler, namespace, instName string, log logr.Logger) error {
	// Confirm that an external LB is ready.
	svc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Name: fmt.Sprintf(controllers.SvcName, instName), Namespace: namespace}, svc); err != nil {
		return err
	}

	if len(svc.Status.LoadBalancer.Ingress) == 0 {
		return fmt.Errorf("preflight check: physical backup: external LB is NOT ready")
	}
	log.Info("preflight check: physical backup, external LB service is ready", "succeededExecCmd#:", 1, "svc", svc.Name)
	return nil
}
