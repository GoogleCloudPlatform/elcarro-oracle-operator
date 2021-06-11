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
	"fmt"
	"time"

	"github.com/go-logr/logr"
	snapv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	capb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

var (
	backupName  = "%s-%s-%s-%d"
	pvcNameFull = "%s-u0%d-%s-0"
)

// BackupReconciler reconciles a Backup object.
type BackupReconciler struct {
	client.Client
	Log           logr.Logger
	Scheme        *runtime.Scheme
	ClientFactory controllers.ConfigAgentClientFactory
	Recorder      record.EventRecorder
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

func (r *BackupReconciler) checkSnapshotStatus(ctx context.Context, backup v1alpha1.Backup, ns string, inst *v1alpha1.Instance, log logr.Logger) error {
	log.Info("found a backup request in-progress")

	sel := labels.NewSelector()
	vsLabels := []string{backup.Status.BackupID + "-u02", backup.Status.BackupID + "-u03"}
	req1, err := labels.NewRequirement("name", selection.In, vsLabels)
	if err != nil {
		return err
	}
	sel.Add(*req1)

	req2, err := labels.NewRequirement("namespace", selection.Equals, []string{ns})
	if err != nil {
		return err
	}
	sel.Add(*req2)

	listOpts := []client.ListOption{
		client.InNamespace(ns),
		client.MatchingLabelsSelector{Selector: sel},
	}

	var volSnaps snapv1.VolumeSnapshotList
	if err := r.List(ctx, &volSnaps, listOpts...); err != nil {
		log.Error(err, "failed to get a volume snapshot")
		return err
	}
	log.Info("list of found volume snapshots", "volSnaps", volSnaps)

	if len(volSnaps.Items) < 1 {
		log.Info("no volume snapshots found for a backup request marked as in-progress.", "backup.Status", backup.Status)
		return nil
	}
	log.Info("found a volume snapshot(s) for a backup request in-progress")

	vsStatus := make(map[string]bool)
	for i, vs := range volSnaps.Items {
		log.Info("iterating over volume snapshots", "VolumeSnapshot#", i, "name", vs.Name)
		vsStatus[vs.Name] = false

		if vs.Status == nil {
			return fmt.Errorf("not yet ready: Status missing for Volume Snapshot %s/%s: %v", vs.Namespace, vs.Name, vs)
		}

		if !*vs.Status.ReadyToUse {
			return fmt.Errorf("not yet ready: Status found, but it's not flipped to DONE yet for VolumeSnapshot %s/%s: %v", vs.Namespace, vs.Name, vs.Status)
		}
		log.Info("ready to use status", "VolumeSnapshot#", i, "name", vs, "status", *vs.Status.ReadyToUse)
		vsStatus[vs.Name] = true
	}
	log.Info("summary of VolumeSnapshot statuses", "vsStatus", vsStatus)

	r.Recorder.Eventf(&backup, corev1.EventTypeNormal, "BackupCompleted", "BackupId:%v, Elapsed time: %v", backup.Status.BackupID, k8s.ElapsedTimeFromLastTransitionTime(k8s.FindCondition(backup.Status.Conditions, k8s.Ready), time.Second))
	backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionTrue, k8s.BackupReady, "")

	log.Info("snapshot is ready")
	if err := r.updateBackupStatus(ctx, &backup, inst); err != nil {
		r.Log.Error(err, "failed to flip the snapshot status from in-progress to ready")
		return err
	}

	return nil
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
	if k8s.ConditionReasonEquals(readyCond, k8s.BackupInProgress) {
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

	// Check if the Backup object is already reconciled
	readyCond := k8s.FindCondition(backup.Status.Conditions, k8s.Ready)
	namespace := req.NamespacedName.Namespace
	if k8s.ConditionReasonEquals(readyCond, k8s.BackupReady) || k8s.ConditionReasonEquals(readyCond, k8s.BackupFailed) {
		log.Info("Backup reconciler: nothing to do, backup status", "readyCond", readyCond, "Status", backup.Status)
		return ctrl.Result{}, nil
	}

	// Verify preflight conditions
	var inst v1alpha1.Instance
	// skip backupPreflightCheck if backup is ready
	if !k8s.ConditionStatusEquals(readyCond, v1.ConditionTrue) {
		if err := r.backupPreflightCheck(ctx, req, &backup, &inst); err != nil {
			backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupFailed, err.Error())
			if updateErr := r.updateBackupStatus(ctx, &backup, &inst); updateErr != nil {
				log.Error(updateErr, "unable to update backup status")
			}
			r.Recorder.Event(&backup, corev1.EventTypeWarning, "BackupFailed", err.Error())
			return ctrl.Result{}, err
		}
	}

	if k8s.ConditionReasonEquals(readyCond, k8s.BackupInProgress) {
		if backup.Spec.Type == "Snapshot" {
			if err := r.checkSnapshotStatus(ctx, backup, namespace, &inst, log); err != nil {
				return ctrl.Result{}, err
			}
		} else {
			c, err, done := r.checkLROStatus(ctx, req, &backup, readyCond, &inst, log)
			if !done {
				return c, err
			}
		}

		// Don't proceed to checking new backups requests until all in-progress are resolved.
		log.Info("in-progress backup has been marked as resolved: DONE",
			"new phase", backup.Status.Phase)
		return ctrl.Result{}, nil
	}

	log.Info("new backup request detected", "backup", backup)

	// If a snapshot type backup doesn't have a sub-type set or
	// if it's set to anything other than Instance, force Instance.
	// (this is because it's the only supported sub-type for a snapshot
	// and it's a reasonable default for RMAN backup too).

	log.Info("BEFORE", "backup.Spec.Subtype", backup.Spec.Subtype)
	if backup.Spec.Subtype == "" || (backup.Spec.Type == "Snapshot" && backup.Spec.Subtype != "Instance") {
		backup.Spec.Subtype = "Instance"
	}
	log.Info("AFTER", "backup.Spec.Subtype", backup.Spec.Subtype)

	if err := r.Update(ctx, &backup); err != nil {
		return ctrl.Result{}, err
	}

	timeLimitMinutes := controllers.PhysBackupTimeLimitDefault

	var bktype string

	switch backup.Spec.Type {
	case "Snapshot":
		bktype = "snap"

	case "Physical":
		bktype = "phys"

		// If omitted, the default DOP is 1.
		if backup.Spec.Dop == 0 {
			backup.Spec.Dop = 1
		}

		// If omitted, the default is backupset, not image copy, so flip it to true.
		// If set, just pass it along "as is".
		if backup.Spec.Backupset == nil {
			backup.Spec.Backupset = func() *bool { b := true; return &b }()
		}

		if backup.Spec.TimeLimitMinutes != 0 {
			timeLimitMinutes = time.Duration(backup.Spec.TimeLimitMinutes) * time.Minute
		}

	default:
		return ctrl.Result{}, fmt.Errorf("unsupported backup request type: %q", backup.Spec.Type)
	}

	if backup.Spec.Instance == "" {
		return ctrl.Result{}, fmt.Errorf("spec.Instance is not set in the backup request: %v", backup)
	}

	// Load default preferences (aka "config") if provided by a customer.
	config, err := r.loadConfig(ctx, namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	if config != nil {
		log.Info("customer config loaded", "config", config)
	} else {
		log.Info("no customer specific config found, assuming all defaults")
	}

	vsc, err := controllers.ConfigAttribute("VolumeSnapshotClass", backup.Spec.VolumeSnapshotClass, config)
	if err != nil || vsc == "" {
		return ctrl.Result{}, fmt.Errorf("failed to identify a volumeSnapshotClassName for instance: %q", inst.Name)
	}
	log.Info("VolumeSnapshotClass", "volumeSnapshotClass", vsc)

	backupID := fmt.Sprintf(backupName, inst.Name, time.Now().Format("20060102"), bktype, time.Now().Nanosecond())

	if backup.Spec.Type == "Snapshot" {
		applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("backup-controller")}

		for _, diskSpec := range inst.Spec.Disks {
			shortPVCName, mount := controllers.GetPVCNameAndMount(inst.Name, diskSpec.Name)
			fullPVCName := fmt.Sprintf("%s-%s-0", shortPVCName, fmt.Sprintf(controllers.StsName, inst.Name))
			snapshotName := fmt.Sprintf("%s-%s", backupID, mount)
			bk, err := controllers.NewSnapshot(&backup, r.Scheme, fullPVCName, snapshotName, vsc)
			if err != nil {
				return ctrl.Result{}, err
			}
			log.Info("new Backup/Snapshot resource", "backup", bk)

			if err := r.Patch(ctx, bk, client.Apply, applyOpts...); err != nil {
				return ctrl.Result{}, err
			}
		}
		backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupInProgress, "")
	} else {
		if err := preflightCheck(ctx, r, namespace, backup.Spec.Instance); err != nil {
			log.Error(err, "external LB is not ready")
			return ctrl.Result{}, err
		}

		ctxBackup, cancel := context.WithTimeout(context.Background(), timeLimitMinutes)
		defer cancel()

		caClient, closeConn, err := r.ClientFactory.New(ctxBackup, r, namespace, backup.Spec.Instance)
		if err != nil {
			log.Error(err, "failed to create config agent client")
			return ctrl.Result{}, err
		}
		defer closeConn()

		resp, err := caClient.PhysicalBackup(ctxBackup, &capb.PhysicalBackupRequest{
			BackupSubType: backupSubType(backup.Spec.Subtype),
			BackupItems:   backup.Spec.BackupItems,
			Backupset:     *backup.Spec.Backupset,
			CheckLogical:  backup.Spec.CheckLogical,
			Compressed:    backup.Spec.Compressed,
			Dop:           backup.Spec.Dop,
			Level:         backup.Spec.Level,
			Filesperset:   backup.Spec.Filesperset,
			SectionSize:   backup.Spec.SectionSize,
			LocalPath:     backup.Spec.LocalPath,
			GcsPath:       backup.Spec.GcsPath,
			LroInput:      &capb.LROInput{OperationId: lroOperationID(&backup)},
		})
		if err != nil {
			if !controllers.IsAlreadyExistsError(err) {
				return ctrl.Result{}, fmt.Errorf("failed on PhysicalBackup gRPC call: %v", err)
			}
			log.Info("operation already exists")
			backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupInProgress, "")
		} else {
			log.Info("caClient.PhysicalBackup", "response", resp)
			if resp.Done {
				log.Info("PhysicalBackup succeeded")
				r.Recorder.Eventf(&backup, corev1.EventTypeNormal, "BackupCompleted", "BackupId:%v, Elapsed time: %v", backup.Status.BackupID, k8s.ElapsedTimeFromLastTransitionTime(readyCond, time.Second))
				backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionTrue, k8s.BackupReady, "")
			} else {
				log.Info("PhysicalBackup started")
				backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupInProgress, "")
			}
		}
	}

	if err := r.Update(ctx, &backup); err != nil {
		log.Info("failed to update the backup resource", "backup", backup)
		return ctrl.Result{}, err
	}

	backup.Status.BackupID = backupID
	backup.Status.BackupTime = time.Now().Format("20060102150405")
	if err := r.updateBackupStatus(ctx, &backup, &inst); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("reconciling backup: DONE")

	return ctrl.Result{}, nil
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

// backupPreflightCheck checks if the instance is ready for taking backups.
func (r *BackupReconciler) backupPreflightCheck(ctx context.Context, req ctrl.Request, backup *v1alpha1.Backup, inst *v1alpha1.Instance) error {
	if err := r.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: backup.Spec.Instance}, inst); err != nil {
		r.Log.Error(err, "Error finding instance for backup validation", "backup", backup)
		return fmt.Errorf("Error finding instance - %v", err)
	}
	if !k8s.ConditionStatusEquals(k8s.FindCondition(inst.Status.Conditions, k8s.Ready), v1.ConditionTrue) {
		r.Log.Error(fmt.Errorf("Instance not in ready state for backup"), "Instance not in ready state for backup", "inst.Status.Conditions", inst.Status.Conditions)
		return fmt.Errorf("Instance is not in a ready state")
	}
	return nil
}

func lroOperationID(backup *v1alpha1.Backup) string {
	return fmt.Sprintf("Backup_%s", backup.GetUID())
}

var preflightCheck = func(ctx context.Context, r *BackupReconciler, namespace, instName string) error {
	// Confirm that an external LB is ready.
	svc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Name: fmt.Sprintf(controllers.SvcName, instName), Namespace: namespace}, svc); err != nil {
		return err
	}

	if len(svc.Status.LoadBalancer.Ingress) == 0 {
		return fmt.Errorf("preflight check: physical backup: external LB is NOT ready")
	}
	r.Log.Info("preflight check: physical backup, external LB service is ready", "succeededExecCmd#:", 1, "svc", svc.Name)
	return nil
}

// checkLROStatus checks the status of a long-running operation (LRO) backup. LRO's are done through the db agent and thus needs to be monitored periodically. This method returns true if LRO is done and false if not.
func (r *BackupReconciler) checkLROStatus(ctx context.Context, req ctrl.Request, backup *v1alpha1.Backup, rc *v1.Condition, inst *v1alpha1.Instance, log logr.Logger) (ctrl.Result, error, bool) {
	id := lroOperationID(backup)
	operation, err := controllers.GetLROOperation(r.ClientFactory, ctx, r, req.Namespace, id, backup.Spec.Instance)
	if err != nil {
		log.Error(err, "GetLROOperation error")
		return ctrl.Result{}, err, false
	}
	if operation.Done {
		log.Info("LRO is DONE", "id", id)
		if operation.GetError() != nil {
			log.Error(fmt.Errorf(operation.GetError().GetMessage()), "backup failed")
			r.Recorder.Event(backup, corev1.EventTypeWarning, "BackupFailed", operation.GetError().GetMessage())
			backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.BackupFailed, operation.GetError().GetMessage())
		} else {
			r.Recorder.Eventf(backup, corev1.EventTypeNormal, "BackupCompleted", "BackupId:%v, Elapsed time: %v", backup.Status.BackupID, k8s.ElapsedTimeFromLastTransitionTime(rc, time.Second))
			backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionTrue, k8s.BackupReady, "")
		}
		if err := r.updateBackupStatus(ctx, backup, inst); err != nil {
			log.Error(err, "failed to update the backup resource", "backup", backup)
			return ctrl.Result{}, err, false
		}
		_ = controllers.DeleteLROOperation(r.ClientFactory, ctx, r, req.Namespace, id, backup.Spec.Instance)
		return ctrl.Result{}, nil, true
	}
	log.Info("LRO is in progress", "id", id)
	return ctrl.Result{RequeueAfter: time.Minute}, nil, false
}
