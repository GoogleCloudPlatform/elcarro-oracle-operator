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
	"fmt"
	"time"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	capb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
	"github.com/go-logr/logr"
	lropb "google.golang.org/genproto/googleapis/longrunning"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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
	if err := r.List(ctx, &backups, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("preflight check: failed to list backups for a restore: %v", err)
	}

	var backup v1alpha1.Backup
	for _, b := range backups.Items {
		if b.Status.BackupID == inst.Spec.Restore.BackupID {
			r.Log.V(1).Info("requested backup found")
			backup = b
		}
	}

	if backup.Spec.Type == "" {
		return nil, fmt.Errorf("preflight check: failed to locate the requested backup %q", inst.Spec.Restore.BackupID)
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

	backupReadyCond := k8s.FindCondition(backup.Status.Conditions, k8s.Ready)
	if !k8s.ConditionStatusEquals(backupReadyCond, v1.ConditionTrue) {
		return nil, fmt.Errorf("preflight check: located a physical backup, but it's not in the ready state: %q", backup.Status)
	}
	r.Log.Info("preflight check for a restore from a physical backup - all DONE", "backup", backup)

	dop := restoreDOP(inst.Spec.Restore.Dop, backup.Spec.Dop)

	caClient, closeConn, err := r.ClientFactory.New(ctx, r, req.Namespace, backup.Spec.Instance)
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
		LroInput:     &capb.LROInput{OperationId: lroRestoreOperationID(physicalRestore, &inst)},
	})
	if err != nil {
		return nil, fmt.Errorf("failed on PhysicalRestore gRPC call: %v", err)
	}

	r.Log.Info("caClient.PhysicalRestore", "response", resp)
	return resp, nil
}

// restoreSnapshot constructs the new PVCs and sets the restore in stsParams struct
// based on the requested snapshot to restore from.
func (r *InstanceReconciler) restoreSnapshot(ctx context.Context, inst v1alpha1.Instance, sts *appsv1.StatefulSet, sp *controllers.StsParams) error {
	if err := r.Delete(ctx, sts); err != nil {
		r.Log.Error(err, "restoreSnapshot: failed to delete the old StatefulSet")
	}
	r.Log.Info("restoreSnapshot: old StatefulSet deleted")

	pvcs, err := controllers.NewPVCs(*sp)
	if err != nil {
		r.Log.Error(err, "NewPVCs failed")
		return err
	}
	r.Log.Info("restoreSnapshot: old PVCs to delete constructed", "pvcs", pvcs)

	for i, pvc := range pvcs {
		pvc.Name = fmt.Sprintf("%s-%s-0", pvc.Name, sp.StsName)
		if err := r.Delete(ctx, &pvc); err != nil {
			r.Log.Error(err, "restoreSnapshot: failed to delete the old PVC", "pvc#", i, "pvc", pvc)
		}
		r.Log.Info("restoreSnapshot: old PVC deleted", "pvc", pvc.Name)

		applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("instance-controller")}
		if err := r.Patch(ctx, &pvc, client.Apply, applyOpts...); err != nil {
			r.Log.Error(err, "restoreSnapshot: failed to patch the deleting of the old PVC")
		}
	}

	sp.Restore = inst.Spec.Restore

	newPVCs, err := controllers.NewPVCs(*sp)
	if err != nil {
		r.Log.Error(err, "NewPVCs failed")
		return err
	}
	newPodTemplate := controllers.NewPodTemplate(*sp, inst.Spec.CDBName, controllers.GetDBDomain(&inst))
	stsRestored, err := controllers.NewSts(*sp, newPVCs, newPodTemplate)
	if err != nil {
		r.Log.Error(err, "restoreSnapshot: failed to construct the restored StatefulSet")
		return err
	}
	sts = stsRestored

	applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("instance-controller")}
	if err := r.Patch(ctx, sts, client.Apply, applyOpts...); err != nil {
		r.Log.Error(err, "failed to patch the restored StatefulSet")
		return err
	}
	r.Log.Info("restoreSnapshot: StatefulSet constructed", "statefulSet", sts, "sts.Status", sts.Status)

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

// forceRestore restores an instance from a backup. This method should be invoked
// only when the force flag in restore spec is set to true.
func (r *InstanceReconciler) forceRestore(ctx context.Context, req ctrl.Request, inst *v1alpha1.Instance, iReadyCond *v1.Condition, sts *appsv1.StatefulSet, sp controllers.StsParams, log logr.Logger) (ctrl.Result, error) {
	k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.RestoreInProgress, fmt.Sprintf("Starting a restore on %s-%d from backup %s (type %s)", time.Now().Format(dateFormat), time.Now().Nanosecond(), inst.Spec.Restore.BackupID, inst.Spec.Restore.BackupType))
	inst.Status.LastRestoreTime = inst.Spec.Restore.RequestTime.DeepCopy()
	inst.Status.BackupID = ""

	if err := r.Status().Update(ctx, inst); err != nil {
		log.Error(err, "failed to update an Instance status (starting a restore)")
		return ctrl.Result{}, err
	}

	backup, err := r.findBackupForRestore(ctx, *inst, req.Namespace)
	if err != nil {
		log.Error(err, "could not find a matching backup")
		r.Recorder.Eventf(inst, corev1.EventTypeWarning, "RestoreFailed", "Could not find a matching backup for BackupID: %v, BackupType: %v", inst.Spec.Restore.BackupID, inst.Spec.Restore.BackupType)
		k8s.InstanceUpsertCondition(&inst.Status, iReadyCond.Type, v1.ConditionFalse, k8s.RestoreFailed, err.Error())
		return ctrl.Result{}, nil
	}

	switch inst.Spec.Restore.BackupType {
	case "Snapshot":
		if err := r.restoreSnapshot(ctx, *inst, sts, &sp); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("restore from a storage snapshot: DONE")

	case "Physical":
		operation, err := r.restorePhysical(ctx, *inst, backup, req)
		if err != nil {
			if !controllers.IsAlreadyExistsError(err) {
				r.Log.Error(err, "PhysicalRestore failed")
				return ctrl.Result{}, err
			}
		} else {
			if operation.Done {
				// we're dealing with non LRO version of restore
				log.V(6).Info("encountered synchronous version of PhysicalRestore")
				log.Info("PhysicalRestore DONE")

				message := fmt.Sprintf("Physical restore done. Elapsed Time: %v", k8s.ElapsedTimeFromLastTransitionTime(k8s.FindCondition(inst.Status.Conditions, k8s.Ready), time.Second))
				r.Recorder.Eventf(inst, corev1.EventTypeNormal, "RestoreComplete", message)
				// non-LRO version sets condition to false, so that it will be set to true and cleaned up in the next reconcile loop
				k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.RestoreComplete, message)
			} else {
				r.Log.Info("PhysicalRestore started")
			}
		}

	default:
		// Not playing games here. A restore (especially the in-place restore)
		// is destructive. It's not about being user-friendly. A user is to
		// be specific as to what kind of backup they want to restore from.
		return ctrl.Result{}, fmt.Errorf("a BackupType is a mandatory parameter for a restore")
	}

	return ctrl.Result{}, nil
}

func (r *InstanceReconciler) handleRestoreInProgress(ctx context.Context, req ctrl.Request,
	inst *v1alpha1.Instance, iReadyCond *v1.Condition, log logr.Logger) (ctrl.Result, error) {

	cleanupLROFunc := func() {}
	// This is to prevent a panic if another thread already resets restore spec.
	if inst.Spec.Restore != nil && inst.Spec.Restore.BackupType == "Physical" && !k8s.ConditionReasonEquals(iReadyCond, k8s.RestoreComplete) {
		id := lroRestoreOperationID(physicalRestore, inst)
		operation, err := controllers.GetLROOperation(r.ClientFactory, ctx, r, req.Namespace, id, inst.Name)
		if err != nil {
			log.Error(err, "GetLROOperation returned an error")
			return ctrl.Result{}, err
		}
		log.Info("GetLROOperation", "response", operation)
		if !operation.Done {
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}

		log.Info("LRO is DONE", "id", id)
		cleanupLROFunc = func() {
			_ = controllers.DeleteLROOperation(r.ClientFactory, ctx, r, req.Namespace, id, inst.Name)
		}

		// handle case when remote LRO completed unsuccessfully
		if operation.GetError() != nil {
			backupID := inst.Spec.Restore.BackupID
			backupType := inst.Spec.Restore.BackupType

			k8s.InstanceUpsertCondition(&inst.Status, iReadyCond.Type, v1.ConditionFalse, k8s.RestoreFailed, fmt.Sprintf("Failed to restore on %s-%d from backup %s (type %s): %s", time.Now().Format(dateFormat),
				time.Now().Nanosecond(), backupID, backupType, operation.GetError().GetMessage()))
			if err := r.Status().Update(ctx, inst); err != nil {
				log.Error(err, "failed to update the instance status")
				return ctrl.Result{}, err
			}

			inst.Spec.Restore = nil
			if err := r.Update(ctx, inst); err != nil {
				log.Error(err, "failed to update the Instance spec (record Restore Failure)")
				return ctrl.Result{}, err
			}
			cleanupLROFunc()
			return ctrl.Result{}, nil
		}
	} else if inst.Spec.Restore != nil && inst.Spec.Restore.BackupType == "Snapshot" {
		if !r.updateProgressCondition(ctx, *inst, req.NamespacedName.Namespace, controllers.RestoreInProgress) {
			log.Info("requeue after 30 seconds")
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}
	}
	// This is to prevent a panic if another thread already resets restore spec.
	if inst.Spec.Restore != nil {
		backupID := inst.Spec.Restore.BackupID
		backupType := inst.Spec.Restore.BackupType

		inst.Spec.Restore = nil
		if err := r.Update(ctx, inst); err != nil {
			log.Error(err, "failed to update the Instance spec (removing the restore bit)")
			return ctrl.Result{}, err
		}
		inst.Status.Description = fmt.Sprintf("Restored on %s-%d from backup %s (type %s)", time.Now().Format(dateFormat),
			time.Now().Nanosecond(), backupID, backupType)
		r.Recorder.Eventf(inst, corev1.EventTypeNormal, "RestoreComplete", inst.Status.Description)
	}
	cleanupLROFunc()

	k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionTrue, k8s.RestoreComplete, "")
	return ctrl.Result{}, nil
}

func restoreInProgress(instReadyCond *v1.Condition) bool {
	return k8s.ConditionStatusEquals(instReadyCond, v1.ConditionFalse) &&
		(k8s.ConditionReasonEquals(instReadyCond, k8s.RestoreComplete) || k8s.ConditionReasonEquals(instReadyCond, k8s.RestoreInProgress))
}

// Create a name for the LRO operation based on instance GUID and restore time.
func lroRestoreOperationID(opType string, instance *v1alpha1.Instance) string {
	return fmt.Sprintf("%s_%s_%s", opType, instance.GetUID(), instance.Status.LastRestoreTime.Format(time.RFC3339))
}
