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

package instancecontroller

import (
	"context"
	go_errors "errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	capb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
	lropb "google.golang.org/genproto/googleapis/longrunning"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reconciler for restore logic.
// Invoked when Spec.Restore is present.
// State transition:
// CreateComplete/RestoreFailed -> RestorePreparationInProgress -> RestorePreparationComplete ->
// -> RestoreInProgress -> PostRestoreBootstrapInProgress -> RestoreComplete
// or ... -> RestoreFailed
// Returns
// * non-empty result if restore state machine needs another reconcile
// * non-empty error if any error occurred
// * empty result and error to continue with main reconciliation loop
func (r *InstanceReconciler) restoreStateMachine(req ctrl.Request,
	instanceReadyCond *v1.Condition,
	dbInstanceCond *v1.Condition,
	inst *v1alpha1.Instance,
	ctx context.Context,
	stsParams controllers.StsParams) (ctrl.Result, error) {
	r.Log.Info("restoreStateMachine start")

	// Check instance is provisioned
	if instanceReadyCond == nil || k8s.ConditionReasonEquals(instanceReadyCond, k8s.CreateInProgress) {
		r.Log.Info("restoreStateMachine: instance not ready yet, proceed with main reconciliation")
		return ctrl.Result{}, nil
	}

	// Check database instance is ready for restore
	if dbInstanceCond == nil || (!k8s.ConditionReasonEquals(dbInstanceCond, k8s.AwaitingRestore) && !k8s.ConditionReasonEquals(dbInstanceCond, k8s.CreateComplete)) {
		r.Log.Info("restoreStateMachine: database instance is not ready for restore, proceed with main reconciliation")
		return ctrl.Result{}, nil
	}

	// Check the Force flag
	if !inst.Spec.Restore.Force {
		r.Log.Info("instance is up and running. To replace (restore from a backup), set force=true")
		return ctrl.Result{}, nil
	}

	// Find the requested backup resource
	backup, err := r.findBackupForRestore(ctx, *inst, req.Namespace)
	if err != nil {
		r.setRestoreFailed(ctx, inst, fmt.Sprintf(
			"Could not find a matching backup for BackupID: %v, BackupRef: %v, BackupType: %v",
			inst.Spec.Restore.BackupID, inst.Spec.Restore.BackupRef, inst.Spec.Restore.BackupType))
		return ctrl.Result{}, nil
	}

	// Check if the Backup object is in Ready status
	backupReadyCond := k8s.FindCondition(backup.Status.Conditions, k8s.Ready)
	if !k8s.ConditionStatusEquals(backupReadyCond, v1.ConditionTrue) {
		if k8s.ConditionReasonEquals(backupReadyCond, k8s.BackupFailed) {
			r.setRestoreFailed(ctx, inst, "Backup is in failed state")
			return ctrl.Result{}, nil
		} else {
			r.Log.Info("Backup is in progress, waiting")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	switch instanceReadyCond.Reason {
	// Entry points for restore process
	case k8s.RestoreComplete, k8s.CreateComplete, k8s.RestoreFailed:
		if inst.Spec.Restore.BackupType != "Snapshot" && inst.Spec.Restore.BackupType != "Physical" {
			// Not playing games here. A restore (especially the in-place restore)
			// is destructive. It's not about being user-friendly. A user is to
			// be specific as to what kind of backup they want to restore from.
			r.Log.Error(fmt.Errorf("a BackupType is a mandatory parameter for a restore"), "stopping")
			return ctrl.Result{}, nil
		}
		// Check the request time
		requestTime := inst.Spec.Restore.RequestTime.Rfc3339Copy()
		if inst.Status.LastRestoreTime != nil && !requestTime.After(inst.Status.LastRestoreTime.Time) {
			r.Log.Info(fmt.Sprintf("skipping the restore request as requestTime=%v is not later than the last restore time %v",
				requestTime, inst.Status.LastRestoreTime.Time))
			return ctrl.Result{}, nil
		}
		inst.Status.LastRestoreTime = inst.Spec.Restore.RequestTime.DeepCopy()
		inst.Status.BackupID = ""
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.RestorePreparationInProgress, "")
		if err := r.Status().Update(ctx, inst); err != nil {
			return ctrl.Result{}, err
		}
		r.Log.Info("restoreStateMachine: CreateComplete->RestorePreparationInProgress")
		// Reconcile again
		return ctrl.Result{Requeue: true}, nil
	case k8s.RestorePreparationInProgress:
		switch inst.Spec.Restore.BackupType {
		case "Snapshot":
			// Cleanup STS and PVCs.
			done, err := r.cleanupSTSandPVCs(ctx, *inst, stsParams)
			if err != nil {
				r.setRestoreFailed(ctx, inst, err.Error())
				return ctrl.Result{}, err
			}
			if !done {
				r.Log.Info("STS/PVC removal in progress, waiting")
				return ctrl.Result{RequeueAfter: 5 * time.Second}, err
			}
		case "Physical":
			// Do nothing in this step.
		}
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.RestorePreparationComplete, "")
		r.Log.Info("restoreStateMachine: RestorePreparationInProgress->RestorePreparationComplete")
		// Reconcile again
		return ctrl.Result{Requeue: true}, nil
	case k8s.RestorePreparationComplete:
		// Update status and commit it to k8s before we proceed.
		// This will protect us from a case where we start a restore job but fail to update our status.
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.RestoreInProgress, "")
		if err := r.Status().Update(ctx, inst); err != nil {
			return ctrl.Result{}, err
		}
		r.Log.Info("restoreStateMachine: RestorePreparationComplete->RestoreInProgress")
		switch inst.Spec.Restore.BackupType {
		case "Snapshot":
			// Launch the restore process
			if err := r.restoreSnapshot(ctx, *inst, stsParams); err != nil {
				return ctrl.Result{}, err
			}
			r.Log.Info("restore from a storage snapshot: started")
		case "Physical":
			// Launch the LRO
			operation, err := r.restorePhysical(ctx, *inst, backup, req)
			if err != nil {
				if !controllers.IsAlreadyExistsError(err) {
					r.Log.Error(err, "PhysicalRestore failed")
					return ctrl.Result{}, err
				}
			} else {
				if operation.Done {
					// we're dealing with non LRO version of restore
					r.Log.Info("encountered synchronous version of PhysicalRestore")
					r.Log.Info("PhysicalRestore DONE")
					r.Log.Info("restoreStateMachine: CreateComplete->RestoreComplete")
					message := fmt.Sprintf("Physical restore done. Elapsed Time: %v",
						k8s.ElapsedTimeFromLastTransitionTime(k8s.FindCondition(inst.Status.Conditions, k8s.Ready), time.Second))
					r.setRestoreSucceeded(ctx, inst, message)
				} else {
					r.Log.Info("PhysicalRestore started")
				}
			}
		}
		// Reconcile again
		return ctrl.Result{Requeue: true}, nil
	case k8s.RestoreInProgress:
		done, err := false, error(nil)
		switch inst.Spec.Restore.BackupType {
		case "Snapshot":
			done, err = r.isSnapshotRestoreDone(ctx, req, *inst)
		case "Physical":
			done, err = r.isPhysicalRestoreDone(ctx, req, *inst)
			// Clean up LRO after we are done.
			// The job will remain available for `ttlAfterDelete`.
			if done {
				id := lroRestoreOperationID(physicalRestore, *inst)
				_ = controllers.DeleteLROOperation(r.ClientFactory, ctx, r, req.Namespace, id, inst.Name)
			}
		default:
			r.setRestoreFailed(ctx, inst, "Unknown restore type")
			return ctrl.Result{}, nil
		}
		if err != nil {
			r.setRestoreFailed(ctx, inst, err.Error())
			return ctrl.Result{}, err
		}
		if !done {
			r.Log.Info("restore still in progress, waiting")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.PostRestoreBootstrapInProgress, "")
		// Reconcile again
		return ctrl.Result{Requeue: true}, r.Status().Update(ctx, inst)
	case k8s.PostRestoreBootstrapInProgress:
		caClient, closeConn, err := r.ClientFactory.New(ctx, r, req.Namespace, inst.Name)
		if err != nil {
			r.Log.Error(err, "failed to create config agent client")
			return ctrl.Result{}, err
		}
		defer closeConn()

		if _, err = caClient.BootstrapDatabase(ctx, &capb.BootstrapDatabaseRequest{
			CdbName:      inst.Spec.CDBName,
			DbUniqueName: inst.Spec.DBUniqueName,
			Dbdomain:     controllers.GetDBDomain(inst),
			Mode:         capb.BootstrapDatabaseRequest_Restore,
		}); err != nil {
			r.setRestoreFailed(ctx, inst, fmt.Sprintf("Post restore bootstrap failed with %v", err))
			return ctrl.Result{}, nil
		}

		description := fmt.Sprintf("Restored on %s-%d from backup %s (type %s)", time.Now().Format(dateFormat),
			time.Now().Nanosecond(), inst.Spec.Restore.BackupID, inst.Spec.Restore.BackupType)
		r.setRestoreSucceeded(ctx, inst, description)
	default:
		r.Log.Info("restoreStateMachine: no action needed, proceed with main reconciliation")
	}
	return ctrl.Result{}, nil
}

// Update spec and status of the instance to reflect restore success.
func (r *InstanceReconciler) setRestoreSucceeded(ctx context.Context, inst *v1alpha1.Instance, message string) {
	r.Log.Info("Restore succeeded")
	description := fmt.Sprintf("Restored on %s-%d from backup %s (type %s)", time.Now().Format(dateFormat),
		time.Now().Nanosecond(), inst.Spec.Restore.BackupID, inst.Spec.Restore.BackupType)
	// Create event.
	r.Recorder.Eventf(inst, corev1.EventTypeWarning, "RestoreComplete", message)
	// Remove restore spec. Update the inst object in place.
	inst.Spec.Restore = nil
	if err := r.Update(ctx, inst); err != nil {
		r.Log.Error(err, "failed to update instance spec")
	}
	// Update status.
	k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionTrue, k8s.RestoreComplete, message)
	inst.Status.Description = description
}

// Update spec and status of the instance to reflect restore failure.
func (r *InstanceReconciler) setRestoreFailed(ctx context.Context, inst *v1alpha1.Instance, reason string) {
	r.Log.Error(go_errors.New(reason), "Restore failed")
	// Create event.
	r.Recorder.Eventf(inst, corev1.EventTypeWarning, "RestoreFailed", reason)
	// Remove restore spec. Update the inst object in place.
	inst.Spec.Restore = nil
	if err := r.Update(ctx, inst); err != nil {
		r.Log.Error(err, "failed to update instance spec")
	}
	// Update status.
	k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.RestoreFailed, reason)
}

// Check for Snapshot restore status
// Return (true, nil) if job is done
// Return (false, nil) if job still in progress
// Return (false, err) if the job failed
func (r *InstanceReconciler) isSnapshotRestoreDone(ctx context.Context, req ctrl.Request,
	inst v1alpha1.Instance) (bool, error) {
	// Re-use STS progress function from instance controller.
	// It will return err = nil when the STS creation is complete.
	_, err := r.statusProgress(ctx, inst.Namespace, fmt.Sprintf(controllers.StsName, inst.Name))
	r.Log.Info(fmt.Sprintf("Snapshot restore status: %s", err))
	return err == nil, nil
}

func restoreDOP(r, b int32) int32 {
	// Determine the restore DOP. The order of preference is:
	// - If DOP is explicitly requested in the restore section, take it.
	// - If not and the DOP was specified when a backup was taken, use it.
	// - Otherwise, use the default, which is 1.
	if r > 0 {
		return r
	}

	if b > 0 {
		return b
	}

	return 1
}

// findBackupForRestore fetches the backup with the backup_id specified in the spec for initiating the instance restore.
func (r *InstanceReconciler) findBackupForRestore(ctx context.Context, inst v1alpha1.Instance, namespace string) (*v1alpha1.Backup, error) {
	var backups v1alpha1.BackupList
	var backup v1alpha1.Backup

	backupRef := inst.Spec.Restore.BackupRef
	if backupRef == nil && inst.Spec.Restore.BackupID == "" {
		return nil, fmt.Errorf("preflight check: either BackupID or BackupRef must be set to perform a restore")
	}
	if backupRef != nil {
		// find backup based on BackupRef
		if err := r.Get(ctx, types.NamespacedName{Name: backupRef.Name, Namespace: backupRef.Namespace}, &backup); err != nil {
			return nil, fmt.Errorf("preflight check: failed to get backup for a restore: %v, backupRef: %v", err, backupRef)
		}
	} else {
		if err := r.List(ctx, &backups, client.InNamespace(namespace)); err != nil {
			return nil, fmt.Errorf("preflight check: failed to list backups for a restore: %v", err)
		}

		for _, b := range backups.Items {
			if b.Status.BackupID == inst.Spec.Restore.BackupID {
				r.Log.Info("requested backup found")
				backup = b
			}
		}
		if backup.Spec.Type == "" {
			return nil, fmt.Errorf("preflight check: failed to locate the requested backup %q", inst.Spec.Restore.BackupID)
		}
	}

	if backup.Spec.Type != inst.Spec.Restore.BackupType {
		return nil, fmt.Errorf("preflight check: located a backup of type %q, wanted: %q", backup.Spec.Type, inst.Spec.Restore.BackupType)
	}

	return &backup, nil
}

// restorePhysical runs the pre-flight checks and if all is good
// it makes a gRPC call to a PhysicalRestore.
func (r *InstanceReconciler) restorePhysical(ctx context.Context, inst v1alpha1.Instance, backup *v1alpha1.Backup, req ctrl.Request) (*lropb.Operation, error) {
	// Confirm that an external LB is ready.
	if err := restorePhysicalPreflightCheck(ctx, r, req.Namespace, inst.Name); err != nil {
		return nil, err
	}
	if !*backup.Spec.Backupset {
		return nil, fmt.Errorf("preflight check: located a physical backup, but in this release the auto-restore is only supported from a Backupset backup: %v", backup.Spec.Backupset)
	}
	if backup.Spec.Subtype != "Instance" {
		return nil, fmt.Errorf("preflight check: located a physical backup, but in this release the auto-restore is only supported from a Backupset taken at the Instance level: %q", backup.Spec.Subtype)
	}
	r.Log.Info("preflight check for a restore from a physical backup - all DONE", "backup", backup)
	dop := restoreDOP(inst.Spec.Restore.Dop, backup.Spec.Dop)
	caClient, closeConn, err := r.ClientFactory.New(ctx, r, req.Namespace, inst.Name)
	if err != nil {
		r.Log.Error(err, "failed to create config agent client")
		return nil, err
	}
	defer closeConn()
	timeLimitMinutes := controllers.PhysBackupTimeLimitDefault * 3
	if inst.Spec.Restore.TimeLimitMinutes != 0 {
		timeLimitMinutes = time.Duration(inst.Spec.Restore.TimeLimitMinutes) * time.Minute
	}
	ctxRestore, cancel := context.WithTimeout(context.Background(), timeLimitMinutes)
	defer cancel()
	resp, err := caClient.PhysicalRestore(ctxRestore, &capb.PhysicalRestoreRequest{
		InstanceName: inst.Name,
		CdbName:      inst.Spec.CDBName,
		Dop:          dop,
		LocalPath:    backup.Spec.LocalPath,
		GcsPath:      backup.Spec.GcsPath,
		LroInput:     &capb.LROInput{OperationId: lroRestoreOperationID(physicalRestore, inst)},
	})
	if err != nil {
		return nil, fmt.Errorf("failed on PhysicalRestore gRPC call: %v", err)
	}
	r.Log.Info("caClient.PhysicalRestore", "LRO", lroRestoreOperationID(physicalRestore, inst), "response", resp)
	return resp, nil
}

// restoreSnapshot constructs the new PVCs and sets the restore in stsParams struct
// based on the requested snapshot to restore from.
func (r *InstanceReconciler) restoreSnapshot(ctx context.Context,
	inst v1alpha1.Instance, sp controllers.StsParams) error {
	// Set Restore field in sts params.
	sp.Restore = inst.Spec.Restore
	// Create PVC and STS objects from sts params (will use restore logic).
	err, sts, _ := r.constructSTSandPVCs(ctx, inst, sp)
	if err != nil {
		r.Log.Error(err, "failed to create a StatefulSet")
		return err
	}
	applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("instance-controller")}
	if err := r.Patch(ctx, sts, client.Apply, applyOpts...); err != nil {
		r.Log.Error(err, "failed to patch the restored StatefulSet")
		return err
	}
	r.Log.Info("restoreSnapshot: updated StatefulSet created", "statefulSet", sts, "sts.Status", sts.Status)
	return nil
}

// extracted for testing.
var restorePhysicalPreflightCheck = func(ctx context.Context, r *InstanceReconciler, namespace, instName string) error {
	svc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Name: fmt.Sprintf(controllers.SvcName, instName), Namespace: namespace}, svc); err != nil {
		return err
	}

	if len(svc.Status.LoadBalancer.Ingress) == 0 {
		return fmt.Errorf("preflight check: physical backup: external LB is NOT ready")
	}
	r.Log.Info("preflight check: restore from a physical backup, external LB service is ready", "succeededExecCmd#:", 1, "svc", svc.Name)

	return nil
}

// Check for Physical restore LRO job status
// Return (true, nil) if LRO is done without errors.
// Return (true, err) if LRO is done with an error.
// Return (false, nil) if LRO still in progress.
// Return (false, err) if other error occurred.
func (r *InstanceReconciler) isPhysicalRestoreDone(ctx context.Context, req ctrl.Request,
	inst v1alpha1.Instance) (bool, error) {
	id := lroRestoreOperationID(physicalRestore, inst)
	operation, err := controllers.GetLROOperation(r.ClientFactory, ctx, r, req.Namespace, id, inst.Name)
	if err != nil {
		r.Log.Error(err, "GetLROOperation returned an error")
		return false, err
	}
	r.Log.Info("GetLROOperation", "response", operation)
	if !operation.Done {
		return false, nil
	}
	r.Log.Info("LRO is DONE, ", "id", id)
	// handle case when remote LRO completed unsuccessfully
	if operation.GetError() != nil {
		backupID := inst.Spec.Restore.BackupID
		backupType := inst.Spec.Restore.BackupType

		return true, fmt.Errorf("Failed to restore on %s-%d from backup %s (type %s): %s. %v", time.Now().Format(dateFormat),
			time.Now().Nanosecond(), backupID, backupType, operation.GetError().GetMessage(), err)
	}

	return true, nil
}

// Create a name for the LRO operation based on instance GUID and restore time.
func lroRestoreOperationID(opType string, instance v1alpha1.Instance) string {
	return fmt.Sprintf("%s_%s_%s", opType, instance.GetUID(), instance.Status.LastRestoreTime.Format(time.RFC3339))
}

// Return STS and PVC objects from given sts params.
func (r *InstanceReconciler) constructSTSandPVCs(ctx context.Context,
	inst v1alpha1.Instance, sp controllers.StsParams) (error, *appsv1.StatefulSet, []corev1.PersistentVolumeClaim) {
	// Create PVCs.
	newPVCs, err := controllers.NewPVCs(sp)
	if err != nil {
		r.Log.Error(err, "createSTSandPVC failed")
		return err, nil, nil
	}
	// Create STS
	newPodTemplate := controllers.NewPodTemplate(sp, inst.Spec.CDBName, controllers.GetDBDomain(&inst))
	sts, err := controllers.NewSts(sp, newPVCs, newPodTemplate)
	if err != nil {
		r.Log.Error(err, "failed to create a StatefulSet", "sts", sts)
		return err, nil, nil
	}
	r.Log.Info("StatefulSet constructed", "sts", sts, "sts.Status", sts.Status, "inst.Status", inst.Status)
	return nil, sts, newPVCs
}

// cleanupSTSandPVCs removes old STS and PVCs before restoring from snapshot.
// Return (true, nil) if all done.
// Return (false, nil) if still in progress.
// Return (false, err) if unrecoverable error occurred.
func (r *InstanceReconciler) cleanupSTSandPVCs(ctx context.Context,
	inst v1alpha1.Instance, sp controllers.StsParams) (bool, error) {
	// Create PVC and STS objects from sts params.
	err, sts, newPVCs := r.constructSTSandPVCs(ctx, inst, sp)
	if err != nil {
		r.Log.Error(err, "failed to create a StatefulSet")
		return false, err
	}
	// Check if old STS still exists.
	existingSTS := appsv1.StatefulSet{}
	stsKey := client.ObjectKey{Namespace: sts.Namespace, Name: sts.Name}
	err = r.Get(ctx, stsKey, &existingSTS)
	// If STS exists delete it and restart reconciling.
	if err == nil {
		r.Log.Info("deleting sts", "name", sts.Name)
		if err := r.Delete(ctx, sts); err != nil {
			r.Log.Error(err, "restoreSnapshot: failed to delete the old STS")
		}
		r.Log.Info("deleted STS, need to reconcile again")
		return false, nil
	} else if errors.IsNotFound(err) {
		// Object is gone
	} else {
		// Other unrecoverable error
		r.Log.Error(err, "unrecoverable error")
		return false, err
	}
	for i, pvc := range newPVCs {
		pvc.Name = fmt.Sprintf("%s-%s-0", pvc.Name, sp.StsName)
		// Check if this PVC still exists.
		existingPVC := corev1.PersistentVolumeClaim{}
		pvcKey := client.ObjectKey{Namespace: pvc.Namespace, Name: pvc.Name}
		err := r.Get(ctx, pvcKey, &existingPVC)
		// If PVC exists delete it and restart reconciling.
		if err == nil {
			r.Log.Info("deleting pvc", "name", pvc.Name)
			if err := r.Delete(ctx, &pvc); err != nil {
				r.Log.Error(err, "cleanupSTSandPVCs: failed to delete the old PVC", "pvc#", i, "pvc", pvc)
			}
			r.Log.Info("deleted PVC, need to reconcile again")
			return false, nil
		} else if errors.IsNotFound(err) {
			// Object is gone
		} else {
			// Other unrecoverable error
			r.Log.Error(err, "Unrecoverable error")
			return false, err
		}
	}
	r.Log.Info("All the existing STS and PVCs are gone.")
	return true, nil
}
