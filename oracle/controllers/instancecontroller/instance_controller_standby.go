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
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

const (
	standbyErrorRetryInterval = time.Second * 60
	// StandbyReconcileInterval is the reconcile interval for a standby instance.
	StandbyReconcileInterval = time.Second * 60
	createStandby            = "CreateStandby"
)

func isStandbyDR(inst *v1alpha1.Instance) bool {
	if inst.Spec.ReplicationSettings != nil {
		return true
	}
	cond := k8s.FindCondition(inst.Status.Conditions, k8s.StandbyDRReady)
	return cond != nil && cond.Status != metav1.ConditionTrue
}

func (r *InstanceReconciler) standbyStateMachine(ctx context.Context, inst *v1alpha1.Instance, log logr.Logger) (ctrl.Result, error) {
	log.Info("Running standby DR state machine")
	// Our initial state is equivalent to StandbyVerifyFailed. See state machine.
	state := k8s.StandbyDRVerifyFailed
	if standbyCond := k8s.FindCondition(inst.Status.Conditions, k8s.StandbyDRReady); standbyCond != nil {
		state = standbyCond.Reason
	}

	switch state {
	case k8s.StandbyDRVerifyFailed:
		externalErrMsgs, err := r.verifySettings(ctx, inst)
		if err != nil {
			log.Error(err, "verify settings failed")
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRVerifyFailed,
				"validate replication settings failed", internalErrToMsg(err))
			return ctrl.Result{RequeueAfter: standbyErrorRetryInterval}, nil
		}
		if len(externalErrMsgs) > 0 {
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRVerifyFailed,
				"validate replication settings failed", externalErrMsgs...)
			return ctrl.Result{RequeueAfter: standbyErrorRetryInterval}, nil
		}
		r.updateStandbyDataReplicationStatus(ctx,
			inst, metav1.ConditionFalse,
			k8s.StandbyDRVerifyCompleted,
			"validate replication settings completed")
		return ctrl.Result{Requeue: true}, nil

	case k8s.StandbyDRVerifyCompleted:
		inst.Status.CurrentReplicationSettings = inst.Spec.ReplicationSettings
		r.updateStandbyDataReplicationStatus(ctx,
			inst, metav1.ConditionFalse,
			k8s.StandbyDRCreateInProgress,
			"create standby instance in progress")
		return ctrl.Result{Requeue: true}, nil

	case k8s.StandbyDRCreateInProgress:
		if inst.Spec.ReplicationSettings == nil {
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRCreateInProgress,
				replicationSettingsNilErr(inst.Status.CurrentReplicationSettings))
			return ctrl.Result{}, nil
		}
		inst.Status.CurrentReplicationSettings = inst.Spec.ReplicationSettings
		operationId := lroOperationID(createStandby, inst)
		credentialReq, err := toCredentialReq(inst.Spec.ReplicationSettings.PrimaryUser)
		if err != nil {
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRCreateInProgress,
				"create standby instance failed", internalErrToMsg(err))
			return ctrl.Result{RequeueAfter: standbyErrorRetryInterval}, nil
		}
		operation, err := controllers.CreateStandby(ctx, r, r.DatabaseClientFactory, inst.Namespace, inst.Name, controllers.CreateStandbyRequest{
			PrimaryHost:         inst.Spec.ReplicationSettings.PrimaryHost,
			PrimaryPort:         inst.Spec.ReplicationSettings.PrimaryPort,
			PrimaryService:      inst.Spec.ReplicationSettings.PrimaryServiceName,
			PrimaryUser:         inst.Spec.ReplicationSettings.PrimaryUser.Name,
			PrimaryCredential:   credentialReq,
			BackupGcsPath:       inst.Spec.ReplicationSettings.BackupURI,
			StandbyDbDomain:     inst.Spec.DBDomain,
			StandbyDbUniqueName: inst.Spec.DBUniqueName,
			StandbyLogDiskSize:  findLogDiskSize(inst),
			LroInput:            &controllers.LROInput{OperationId: operationId},
		})
		if err != nil {
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRCreateFailed,
				"create standby instance failed", internalErrToMsg(err))
			return ctrl.Result{}, nil
		}
		if operation.GetError() != nil {
			controllers.DeleteLROOperation(ctx, r.DatabaseClientFactory, r, operationId, inst.Namespace, inst.Name)
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRCreateFailed,
				"create standby instance failed", operation.GetError().GetMessage())
			return ctrl.Result{}, nil
		} else if !operation.Done {
			log.Info("create standby still in progress")
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRCreateInProgress,
				"create standby instance in progress")
			return ctrl.Result{RequeueAfter: StandbyReconcileInterval}, nil
		}
		r.updateStandbyDataReplicationStatus(ctx,
			inst, metav1.ConditionFalse,
			k8s.StandbyDRCreateCompleted,
			"create standby instance completed")
		return ctrl.Result{}, nil

	case k8s.StandbyDRCreateFailed:
		return ctrl.Result{}, nil

	case k8s.StandbyDRCreateCompleted:
		if inst.Spec.ReplicationSettings == nil {
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRCreateCompleted,
				replicationSettingsNilErr(inst.Status.CurrentReplicationSettings))
			return ctrl.Result{}, nil
		}
		inst.Status.CurrentReplicationSettings = inst.Spec.ReplicationSettings
		if err := r.reconcileDataGuard(ctx, inst); err != nil {
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRSetUpDataGuardFailed,
				"set up Data Guard failed", internalErrToMsg(err))
			return ctrl.Result{RequeueAfter: standbyErrorRetryInterval}, nil
		}
		r.updateStandbyDataReplicationStatus(ctx,
			inst, metav1.ConditionFalse,
			k8s.StandbyDRSetUpDataGuardCompleted,
			"set up Data Guard completed")
		return ctrl.Result{Requeue: true}, nil

	case k8s.StandbyDRSetUpDataGuardFailed:
		if inst.Spec.ReplicationSettings == nil {
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRSetUpDataGuardFailed,
				replicationSettingsNilErr(inst.Status.CurrentReplicationSettings))
			return ctrl.Result{}, nil
		}
		inst.Status.CurrentReplicationSettings = inst.Spec.ReplicationSettings

		if err := r.reconcileDataGuard(ctx, inst); err != nil {
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRSetUpDataGuardFailed,
				"set up Data Guard failed", internalErrToMsg(err))
			return ctrl.Result{RequeueAfter: standbyErrorRetryInterval}, nil
		}
		r.updateStandbyDataReplicationStatus(ctx,
			inst, metav1.ConditionFalse,
			k8s.StandbyDRSetUpDataGuardCompleted,
			"set up Data Guard completed")
		return ctrl.Result{Requeue: true}, nil

	case k8s.StandbyDRSetUpDataGuardCompleted:
		r.updateStandbyDataReplicationStatus(ctx,
			inst, metav1.ConditionFalse,
			k8s.StandbyDRDataGuardReplicationInProgress,
			"Data Guard data replication in progress")
		return ctrl.Result{Requeue: true}, nil

	case k8s.StandbyDRDataGuardReplicationInProgress:
		if inst.Spec.ReplicationSettings == nil {
			if err := r.reconcilePromoteStandby(ctx, inst, log); err != nil {
				r.updateStandbyDataReplicationStatus(ctx,
					inst, metav1.ConditionFalse,
					k8s.StandbyDRPromoteFailed,
					"promote standby failed", internalErrToMsg(err))
				return ctrl.Result{}, nil
			}
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRPromoteCompleted,
				"promote standby completed")
			return ctrl.Result{Requeue: true}, nil
		}
		if err := r.reconcileDataGuard(ctx, inst); err != nil {
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRDataGuardReplicationInProgress,
				"Data Guard data replication in progress with errors", internalErrToMsg(err))
			r.updateDataGuardStatus(ctx, inst, standbyErrorRetryInterval, log)
			return ctrl.Result{RequeueAfter: standbyErrorRetryInterval}, nil
		}
		r.updateStandbyDataReplicationStatus(ctx,
			inst, metav1.ConditionFalse,
			k8s.StandbyDRDataGuardReplicationInProgress,
			"Data Guard data replication in progress")
		r.updateDataGuardStatus(ctx, inst, StandbyReconcileInterval, log)
		return ctrl.Result{RequeueAfter: StandbyReconcileInterval}, nil

	case k8s.StandbyDRPromoteFailed:
		if err := r.reconcilePromoteStandby(ctx, inst, log); err != nil {
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRPromoteFailed,
				"promote standby failed", internalErrToMsg(err))
			return ctrl.Result{RequeueAfter: standbyErrorRetryInterval}, nil
		}
		r.updateStandbyDataReplicationStatus(ctx,
			inst, metav1.ConditionFalse,
			k8s.StandbyDRPromoteCompleted,
			"promote standby completed")
		return ctrl.Result{Requeue: true}, nil

	case k8s.StandbyDRPromoteCompleted, k8s.StandbyDRBootstrapFailed:
		inst.Status.CurrentReplicationSettings = nil
		inst.Status.DataGuardOutput = nil
		err := r.bootstrapStandby(ctx, inst)
		if err != nil {
			r.updateStandbyDataReplicationStatus(ctx,
				inst, metav1.ConditionFalse,
				k8s.StandbyDRBootstrapFailed,
				"bootstrap standby failed", internalErrToMsg(err))
			return ctrl.Result{RequeueAfter: standbyErrorRetryInterval}, nil
		}
		r.updateStandbyDataReplicationStatus(ctx,
			inst, metav1.ConditionTrue,
			k8s.StandbyDRBootstrapCompleted,
			"bootstrap standby completed")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready,
			metav1.ConditionTrue, k8s.CreateComplete,
			"bootstrap standby completed")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady,
			metav1.ConditionTrue, k8s.CreateComplete,
			"bootstrap standby completed")
		return ctrl.Result{Requeue: true}, nil

	case k8s.StandbyDRBootstrapCompleted:
		r.updateStandbyDataReplicationStatus(ctx,
			inst, metav1.ConditionTrue,
			k8s.StandbyDRBootstrapCompleted,
			"bootstrap standby completed")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready,
			metav1.ConditionTrue, k8s.CreateComplete,
			"bootstrap standby completed")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady,
			metav1.ConditionTrue, k8s.CreateComplete,
			"bootstrap standby completed")

		return ctrl.Result{Requeue: true}, nil

	default:
		log.Info("standbyStateMachine: no action needed, proceed with main reconciliation", "unknown state", state)
		return ctrl.Result{}, nil
	}
}

func (r *InstanceReconciler) verifySettings(ctx context.Context, inst *v1alpha1.Instance) (externalErrMsgs []string, err error) {
	if inst.Spec.DBUniqueName == "" {
		externalErrMsgs = append(externalErrMsgs, "spec.dbUniqueName is required for standby replication, try adding spec.dbUniqueName in the instance Kubernetes manifest.")
		return externalErrMsgs, nil
	}
	if inst.Spec.CDBName == "" {
		externalErrMsgs = append(externalErrMsgs, "spec.cdbName is required for standby replication, try adding spec.cdbName in the instance Kubernetes manifest.")
		return externalErrMsgs, nil
	}
	if inst.Spec.Images == nil || inst.Spec.Images["service"] == "" {
		externalErrMsgs = append(externalErrMsgs, "spec.images.service is required for standby replication, try adding spec.images.service in the instance Kubernetes manifest.")
		return externalErrMsgs, nil
	}
	if inst.Spec.ReplicationSettings == nil {
		externalErrMsgs = append(externalErrMsgs, "spec.replicationSettings is required for standby replication, try adding spec.replicationSettings in the instance Kubernetes manifest.")
		return externalErrMsgs, nil
	}
	if inst.Spec.ReplicationSettings.PrimaryUser.GsmSecretRef == nil {
		externalErrMsgs = append(externalErrMsgs, "spec.replicationSettings.primaryCredential.gsmSecretRef is required for standby replication, "+
			"try creating a secret to store password in Google Secret Manager and add corresponding spec.replicationSettings.primaryCredential.gsmSecretRef in the instance Kubernetes manifest.")
		return externalErrMsgs, nil
	}
	if inst.Spec.ReplicationSettings.PrimaryUser.Name != "sys" {
		externalErrMsgs = append(externalErrMsgs, "spec.replicationSettings.primaryUser.name must be sys for standby replication.")
		return externalErrMsgs, nil
	}

	credentialReq, err := toCredentialReq(inst.Spec.ReplicationSettings.PrimaryUser)
	if err != nil {
		return nil, err
	}

	resp, err := controllers.VerifyStandbySettings(ctx, r, r.DatabaseClientFactory, inst.Namespace, inst.Name, controllers.VerifyStandbySettingsRequest{
		PrimaryHost:         inst.Spec.ReplicationSettings.PrimaryHost,
		PrimaryPort:         inst.Spec.ReplicationSettings.PrimaryPort,
		PrimaryService:      inst.Spec.ReplicationSettings.PrimaryServiceName,
		PrimaryUser:         inst.Spec.ReplicationSettings.PrimaryUser.Name,
		PrimaryCredential:   credentialReq,
		StandbyDbUniqueName: inst.Spec.DBUniqueName,
		StandbyCdbName:      inst.Spec.CDBName,
		BackupGcsPath:       inst.Spec.ReplicationSettings.BackupURI,
		PasswordFileGcsPath: inst.Spec.ReplicationSettings.PasswordFileURI,
		StandbyVersion:      inst.Spec.Version,
	})

	if err != nil {
		return nil, err
	}

	settingErrs := resp.Errors
	if settingErrs != nil && len(settingErrs) > 0 {
		for _, settingErr := range settingErrs {
			externalErrMsgs = append(externalErrMsgs, fmt.Sprintf("%s: %s", settingErr.Type.String(), settingErr.Detail))
		}
	}

	return externalErrMsgs, nil
}

func (r *InstanceReconciler) reconcileDataGuard(ctx context.Context, inst *v1alpha1.Instance) error {
	standbyHost, err := r.getStandbyHost(ctx, inst)
	if err != nil {
		return err
	}

	credentialReq, err := toCredentialReq(inst.Spec.ReplicationSettings.PrimaryUser)
	if err != nil {
		return err
	}

	if err := controllers.SetUpDataGuard(ctx, r, r.DatabaseClientFactory, inst.Namespace, inst.Name, controllers.SetUpDataGuardRequest{
		PrimaryHost:         inst.Spec.ReplicationSettings.PrimaryHost,
		PrimaryPort:         inst.Spec.ReplicationSettings.PrimaryPort,
		PrimaryService:      inst.Spec.ReplicationSettings.PrimaryServiceName,
		PrimaryUser:         inst.Spec.ReplicationSettings.PrimaryUser.Name,
		PrimaryCredential:   credentialReq,
		StandbyDbUniqueName: inst.Spec.DBUniqueName,
		StandbyHost:         standbyHost,
		PasswordFileGcsPath: inst.Spec.ReplicationSettings.PasswordFileURI,
	}); err != nil {
		return err
	}
	inst.Status.CurrentReplicationSettings = inst.Spec.ReplicationSettings
	return nil
}

func (r *InstanceReconciler) updateDataGuardStatus(ctx context.Context, inst *v1alpha1.Instance, interval time.Duration, log logr.Logger) {
	// decide whether to update Data Guard output,
	// This is a workaround to avoid the reconciliation call again due to status update.
	if inst.Status.DataGuardOutput != nil &&
		metav1.Now().Sub(inst.Status.DataGuardOutput.LastUpdateTime.Time) < interval {
		log.Info("skipped Data Guard update", "last update time", inst.Status.DataGuardOutput.LastUpdateTime)
		return
	}

	resp, err := controllers.DataGuardStatus(ctx, r, r.DatabaseClientFactory, inst.Namespace, inst.Name, controllers.DataGuardStatusRequest{StandbyDbUniqueName: inst.Spec.DBUniqueName})
	if err == nil {
		inst.Status.DataGuardOutput = &v1alpha1.DataGuardOutput{
			LastUpdateTime: metav1.Now(),
			StatusOutput:   resp.Output,
		}
	} else {
		inst.Status.DataGuardOutput = &v1alpha1.DataGuardOutput{
			LastUpdateTime: metav1.Now(),
			StatusOutput:   []string{internalErrToMsg(err)},
		}
	}
}

func (r *InstanceReconciler) bootstrapStandby(ctx context.Context, inst *v1alpha1.Instance) error {
	req := &controllers.BootstrapStandbyRequest{
		CdbName:  inst.Spec.CDBName,
		Version:  inst.Spec.Version,
		Dbdomain: controllers.GetDBDomain(inst),
	}
	migratedPDBs, err := controllers.BootstrapStandby(ctx, r, r.DatabaseClientFactory, inst.GetNamespace(), inst.GetName(), *req)
	if err != nil {
		return fmt.Errorf("failed to bootstrap the standby instance: %v", err)
	}

	// Create missing resources for migrated database.
	for _, pdb := range migratedPDBs {
		var users []v1alpha1.UserSpec
		for _, u := range pdb.Users {
			var privs []v1alpha1.PrivilegeSpec
			for _, p := range u.Privs {
				privs = append(privs, v1alpha1.PrivilegeSpec(p))
			}
			users = append(users, v1alpha1.UserSpec{
				UserSpec: commonv1alpha1.UserSpec{
					Name: u.UserName,
				},
				Privileges: privs,
			})
		}
		database := &v1alpha1.Database{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: inst.GetNamespace(),
				Name:      pdb.PdbName,
			},
			Spec: v1alpha1.DatabaseSpec{
				DatabaseSpec: commonv1alpha1.DatabaseSpec{
					Name:     pdb.PdbName,
					Instance: inst.GetName(),
				},
				Users: users,
			},
		}

		err := r.Client.Create(ctx, database)
		if apierrors.IsAlreadyExists(err) {
			if err := r.Client.Patch(ctx, database, client.Apply); err != nil {
				return fmt.Errorf("bootstrapStandby failed to patch database resource: %v", err)
			}
		} else if err != nil {
			return fmt.Errorf("bootstrapStandby failed to create database resource: %v", err)
		}
	}
	return nil
}

func (r *InstanceReconciler) updateStandbyDataReplicationStatus(ctx context.Context, inst *v1alpha1.Instance, cs metav1.ConditionStatus, nextState, msg string, errMsgs ...string) {
	if len(errMsgs) > 0 {
		sort.Strings(errMsgs)
		// TODO better message format.
		msg = fmt.Sprintf("%s\n%s", msg, strings.Join(errMsgs, "\n"))
	}
	k8s.InstanceUpsertCondition(
		&inst.Status,
		k8s.StandbyDRReady,
		cs,
		nextState,
		msg)
}

func (r *InstanceReconciler) reconcilePromoteStandby(ctx context.Context, inst *v1alpha1.Instance, log logr.Logger) error {
	if inst.Status.CurrentReplicationSettings == nil {
		log.Info("reconcilePromoteStandby: skipping as promote standby completed.")
		return nil
	}
	standbyHost, err := r.getStandbyHost(ctx, inst)
	if err != nil {
		return err
	}

	credentialReq, err := toCredentialReq(inst.Status.CurrentReplicationSettings.PrimaryUser)
	if err != nil {
		return err
	}

	if err := controllers.PromoteStandby(ctx, r, r.DatabaseClientFactory, inst.Namespace, inst.Name, controllers.PromoteStandbyRequest{
		PrimaryHost:         inst.Status.CurrentReplicationSettings.PrimaryHost,
		PrimaryPort:         inst.Status.CurrentReplicationSettings.PrimaryPort,
		PrimaryService:      inst.Status.CurrentReplicationSettings.PrimaryServiceName,
		PrimaryUser:         inst.Status.CurrentReplicationSettings.PrimaryUser.Name,
		PrimaryCredential:   credentialReq,
		StandbyDbUniqueName: inst.Spec.DBUniqueName,
		StandbyHost:         standbyHost,
	}); err != nil {
		return err
	}
	return nil
}

func (r *InstanceReconciler) getStandbyHost(ctx context.Context, inst *v1alpha1.Instance) (string, error) {
	lbSvcName := fmt.Sprintf(controllers.SvcName, inst.Name)
	lbSvc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Name: lbSvcName, Namespace: inst.Namespace}, lbSvc); err != nil {
		return "", err
	}
	standbyHost := ""
	if len(lbSvc.Status.LoadBalancer.Ingress) > 0 {
		standbyHost = lbSvc.Status.LoadBalancer.Ingress[0].Hostname
		if standbyHost == "" {
			standbyHost = lbSvc.Status.LoadBalancer.Ingress[0].IP
		}
	}
	if standbyHost == "" {
		return "", fmt.Errorf("load balancer service %v not ready", lbSvc)
	}
	return standbyHost, nil
}

func internalErrToMsg(err error) string {
	// with this helper method, we can decide how to show code internal errors in status
	return fmt.Sprintf("Internal error: %v .", err)
}

func toCredentialReq(userSpec commonv1alpha1.UserSpec) (*controllers.Credential, error) {
	if userSpec.GsmSecretRef != nil {
		return &controllers.Credential{
			Source: &controllers.CredentialGsmSecretReference{GsmSecretReference: &controllers.GsmSecretReference{
				ProjectId: userSpec.GsmSecretRef.ProjectId,
				SecretId:  userSpec.GsmSecretRef.SecretId,
				Version:   userSpec.GsmSecretRef.Version,
			}},
		}, nil
	}
	return nil, errors.New("failed to find a valid credential spec")
}

func findLogDiskSize(inst *v1alpha1.Instance) int64 {
	diskName := "LogDisk"
	if inst.Spec.Disks != nil {
		for _, d := range inst.Spec.Disks {
			if d.Name == diskName && !d.Size.IsZero() {
				return d.Size.Value()
			}
		}
	}
	defaultLogDiskSpec, _ := controllers.DefaultDiskSpecs[diskName]
	return defaultLogDiskSpec.Size.Value()
}

func replicationSettingsNilErr(settings *v1alpha1.ReplicationSettings) string {
	var s string
	if b, err := json.Marshal(settings); err == nil {
		s = string(b)
	} else {
		s = fmt.Sprintf("%+v", settings)
	}
	return fmt.Sprintf("spec.replicationSettings must be specified for a standby instance before promotion ready state. "+
		"Try adding back spec.replicationSettings to the instance Kubernetes manifest. "+
		"Last known replicationSettings: %s", s)
}
