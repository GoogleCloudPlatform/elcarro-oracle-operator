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
	"strings"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	maintenance "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/pkg/maintenance"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	capb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

// reservedParameters holds the list of parameters that aren't allowed for modification.
var reservedParameters = map[string]bool{
	"audit_file_dest":            true,
	"audit_trail":                true,
	"compatible":                 true,
	"control_files":              true,
	"db_block_size":              true,
	"db_recovery_file_dest":      true,
	"db_recovery_file_dest_size": true,
	"diagnostic_dest":            true,
	"dispatchers":                true,
	"enable_pluggable_database":  true,
	"filesystemio_options":       true,
	"local_listener":             true,
	"open_cursors":               true,
	"pga_aggregate_target":       true,
	"processes":                  true,
	"remote_login_passwordfile":  true,
	"sga_target":                 true,
	"undo_tablespace":            true,
	"log_archive_dest_1":         true,
	"log_archive_dest_state_1":   true,
	"log_archive_format":         true,
	"standby_file_management":    true,
}

func (r *InstanceReconciler) recordEventAndUpdateStatus(ctx context.Context, inst *v1alpha1.Instance, conditionStatus v1.ConditionStatus, reason, msg string, log logr.Logger) {
	if conditionStatus == v1.ConditionTrue {
		r.Recorder.Eventf(inst, corev1.EventTypeNormal, reason, msg)
	} else {
		r.Recorder.Eventf(inst, corev1.EventTypeWarning, reason, msg)
	}
	k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, conditionStatus, reason, msg)
	if err := r.Status().Update(ctx, inst); err != nil {
		log.Error(err, "failed to update the instance status")
	}
}

// fetchCurrentParameterState infers the type and current value of the
// parameters by querying the database and is used for the following purpose,
// * The parameter type (static or dynamic) will be used for deciding whether
//   a database restart is required.
// * The current parameter value will be used for rollback if the parameter
//   update fails or the database is non-functional after the restart.
func fetchCurrentParameterState(ctx context.Context, caClient capb.ConfigAgentClient, spec v1alpha1.InstanceSpec) (map[string]string, map[string]string, error) {

	var unacceptableParams []string
	var keys []string
	for k := range spec.Parameters {
		if _, ok := reservedParameters[k]; ok {
			unacceptableParams = append(unacceptableParams, k)
		}
		keys = append(keys, k)
	}

	if len(unacceptableParams) != 0 {
		return nil, nil, fmt.Errorf("fetchCurrentParameterState: parameter list contains reserved parameters:%v", unacceptableParams)
	}
	staticParams := make(map[string]string)
	dynamicParams := make(map[string]string)
	response, err := caClient.GetParameterTypeValue(ctx, &capb.GetParameterTypeValueRequest{
		Keys: keys,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("fetchCurrentParameterState: error while querying parameter type:%v", err)
	}

	// Check if static parameters are specified and restart is required.
	restartRequired := false
	paramType := response.GetTypes()
	paramValues := response.GetValues()
	for i := 0; i < len(paramType); i++ {
		if paramType[i] == "FALSE" {
			restartRequired = restartRequired || paramType[i] == "FALSE"
			staticParams[keys[i]] = paramValues[i]
		} else {
			dynamicParams[keys[i]] = paramValues[i]
		}
	}

	// If restart is required, check if the restartTimeRange is specified in the config.
	if restartRequired && !maintenance.HasValidTimeRanges(spec.MaintenanceWindow) {
		return nil, nil, errors.New("maintenanceWindow for db downtime not specified for static parameter update")
	}

	currentTime := time.Now()
	inMaintenanceWindow := maintenance.InRange(spec.MaintenanceWindow, currentTime)

	if !inMaintenanceWindow {
		return nil, nil, errors.New("current time is not in a maintenance window that allows db restarts")
	}
	return staticParams, dynamicParams, nil
}

func (r *InstanceReconciler) setParameters(ctx context.Context, inst v1alpha1.Instance, caClient capb.ConfigAgentClient, log logr.Logger) (bool, error) {
	log.Info("Parameters are ", "parameters:", inst.Spec.Parameters)
	requireDatabaseRestart := false
	var keys []string

	for k, v := range inst.Spec.Parameters {
		isStatic, err := controllers.SetParameter(ctx, r.DatabaseClientFactory, r.Client, inst.Namespace, inst.Name, k, v)
		if err != nil {
			log.Error(err, "setParameters: error while running SetParameter query")
			return requireDatabaseRestart, err
		}
		keys = append(keys, k)
		requireDatabaseRestart = requireDatabaseRestart || isStatic
		log.Info("setParameters: requireDatabaseRestart", "requireDatabaseRestart", requireDatabaseRestart)
	}

	response, err := caClient.GetParameterTypeValue(ctx, &capb.GetParameterTypeValueRequest{
		Keys: keys,
	})
	if err != nil {
		log.Error(err, "setParameters: error while running GetParameterTypeValue query")
		return false, err
	}

	paramValues := response.GetValues()
	for i := 0; i < len(keys); i++ {
		if inst.Spec.Parameters[keys[i]] != paramValues[i] &&
			// For certain parameter types Oracle converts them to uppercase before storing
			// For eg boolean (true/false) units(char/byte)
			strings.ToUpper(inst.Spec.Parameters[keys[i]]) != paramValues[i] {
			msg := fmt.Sprintf("setParameters: parameter update for %s with value %s was rejected by database and instead set to %s", keys[i], inst.Spec.Parameters[keys[i]], paramValues[i])
			log.Error(err, msg)
			return false, errors.New(msg)
		}
	}

	log.Info("setParameters: SQL commands executed successfully")
	return requireDatabaseRestart, nil
}

// setInstanceParameterStateMachine guides the transition of parameter update
// workflow to the next possible state based on the current state and the outcome
// of the task associated with the current state.
func (r *InstanceReconciler) setInstanceParameterStateMachine(ctx context.Context, req ctrl.Request, inst v1alpha1.Instance, log logr.Logger) (ctrl.Result, error) {

	// If the current parameter state is equal to the requested state skip the update
	if eq := reflect.DeepEqual(inst.Spec.Parameters, inst.Status.CurrentParameters); eq {
		return ctrl.Result{}, nil
	}

	// If the last failed parameter update is equal to the requested state skip it.
	if eq := reflect.DeepEqual(inst.Spec.Parameters, inst.Status.LastFailedParameterUpdate); eq {
		return ctrl.Result{}, nil
	}

	if result, err := r.sanityCheckTimeRange(inst, log); err != nil {
		return result, err
	}
	conn, caClient, err := r.getConfigAgentClient(ctx, req, inst, log)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer conn.Close()

	_, dynamicParamsRollbackState, err := fetchCurrentParameterState(ctx, caClient, inst.Spec)
	if err != nil {
		msg := "setInstanceParameterStateMachine: Sanity check failed for instance parameters"
		r.recordEventAndUpdateStatus(ctx, &inst, v1.ConditionFalse, k8s.ParameterUpdateRollback, fmt.Sprintf("%s: %v", msg, err), log)
		return ctrl.Result{}, err
	}

	log.Info("setInstanceParameterStateMachine: entering state machine")
	for true {
		instanceReadyCond := k8s.FindCondition(inst.Status.Conditions, k8s.Ready)
		switch instanceReadyCond.Reason {
		case k8s.CreateComplete:
			msg := "setInstanceParameterStateMachine: parameter update in progress"
			r.recordEventAndUpdateStatus(ctx, &inst, v1.ConditionFalse, k8s.ParameterUpdateInProgress, msg, log)
			log.Info("setInstanceParameterStateMachine: SM CreateComplete -> ParameterUpdateInProgress")
		case k8s.ParameterUpdateInProgress:
			restartRequired, err := r.setParameters(ctx, inst, caClient, log)
			if err != nil {
				msg := "setInstanceParameterStateMachine: Error while setting instance parameters"
				r.recordEventAndUpdateStatus(ctx, &inst, v1.ConditionFalse, k8s.ParameterUpdateRollback, fmt.Sprintf("%s: %v", msg, err), log)
				log.Info("setInstanceParameterStateMachine: SM ParameterUpdateInProgress -> ParameterUpdateRollback")
				break
			}
			if restartRequired {
				log.Info("setInstanceParameterStateMachine: static parameter specified in config, scheduling restart to activate them")
				if err := controllers.BounceDatabase(ctx, r, r.DatabaseClientFactory, inst.Namespace, inst.Name, controllers.BounceDatabaseRequest{
					Sid: inst.Spec.CDBName,
				}); err != nil {
					msg := "setInstanceParameterStateMachine: error while restarting database after setting static parameters"
					r.recordEventAndUpdateStatus(ctx, &inst, v1.ConditionFalse, k8s.ParameterUpdateRollback, fmt.Sprintf("%s: %v", msg, err), log)
					log.Info("setInstanceParameterStateMachine: SM ParameterUpdateInProgress -> ParameterUpdateRollback")
					break
				}
			}
			msg := "setInstanceParameterStateMachine: Parameter update successful"
			inst.Status.CurrentParameters = inst.Spec.Parameters
			r.recordEventAndUpdateStatus(ctx, &inst, v1.ConditionTrue, k8s.CreateComplete, msg, log)
			log.Info("setInstanceParameterStateMachine: SM ParameterUpdateInProgress -> CreateComplete")
			return ctrl.Result{}, nil
		case k8s.ParameterUpdateRollback:
			if err := r.initiateRecovery(ctx, inst, caClient, dynamicParamsRollbackState, log); err != nil {
				log.Info("setInstanceParameterStateMachine: recovery failed, instance currently in irrecoverable state", "err", err)
				return ctrl.Result{}, err
			}
			inst.Status.LastFailedParameterUpdate = inst.Spec.Parameters
			msg := "setInstanceParameterStateMachine: instance recovered after bad parameter update"
			r.recordEventAndUpdateStatus(ctx, &inst, v1.ConditionTrue, k8s.CreateComplete, msg, log)
			log.Info("setInstanceParameterStateMachine: SM ParameterUpdateRollback -> CreateComplete")
			return ctrl.Result{}, nil
		}
	}
	return ctrl.Result{}, nil
}

func (r *InstanceReconciler) getConfigAgentClient(ctx context.Context, req ctrl.Request, inst v1alpha1.Instance, log logr.Logger) (*grpc.ClientConn, capb.ConfigAgentClient, error) {
	agentSvc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Name: fmt.Sprintf(controllers.AgentSvcName, inst.Name), Namespace: req.Namespace}, agentSvc); err != nil {
		return nil, nil, err
	}

	conn, err := grpc.Dial(fmt.Sprintf("%s:%d", agentSvc.Spec.ClusterIP, consts.DefaultConfigAgentPort), grpc.WithInsecure())
	if err != nil {
		// We'll retry the reconcile if its due to transient connection errors
		log.Error(err, "setInstanceParameterStateMachine: failed to create a conn via gRPC.Dial")
		return nil, nil, err
	}
	caClient := capb.NewConfigAgentClient(conn)
	return conn, caClient, nil
}

// initiateRecovery will recover the config file (which contains the static
// parameters) to the last known working copy if the static
// parameter update failed (which caused the database to be non-functional
// after a restart).
func (r *InstanceReconciler) initiateRecovery(ctx context.Context, inst v1alpha1.Instance, caClient capb.ConfigAgentClient, dynamicParams map[string]string, log logr.Logger) error {

	log.Info("initiateRecovery: initiating recovery of config file")
	if err := controllers.RecoverConfigFile(ctx, r.DatabaseClientFactory, r.Client, inst.Namespace, inst.Name, inst.Spec.CDBName); err != nil {
		msg := "initiateRecovery: error while recovering config file"
		log.Info(msg, "err", err)
		return err
	}

	if err := controllers.BounceDatabase(ctx, r, r.DatabaseClientFactory, inst.Namespace, inst.Name, controllers.BounceDatabaseRequest{
		Sid: inst.Spec.CDBName,
	}); err != nil {
		return err
	}
	log.Info("initiateRecovery: database bounced completed successfully")

	// Rollback all the dynamic parameter updates after the database has recovered
	for k, v := range dynamicParams {
		_, err := controllers.SetParameter(ctx, r.DatabaseClientFactory, r.Client, inst.Namespace, inst.Name, k, v)
		if err != nil {
			log.Error(err, "initiateRecovery: error while rolling back dynamic parameters")
			return err
		}
	}
	log.Info("initiateRecovery: rolling back of dynamic parameters completed successfully", "dynamicParams", dynamicParams)
	return nil
}

func (r *InstanceReconciler) sanityCheckTimeRange(inst v1alpha1.Instance, log logr.Logger) (ctrl.Result, error) {
	if !maintenance.HasValidTimeRanges(inst.Spec.MaintenanceWindow) {
		return ctrl.Result{}, fmt.Errorf("MaintenanceWindow specification is not valid: %+v", inst.Spec.MaintenanceWindow)
	}

	now := time.Now()

	if maintenance.InRange(inst.Spec.MaintenanceWindow, now) {
		return ctrl.Result{}, nil
	}

	nextStart, _, err := maintenance.NextWindow(inst.Spec.MaintenanceWindow, now)

	// If there is no future maintenance windows (next window), return an error.
	if err != nil {
		return ctrl.Result{}, errors.New("current time is past the maintenance time range")
	}

	// Otherwise: requeue for processing when the maintenance window opens up.
	restartWaitTime := nextStart.Sub(now)
	log.Info("setInstanceParameterStateMachine: Wait time before restart ", "restartWaitTime", restartWaitTime.Seconds())
	return ctrl.Result{RequeueAfter: restartWaitTime}, errors.New("current time is not within the maintenance time range")
}

func mapsToStringArray(parameterMap map[string]string) []string {
	var parameters []string
	for k, v := range parameterMap {
		parameters = append(parameters, fmt.Sprintf("%s=%s", k, v))
	}
	return parameters
}
