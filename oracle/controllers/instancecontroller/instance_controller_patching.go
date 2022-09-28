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
	"errors"
	"fmt"
	"reflect"
	"time"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/go-logr/logr"
	snapv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	log "k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

const (
	deploymentPatchingTimeout = 3 * time.Minute
)

var statefulSetImages = []string{"service", "dbinit", "logging_sidecar"}
var deploymentImages = []string{"config", "monitoring"}

// State transition:
// Happy case
// CreateComplete -> PatchingBackupStarted -> PatchingBackupCompleted ->
// -> DeploymentSetPatchingInProgress -> DeploymentSetPatchingComplete ->
// StatefulSetPatchingInProgress -> StatefulSetPatchingComplete -> DatabasePatchingInProgress
// -> DatabasePatchingComplete -> CreateComplete
//
// Unhappy case (*/asterisk mean this state can be short-circuited due to failures in the parent state)
// CreateComplete -> PatchingBackupStarted -> PatchingBackupCompleted ->
// -> DeploymentSetPatchingInProgress* -> DeploymentSetPatchingComplete* ->
// StatefulSetPatchingInProgress* -> StatefulSetPatchingComplete* -> DatabasePatchingInProgress*
// -> DatabasePatchingComplete* -> StatefulSetPatchingFailure/DatabasePatchingFailure -> PatchingRecoveryCompleted
//
// Returns
// * non-empty result if restore state machine needs another reconcile
// * non-empty error if any error occurred
// * empty result and empty error to continue with main reconciliation loop
func (r *InstanceReconciler) patchingStateMachine(req ctrl.Request, instanceReadyCond *v1.Condition, dbInstanceCond *v1.Condition, inst *v1alpha1.Instance, ctx context.Context, stsParams *controllers.StsParams, config *v1alpha1.Config, databasePatchingTimeout time.Duration, log logr.Logger) (ctrl.Result, error, bool) {
	// Conditions not initialized yet
	if instanceReadyCond == nil || dbInstanceCond == nil {
		log.Info("patchingStateMachine: Instance not ready yet, proceed with main reconciliation")
		return ctrl.Result{}, nil, false
	}

	switch instanceReadyCond.Reason {
	case k8s.CreateComplete, k8s.ExportComplete, k8s.RestoreComplete, k8s.ImportComplete, k8s.PatchingRecoveryCompleted:

		// PatchingRecoveryCompleted is also a stable state and we need this check to avoid the infinite loop of retrying Patching with failed images.
		if instanceReadyCond.Reason == k8s.PatchingRecoveryCompleted && reflect.DeepEqual(inst.Spec.Images, inst.Status.LastFailedImages) {
			return ctrl.Result{}, nil, true
		}

		inst.Status.CurrentActiveStateMachine = "PatchingStateMachine"
		if result, err := r.startPatchingBackup(req, ctx, inst, log); err != nil {
			// In case of k8s conflict retry, otherwise switch to failed state
			if !apierrors.IsConflict(err) {
				k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.PatchingBackupFailure, "")
			}
			return result, err, true
		}
		log.Info("patchingStateMachine: CreateComplete->PatchingBackupStarted")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.PatchingBackupStarted, "Patching Backup Started")
		return ctrl.Result{Requeue: true}, nil, true

	case k8s.PatchingBackupStarted:
		completed, err := r.isPatchingBackupCompleted(ctx, *inst)
		if err != nil {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.PatchingBackupFailure, "")
			return ctrl.Result{}, err, true
		} else if !completed {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil, true
		} else {
			log.Info("patchingStateMachine: PatchingBackupStarted->PatchingBackupCompleted")
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.PatchingBackupCompleted, "Patching backup completed, continuing patching")
			return ctrl.Result{Requeue: true}, nil, true
		}

	case k8s.PatchingBackupCompleted:
		// If there are no new images specified with respect to the deployment set skip this state.
		if !isDeploymentSetPatchingRequired(inst.Status.ActiveImages, inst.Spec.Images, inst.Spec.Services) {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DeploymentSetPatchingComplete, "")
			return ctrl.Result{Requeue: true}, nil, true
		}
		// Start control plane agent patching
		if _, err, _ := r.recreateDeployment(ctx, *inst, config, stsParams.Images, log); err != nil {
			log.Info("agentPatchingStateMachine: PatchingBackupCompleted->DeploymentSetPatchingFailure")
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DeploymentSetPatchingFailure, "")
			return ctrl.Result{}, err, true
		}
		log.Info("agentPatchingStateMachine: PatchingBackupCompleted->DeploymentSetPatchingInProgress")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DeploymentSetPatchingInProgress, "")
		return ctrl.Result{Requeue: true}, nil, true

	case k8s.DeploymentSetPatchingInProgress:
		elapsed := k8s.ElapsedTimeFromLastTransitionTime(instanceReadyCond, time.Second)
		if elapsed > deploymentPatchingTimeout {
			msg := fmt.Sprintf("agentPatchingStateMachine: Agent patching timed out after %v", deploymentPatchingTimeout)
			log.Info(msg)
			r.Recorder.Eventf(inst, corev1.EventTypeWarning, "InstanceReady", msg)
			log.Info("agentPatchingStateMachine: DeploymentSetPatchingInProgress->DeploymentSetPatchingFailure")
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DeploymentSetPatchingFailure, msg)
			return ctrl.Result{}, errors.New(msg), true
		}
		if !r.isDeploymentCreationComplete(ctx, req.NamespacedName.Namespace, inst.Name, log) {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil, true
		} else {
			log.Info("agentPatchingStateMachine: DeploymentSetPatchingInProgress->DeploymentSetPatchingComplete")
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DeploymentSetPatchingComplete, "")
			return ctrl.Result{Requeue: true}, nil, true
		}

	case k8s.DeploymentSetPatchingComplete:
		// We know Deployment patching is complete, check status of Oracle
		oracleRunning, err := r.isOracleUpAndRunning(ctx, inst, req.Namespace, log)
		if err != nil {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DatabasePatchingFailure, "Failed to check Oracle status")
			return ctrl.Result{}, err, true
		}
		if !oracleRunning {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil, true
		}
		// If there are no new images specified with respect to the stateful set skip this state.
		if !isStatefulSetPatchingRequired(inst.Status.ActiveImages, inst.Spec.Images) {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.StatefulSetPatchingComplete, "")
			return ctrl.Result{Requeue: true}, nil, true
		}

		// Start software patching
		if _, err, _ := r.startStatefulSetPatching(req, ctx, *inst, stsParams, log); err != nil {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.StatefulSetPatchingFailure, "")
			return ctrl.Result{}, err, true
		}
		log.Info("patchingStateMachine: DeploymentSetPatchingComplete->StatefulSetPatchingInProgress")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.StatefulSetPatchingInProgress, "")
		return ctrl.Result{Requeue: true}, nil, true

	case k8s.DeploymentSetPatchingFailure:
		// Start deployment set patching rollback
		if _, err, _ := r.recreateDeployment(ctx, *inst, config, inst.Status.ActiveImages, log); err != nil {
			log.Info("agentPatchingStateMachine: DeploymentSetPatchingFailure->PatchingRecoveryFailure")
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.PatchingRecoveryFailure, "")
			return ctrl.Result{}, err, true
		}
		log.Info("agentPatchingStateMachine: DeploymentSetPatchingFailure->DeploymentSetPatchingRollbackInProgress")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DeploymentSetPatchingRollbackInProgress, "")
		return ctrl.Result{Requeue: true}, nil, true

	case k8s.DeploymentSetPatchingRollbackInProgress:
		elapsed := k8s.ElapsedTimeFromLastTransitionTime(instanceReadyCond, time.Second)
		if elapsed > deploymentPatchingTimeout {
			msg := fmt.Sprintf("agentPatchingStateMachine: Agent patching timed out after %v", deploymentPatchingTimeout)
			log.Info(msg)
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.PatchingRecoveryFailure, msg)
			return ctrl.Result{}, errors.New(msg), true
		}
		if !r.isDeploymentCreationComplete(ctx, req.NamespacedName.Namespace, inst.Name, log) {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil, true
		} else {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.PatchingRecoveryCompleted, "")
			return ctrl.Result{Requeue: true}, nil, true
		}

	case k8s.StatefulSetPatchingInProgress:
		// Track software patching runtime and terminate if its running beyond timeout interval
		elapsed := k8s.ElapsedTimeFromLastTransitionTime(instanceReadyCond, time.Second)
		if elapsed > databasePatchingTimeout {
			msg := fmt.Sprintf("Software patching timed out after %v", databasePatchingTimeout)
			log.Info(msg)
			r.Recorder.Eventf(inst, corev1.EventTypeWarning, "InstanceReady", msg)
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.StatefulSetPatchingFailure, msg)
			return ctrl.Result{}, errors.New(msg), true
		}
		// Monitor patching progress
		if !r.updateProgressCondition(ctx, *inst, req.NamespacedName.Namespace, k8s.StatefulSetPatchingInProgress, log) {
			log.Info("waiting for STS creation to complete: requeue after 30 seconds")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil, true
		}
		log.Info("patchingStateMachine: StatefulSetPatchingInProgress->StatefulSetPatchingComplete")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.StatefulSetPatchingComplete, "")
		return ctrl.Result{Requeue: true}, nil, true

	case k8s.StatefulSetPatchingComplete:
		// We know STS is up, check status of Oracle
		oracleRunning, err := r.isOracleUpAndRunning(ctx, inst, req.Namespace, log)
		if err != nil {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DatabasePatchingFailure, "Failed to check Oracle status")
			return ctrl.Result{}, err, true
		}
		if !oracleRunning {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil, true
		}
		// Start patching
		if err := r.startDatabasePatching(req, ctx, *inst, log); err != nil {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DatabasePatchingFailure, "Failed to start database patching")
			return ctrl.Result{}, err, true
		}
		log.Info("patchingStateMachine: StatefulSetPatchingComplete->DatabasePatchingInProgress")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DatabasePatchingInProgress, "Calling ApplyDataPatch()")
		return ctrl.Result{Requeue: true}, nil, true

	case k8s.DatabasePatchingInProgress:
		// Track database patching runtime and terminate if its running beyond timeout interval
		elapsed := k8s.ElapsedTimeFromLastTransitionTime(instanceReadyCond, time.Second)
		if elapsed > databasePatchingTimeout {
			msg := fmt.Sprintf("Database patching timed out after %v", databasePatchingTimeout)
			log.Info(msg)
			r.Recorder.Eventf(inst, corev1.EventTypeWarning, "InstanceReady", msg)
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DatabasePatchingFailure, msg)
			return ctrl.Result{}, errors.New(msg), true
		}
		// Monitor patching progress
		done, err := r.isDatabasePatchingDone(ctx, req, *inst, log)
		if err != nil {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DatabasePatchingFailure, "Failed to check datapatch status")
			return ctrl.Result{}, err, true
		}
		if !done {
			log.Info("datapatch still in progress, waiting")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil, true
		}
		log.Info("patchingStateMachine: DatabasePatchingInProgress->DatabasePatchingComplete")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DatabasePatchingComplete, "Calling ApplyDataPatch()")
		return ctrl.Result{Requeue: true}, nil, true

	case k8s.DatabasePatchingComplete:
		log.Info("patchingStateMachine: DatabasePatchingComplete->CreateComplete")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionTrue, k8s.CreateComplete, "")
		// Update current service image path
		inst.Status.ActiveImages = cloneMap(stsParams.Images)
		inst.Status.CurrentActiveStateMachine = ""
		log.Info("patchingStateMachine: patching done", "updating CurrentServiceImage", inst.Spec.Images)
		return ctrl.Result{}, nil, true

	case k8s.StatefulSetPatchingFailure, k8s.DatabasePatchingFailure:
		// Remove old STS/PVC so we can recover.
		if done, err := r.deleteOldSTSandPVCs(ctx, *inst, *stsParams, r.Log); err != nil {
			log.Info("patchingStateMachine: PatchingRecoveryInProgress->PatchingRecoveryFailure")
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.PatchingRecoveryFailure, "Failed to restore from snapshot after patching failure")
			return ctrl.Result{}, err, true
		} else if !done {
			r.Log.Info("STS/PVC removal in progress, waiting")
			return ctrl.Result{Requeue: true}, nil, true
		}

		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.PatchingRecoveryInProgress, "Restoring snapshot due to patching failure")
		log.Info("patchingStateMachine: XXXPatchingFailure->PatchingRecoveryInProgress")
		return ctrl.Result{Requeue: true}, nil, true

	case k8s.PatchingRecoveryInProgress:
		// always retry recoverFromPatchingFailure to keep STS correct
		// in case we flipflop between states.
		if err := r.recoverFromPatchingFailure(ctx, *inst, stsParams); err != nil {
			return ctrl.Result{}, err, true
		}

		if complete := r.isRecoveryFromPatchingFailureComplete(req, ctx, *inst); !complete {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil, true
		}
		shortCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		oracleRunning, err := r.isOracleUpAndRunning(shortCtx, inst, req.Namespace, log)
		if err != nil {
			log.Info("patchingStateMachine: PatchingRecoveryInProgress->PatchingRecoveryFailure")
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.PatchingRecoveryFailure, "Failed to restore from snapshot after patching failure. Could not retrieve status of Oracle")
			return ctrl.Result{}, err, true
		}
		if !oracleRunning {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil, true
		}
		inst.Status.LastFailedImages = cloneMap(inst.Spec.Images)
		log.Info("patchingStateMachine: PatchingRecoveryInProgress->PatchingRecoveryCompleted")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionTrue, k8s.PatchingRecoveryCompleted, "Finished restoring from snapshot after patching failure")
		inst.Status.CurrentActiveStateMachine = ""
		return ctrl.Result{}, nil, true

	default:
		log.Info("patchingStateMachine: no action needed, proceed with main reconciliation")
		return ctrl.Result{}, nil, false
	}
}

func isDeploymentSetPatchingRequired(currentImages map[string]string, newImages map[string]string, enabledServices map[commonv1alpha1.Service]bool) bool {

	// TODO: Make it more generic. Currently monitoring is the only service part of deployment set.
	// Currently there is no direct mapping from service names to image keys.
	if enabled, specified := enabledServices[commonv1alpha1.Monitoring]; specified && enabled {
		if currentImages["monitoring"] != newImages["monitoring"] {
			return true
		}
	}

	if currentImages["config"] != newImages["config"] {
		return true
	}

	return false
}

func isStatefulSetPatchingRequired(currentImages map[string]string, newImages map[string]string) bool {
	for _, image := range statefulSetImages {
		if currentImages[image] != newImages[image] {
			return true
		}
	}
	return false
}

func (r *InstanceReconciler) startPatchingBackup(req ctrl.Request, ctx context.Context, inst *v1alpha1.Instance, log logr.Logger) (ctrl.Result, error) {
	backupID, err := r.prePatchBackup(*inst)
	if err != nil {
		return ctrl.Result{}, err
	}
	log.Info("startPatchingBackup: backup id", "backupID", backupID)

	inst.Status.BackupID = backupID

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *InstanceReconciler) startStatefulSetPatching(req ctrl.Request, ctx context.Context, inst v1alpha1.Instance, stsParams *controllers.StsParams, log logr.Logger) (ctrl.Result, error, bool) {
	log.Info("startStatefulSetPatching, enter")

	// Delete existing stateful set
	existingSTS, err := r.retrieveStatefulSetByName(ctx, req.Namespace, stsParams.StsName)
	if err := r.Delete(ctx, existingSTS); err != nil {
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.StatefulSetPatchingFailure, "Error while deleting STS")
		return ctrl.Result{}, err, false
	}

	// Create new stateful set
	err, sts, _ := r.constructSTSandPVCs(inst, *stsParams, log)
	if err != nil {
		r.Log.Error(err, "failed to create a StatefulSet")
		return ctrl.Result{}, err, false
	}
	applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("instance-controller")}
	if err := r.Patch(ctx, sts, client.Apply, applyOpts...); err != nil {
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.StatefulSetPatchingFailure, "Error while creating patched STS")
		r.Log.Error(err, "failed to patch the restored StatefulSet")
		return ctrl.Result{}, err, false
	}

	k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.StatefulSetPatchingInProgress, "Creating new STS")
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil, true
}

func (r *InstanceReconciler) recoverFromPatchingFailure(ctx context.Context, inst v1alpha1.Instance, stsParams *controllers.StsParams) error {

	log.Info("Restoring from a snapshot due to patching failure. Restoring from Backup ID ", inst.Status.BackupID)
	inst.Spec.Restore = buildRestoreSpecUsingSnapshotBackupID(inst.Status.BackupID)

	stsParams.Images = cloneMap(inst.Status.ActiveImages)
	log.Info("recoverFromPatchingFailure: stsparams", "images", stsParams.Images)
	// Start restore process
	return r.restoreSnapshot(ctx, inst, *stsParams, r.Log)
}

// Checks the progress of the restore operation. Returns true if the restore has completed, false otherwise
func (r *InstanceReconciler) isRecoveryFromPatchingFailureComplete(req ctrl.Request, ctx context.Context, inst v1alpha1.Instance) bool {
	if !r.updateProgressCondition(ctx, inst, req.Namespace, k8s.PatchingRecoveryInProgress, r.Log) {
		log.Info("Recovery from patching failure still in progress...")
		return false
	}
	return true
}

// Retrieves and returns a pointer to the StatefulSet with the given name within the specified namespace
func (r *InstanceReconciler) retrieveStatefulSetByName(ctx context.Context, ns, stsName string) (*appsv1.StatefulSet, error) {
	foundSts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: ns,
		Name:      stsName,
	}, foundSts); err != nil {
		r.Log.Error(err, "Failed to retrieve statefulSet named ", "stsName", stsName, " in namespace ", ns)
		return nil, err
	}
	return foundSts, nil
}

func buildRestoreSpecUsingSnapshotBackupID(backupID string) *v1alpha1.RestoreSpec {
	//The values used below are dummies except the backupID
	return &v1alpha1.RestoreSpec{
		BackupType:       "Snapshot",
		BackupID:         backupID,
		Dop:              0,
		TimeLimitMinutes: 0,
		Force:            false,
		RequestTime:      v1.Now(),
	}
}

func cloneMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func (r *InstanceReconciler) prePatchBackup(inst v1alpha1.Instance) (string, error) {
	// do the same for db instance
	// TODO: these snapshots should get cleaned up at some point

	applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("instance-controller")}

	backupID := fmt.Sprint("patching-backup-", inst.Name, time.Now().Format("20060102"), time.Now().Nanosecond())

	// FIXME: this should not be hard coded
	vsc := "csi-gce-pd-snapshot-class"
	ctx := context.Background()
	log := r.Log.WithValues("Instance", inst.Name)

	for _, diskSpec := range inst.Spec.Disks {
		shortPVCName, mount := controllers.GetPVCNameAndMount(inst.Name, diskSpec.Name)
		fullPVCName := fmt.Sprintf("%s-%s-0", shortPVCName, fmt.Sprintf(controllers.StsName, inst.Name))
		snapshotName := fmt.Sprintf("%s-%s", backupID, mount)
		bk, err := controllers.NewSnapshotInst(&inst, r.SchemeVal, fullPVCName, snapshotName, vsc)
		if err != nil {
			return "", err
		}
		log.V(1).Info("new PatchingBackupSnapshot", "backup", bk)

		if err := r.Patch(ctx, bk, client.Apply, applyOpts...); err != nil {
			return "", err
		}
	}

	return backupID, nil
}

func (r *InstanceReconciler) isPatchingBackupCompleted(ctx context.Context, inst v1alpha1.Instance) (bool, error) {
	backupID := inst.Status.BackupID

	vsc := "csi-gce-pd-snapshot-class"
	log := r.Log.WithValues("Instance", inst.Name)

	for _, diskSpec := range inst.Spec.Disks {
		shortPVCName, mount := controllers.GetPVCNameAndMount(inst.Name, diskSpec.Name)
		fullPVCName := fmt.Sprintf("%s-%s-0", shortPVCName, fmt.Sprintf(controllers.StsName, inst.Name))
		snapshotName := fmt.Sprintf("%s-%s", backupID, mount)
		bk, err := controllers.NewSnapshotInst(&inst, r.SchemeVal, fullPVCName, snapshotName, vsc)
		if err != nil {
			return false, err
		}
		log.V(1).Info("new Backup/Snapshot resource", "backup", bk)

		name := types.NamespacedName{
			Namespace: inst.Namespace,
			Name:      snapshotName,
		}
		snapshot := snapv1.VolumeSnapshot{}
		err = r.Get(ctx, name, &snapshot)
		if err != nil || snapshot.Status == nil {
			return false, err
		}
		if !*snapshot.Status.ReadyToUse {
			return false, nil
		}
	}

	return true, nil
}

func (r *InstanceReconciler) recreateDeployment(ctx context.Context, inst v1alpha1.Instance, config *v1alpha1.Config, images map[string]string, log logr.Logger) (ctrl.Result, error, bool) {

	applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("instance-controller")}

	var enabledServices []commonv1alpha1.Service
	for service, enabled := range inst.Spec.Services {
		if enabled {
			enabledServices = append(enabledServices, service)
		}
	}
	agentParam := controllers.AgentDeploymentParams{
		Inst:           &inst,
		Config:         config,
		Scheme:         r.SchemeVal,
		Name:           fmt.Sprintf(controllers.AgentDeploymentName, inst.Name),
		Images:         images,
		PrivEscalation: false,
		Log:            log,
		Args:           controllers.GetLogLevelArgs(config),
		Services:       enabledServices,
	}
	agentDeployment, err := controllers.NewAgentDeployment(agentParam)
	if err != nil {
		log.Info("createAgentDeployment: error in function NewAgentDeployment")
		log.Error(err, "failed to create a Deployment", "agent deployment", agentDeployment)
		return ctrl.Result{}, err, false
	}
	if result, err := r.deleteOldDeployment(ctx, inst); err != nil {
		return result, err, false
	}
	if err := r.Patch(ctx, agentDeployment, client.Apply, applyOpts...); err != nil {
		log.Error(err, "failed to patch the Deployment", "agent deployment.Status", agentDeployment.Status)
		return ctrl.Result{}, err, false
	}
	log.Info("createAgentDeployment: function Patch succeeded")
	return ctrl.Result{}, nil, true
}

func (r *InstanceReconciler) deleteOldDeployment(ctx context.Context, inst v1alpha1.Instance) (ctrl.Result, error) {
	oldDeployment := &appsv1.Deployment{}
	deplName := fmt.Sprintf(controllers.AgentDeploymentName, inst.Name)
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: inst.Namespace,
		Name:      deplName,
	}, oldDeployment); err != nil {
		r.Log.Error(err, "Failed to retrieve deployment named ", "deplName", deplName, " in namespace ", inst.Namespace)
		return ctrl.Result{}, err
	}
	if err := r.Delete(ctx, oldDeployment); err != nil {
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.DeploymentSetPatchingFailure, "Error while deleting deployment")
		return ctrl.Result{}, err
	}
	//TODO: Add a deterministic delete verification/retry wrapper around async delete
	time.Sleep(10 * time.Second)
	return ctrl.Result{}, nil
}

func (r *InstanceReconciler) isDeploymentCreationComplete(ctx context.Context, ns, instName string, log logr.Logger) bool {

	var foundDeployment appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: fmt.Sprintf("%s-agent-deployment", instName)}, &foundDeployment); err != nil {
		log.Error(err, "isDeploymentCreationComplete: failed to deployment to check status")
		return false
	}
	log.Info("isDeploymentCreationComplete: found deployment", "foundDeployment", foundDeployment)

	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{"instance-agent": fmt.Sprintf("%s-agent", instName)}); err != nil {
		log.Error(err, "isDeploymentCreationComplete: failed to get a list of Pods to check status")
		return false
	}
	log.Info("isDeploymentCreationComplete: found pods", "pods", pods)

	if foundDeployment.Status.UnavailableReplicas > 0 {
		log.Info("isDeploymentCreationComplete: Deployment is not available")
		return false
	}
	log.Info("isDeploymentCreationComplete: found deployment status is", "status", foundDeployment.Status)

	if len(pods.Items) != 1 {
		log.Info("isDeploymentCreationComplete: the number of pods is not 1 ")
		return false
	}

	var foundPod *corev1.Pod
	for index := range pods.Items {
		foundPod = &pods.Items[index]
	}

	log.Info("isDeploymentCreationComplete: pod details are ", "foundPod", foundPod)

	if foundPod.Status.Phase != "Running" {
		log.Info("isDeploymentCreationComplete: agent pod not in running state")
		return false
	}

	for _, podCondition := range foundPod.Status.Conditions {
		if podCondition.Type == "Ready" && podCondition.Status == "False" {
			log.Info("isDeploymentCreationComplete: podCondition.Type ready is False")
			return false
		}
		if podCondition.Type == "ContainersReady" && podCondition.Status == "False" {
			log.Info("isDeploymentCreationComplete: podCondition.Type ContainersReady is False")
			return false
		}
	}
	log.Info("isDeploymentCreationComplete: agent pod in running state")
	return true
}
