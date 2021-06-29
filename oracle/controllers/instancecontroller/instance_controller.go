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
	"reflect"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	commonutils "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/pkg/utils"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/databasecontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common/sql"
	capb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

var CheckStatusInstanceFunc = controllers.CheckStatusInstanceFunc

// InstanceReconciler reconciles an Instance object.
type InstanceReconciler struct {
	client.Client
	Log           logr.Logger
	Scheme        *runtime.Scheme
	Images        map[string]string
	ClientFactory controllers.ConfigAgentClientFactory
	Recorder      record.EventRecorder
}

// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=instances/status,verbs=get;update;patch

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=list;watch;get;patch;create
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;create;update

// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=databases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=databases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=configs,verbs=get;list;watch;create;update;patch;delete

const (
	physicalRestore               = "PhysicalRestore"
	instanceProvisionTimeout      = 30 * time.Minute
	createDatabaseInstanceTimeout = 30 * time.Minute // 30 minutes because it can take 20+ minutes for unseeded CDB creations
	dateFormat                    = "20060102"
)

var defaultDisks = []commonv1alpha1.DiskSpec{
	{
		Name: "DataDisk",
		Size: resource.MustParse("100Gi"),
	},
	{
		Name: "LogDisk",
		Size: resource.MustParse("150Gi"),
	},
}

// loadConfig attempts to find a customer specific Operator config
// if it's been provided. There should be at most one config.
// If no config is provided by a customer, no errors are raised and
// all defaults are assumed.
func (r *InstanceReconciler) loadConfig(ctx context.Context, ns string) (*v1alpha1.Config, error) {
	var configs v1alpha1.ConfigList
	if err := r.List(ctx, &configs, client.InNamespace(ns)); err != nil {
		return nil, err
	}

	if len(configs.Items) == 0 {
		return nil, nil
	}

	if len(configs.Items) != 1 {
		return nil, fmt.Errorf("number of customer provided configs is not one: %d", len(configs.Items))
	}

	return &configs.Items[0], nil
}

// statusProgress tracks the progress of an ongoing instance creation and returns the progress in terms of percentage.
func (r *InstanceReconciler) statusProgress(ctx context.Context, ns, name string) (int, error) {
	var sts appsv1.StatefulSetList
	if err := r.List(ctx, &sts, client.InNamespace(ns)); err != nil {
		r.Log.Error(err, "failed to get a list of StatefulSets to check status")
		return 0, err
	}

	if len(sts.Items) < 1 {
		return 0, fmt.Errorf("failed to find a StatefulSet, found: %d", len(sts.Items))
	}

	// In theory a user should not be running any StatefulSet in a
	// namespace, but to be on a safe side, iterate over all until we find ours.
	var foundSts *appsv1.StatefulSet
	for index, s := range sts.Items {
		if s.Name == name {
			foundSts = &sts.Items[index]
		}
	}

	if foundSts == nil {
		return 0, fmt.Errorf("failed to find the right StatefulSet %s (out of %d)", name, len(sts.Items))
	}
	r.Log.V(1).Info("found the right StatefulSet", "foundSts", &foundSts.Name,
		"sts.Status.CurrentReplicas", &foundSts.Status.CurrentReplicas, "sts.Status.ReadyReplicas", foundSts.Status.ReadyReplicas)

	if foundSts.Status.CurrentReplicas != 1 {
		return 10, fmt.Errorf("StatefulSet is not ready yet? (failed to find the expected number of current replicas): %d", foundSts.Status.CurrentReplicas)
	}

	if foundSts.Status.ReadyReplicas != 1 {
		return 50, fmt.Errorf("StatefulSet is not ready yet? (failed to find the expected number of ready replicas): %d", foundSts.Status.ReadyReplicas)
	}

	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{"statefulset": name}); err != nil {
		r.Log.Error(err, "failed to get a list of Pods to check status")
		return 60, err
	}

	if len(pods.Items) < 1 {
		return 65, fmt.Errorf("failed to find enough pods, found: %d pods", len(pods.Items))
	}

	var foundPod *corev1.Pod
	for index, p := range pods.Items {
		if p.Name == name+"-0" {
			foundPod = &pods.Items[index]
		}
	}

	if foundPod == nil {
		return 75, fmt.Errorf("failed to find the right Pod %s (out of %d)", name+"-0", len(pods.Items))
	}
	r.Log.V(1).Info("found the right Pod", "pod.Name", &foundPod.Name, "pod.Status", foundPod.Status.Phase, "#containers", len(foundPod.Status.ContainerStatuses))

	if foundPod.Status.Phase != "Running" {
		return 85, fmt.Errorf("failed to find the right Pod %s in status Running: %s", name+"-0", foundPod.Status.Phase)
	}

	for _, c := range foundPod.Status.ContainerStatuses {
		if c.Name == databasecontroller.DatabaseContainerName && c.Ready {
			return 100, nil
		}
	}
	return 85, fmt.Errorf("failed to find a database container in %+v", foundPod.Status.ContainerStatuses)
}

func (r *InstanceReconciler) updateProgressCondition(ctx context.Context, inst v1alpha1.Instance, ns, op string) bool {
	iReadyCond := k8s.FindCondition(inst.Status.Conditions, k8s.Ready)

	r.Log.Info("updateProgressCondition", "operation", op, "iReadyCond", iReadyCond)
	progress, err := r.statusProgress(ctx, ns, fmt.Sprintf(controllers.StsName, inst.Name))
	if err != nil && iReadyCond != nil {
		if progress > 0 {
			k8s.InstanceUpsertCondition(&inst.Status, iReadyCond.Type, iReadyCond.Status, iReadyCond.Reason, fmt.Sprintf("%s: %d%%", op, progress))
		}
		r.Log.Info("updateProgressCondition", "statusProgress", err)
		return false
	}
	return true
}

// validateSpec sanity checks a DB Domain input for conflicts.
func validateSpec(inst *v1alpha1.Instance) error {
	// Does DBUniqueName contain DB Domain as a suffix?
	if strings.Contains(inst.Spec.DBUniqueName, ".") {
		domainFromName := strings.SplitN(inst.Spec.DBUniqueName, ".", 2)[1]
		if inst.Spec.DBDomain != "" && domainFromName != inst.Spec.DBDomain {
			return fmt.Errorf("validateSpec: domain %q provided in DBUniqueName %q does not match with provided DBDomain %q",
				domainFromName, inst.Spec.DBUniqueName, inst.Spec.DBDomain)
		}
	}

	if inst.Spec.CDBName != "" {
		if _, err := sql.Identifier(inst.Spec.CDBName); err != nil {
			return fmt.Errorf("validateSpec: cdbName is not valid: %w", err)
		}
	}

	return nil
}

// updateIsChangeApplied sets instance.Status.IsChangeApplied field to false if observedGeneration < generation, it sets it to true if changes are applied.
// TODO: add logic to handle restore/recovery
func (r *InstanceReconciler) updateIsChangeApplied(ctx context.Context, inst *v1alpha1.Instance) {
	if inst.Status.ObservedGeneration < inst.Generation {
		inst.Status.IsChangeApplied = v1.ConditionFalse
		inst.Status.ObservedGeneration = inst.Generation
		r.Log.Info("change detected", "observedGeneration", inst.Status.ObservedGeneration, "generation", inst.Generation)
	}
	if inst.Status.IsChangeApplied == v1.ConditionTrue {
		return
	}
	parameterUpdateDone := inst.Spec.Parameters == nil || reflect.DeepEqual(inst.Status.CurrentParameters, inst.Spec.Parameters)
	if parameterUpdateDone {
		inst.Status.IsChangeApplied = v1.ConditionTrue
	}
	r.Log.Info("change applied", "observedGeneration", inst.Status.ObservedGeneration, "generation", inst.Generation)
}

func (r *InstanceReconciler) Reconcile(_ context.Context, req ctrl.Request) (_ ctrl.Result, respErr error) {
	ctx := context.Background()
	log := r.Log.WithValues("Instance", req.NamespacedName)

	log.Info("reconciling instance")

	var inst v1alpha1.Instance
	if err := r.Get(ctx, req.NamespacedName, &inst); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := validateSpec(&inst); err != nil {
		log.Error(err, "instance spec validation failed")
		// TODO better error handling, no need retry
		return ctrl.Result{}, nil
	}

	defer func() {
		r.updateIsChangeApplied(ctx, &inst)
		if err := r.Status().Update(ctx, &inst); err != nil {
			log.Error(err, "failed to update the instance status")
			if respErr == nil {
				respErr = err
			}
		}
	}()

	diskSpace, err := commonutils.DiskSpaceTotal(&inst)
	if err != nil {
		log.Error(err, "failed to calculate the total disk space")
	}
	log.Info("common instance", "total allocated disk space across all instance disks [Gi]", diskSpace/1024/1024/1024)

	instanceReadyCond := k8s.FindCondition(inst.Status.Conditions, k8s.Ready)
	dbInstanceCond := k8s.FindCondition(inst.Status.Conditions, k8s.DatabaseInstanceReady)

	var enabledServices []commonv1alpha1.Service
	for service, enabled := range inst.Spec.Services {
		if enabled {
			enabledServices = append(enabledServices, service)
		}
	}

	// If the instance and database is ready, we can set the instance parameters
	if k8s.ConditionStatusEquals(instanceReadyCond, v1.ConditionTrue) &&
		k8s.ConditionStatusEquals(dbInstanceCond, v1.ConditionTrue) && inst.Spec.Parameters != nil {
		log.Info("instance and db is ready, setting instance parameters")

		if result, err := r.setInstanceParameterStateMachine(ctx, req, inst, log); err != nil {
			return result, err
		}
	}

	iReadyCond := k8s.FindCondition(inst.Status.Conditions, k8s.Ready)
	dbiCond := k8s.FindCondition(inst.Status.Conditions, k8s.DatabaseInstanceReady)

	// Load default preferences (aka "config") if provided by a customer.
	config, err := r.loadConfig(ctx, req.NamespacedName.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	images := make(map[string]string)
	for k, v := range r.Images {
		images[k] = v
	}

	result, err := r.overrideDefaultImages(config, images, &inst, log)
	if err != nil {
		return result, err
	}

	services := []string{"lb", "node"}

	applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("instance-controller")}

	cm, err := controllers.NewConfigMap(&inst, r.Scheme, fmt.Sprintf(controllers.CmName, inst.Name))
	if err != nil {
		log.Error(err, "failed to create a ConfigMap", "cm", cm)
		return ctrl.Result{}, err
	}

	if err := r.Patch(ctx, cm, client.Apply, applyOpts...); err != nil {
		return ctrl.Result{}, err
	}

	// Create a StatefulSet if needed.
	sp := controllers.StsParams{
		Inst:           &inst,
		Scheme:         r.Scheme,
		Namespace:      req.NamespacedName.Namespace,
		Images:         images,
		SvcName:        fmt.Sprintf(controllers.SvcName, inst.Name),
		StsName:        fmt.Sprintf(controllers.StsName, inst.Name),
		PrivEscalation: false,
		ConfigMap:      cm,
		Disks:          diskSpecs(&inst, config),
		Config:         config,
		Log:            log,
		Services:       enabledServices,
	}

	// If there is a Restore section in the spec the reconciliation will be handled
	// by restore state machine until the Spec.Restore section is removed again.
	if inst.Spec.Restore != nil {
		// Ask the restore state machine to reconcile
		result, err := r.restoreStateMachine(req, instanceReadyCond, dbInstanceCond, &inst, ctx, sp)
		if err != nil {
			log.Error(err, "restoreStateMachine failed")
			return result, err
		}
		if !result.IsZero() {
			return result, err
		}
		// No error and no result - state machine is done, proceed with main reconciler
	}

	if k8s.ConditionStatusEquals(instanceReadyCond, v1.ConditionTrue) && k8s.ConditionStatusEquals(dbInstanceCond, v1.ConditionTrue) {
		log.Info("instance has already been provisioned and ready")
		return ctrl.Result{}, nil
	}

	newPVCs, err := controllers.NewPVCs(sp)
	if err != nil {
		r.Log.Error(err, "NewPVCs failed")
		return ctrl.Result{}, err
	}
	newPodTemplate := controllers.NewPodTemplate(sp, inst.Spec.CDBName, controllers.GetDBDomain(&inst))
	sts, err := controllers.NewSts(sp, newPVCs, newPodTemplate)
	if err != nil {
		log.Error(err, "failed to create a StatefulSet", "sts", sts)
		return ctrl.Result{}, err
	}
	log.Info("StatefulSet constructed", "sts", sts, "sts.Status", sts.Status, "inst.Status", inst.Status)

	if err := r.Patch(ctx, sts, client.Apply, applyOpts...); err != nil {
		log.Error(err, "failed to patch the StatefulSet", "sts.Status", sts.Status)
		return ctrl.Result{}, err
	}

	agentParam := controllers.AgentDeploymentParams{
		Inst:           &inst,
		Config:         config,
		Scheme:         r.Scheme,
		Name:           fmt.Sprintf(controllers.AgentDeploymentName, inst.Name),
		Images:         images,
		PrivEscalation: false,
		Log:            log,
		Args:           controllers.GetLogLevelArgs(config),
		Services:       enabledServices,
	}
	agentDeployment, err := controllers.NewAgentDeployment(agentParam)
	if err != nil {
		log.Error(err, "failed to create a Deployment", "agent deployment", agentDeployment)
		return ctrl.Result{}, err
	}
	if err := r.Patch(ctx, agentDeployment, client.Apply, applyOpts...); err != nil {
		log.Error(err, "failed to patch the Deployment", "agent deployment.Status", agentDeployment.Status)
		return ctrl.Result{}, err
	}

	// Create LB/NodePort Services if needed.
	var svcLB *corev1.Service
	for _, s := range services {
		svc, err := controllers.NewSvc(&inst, r.Scheme, s)
		if err != nil {
			return ctrl.Result{}, err
		}

		if err := r.Patch(ctx, svc, client.Apply, applyOpts...); err != nil {
			return ctrl.Result{}, err
		}

		if s == "lb" {
			svcLB = svc
		}
	}

	svc, err := controllers.NewDBDaemonSvc(&inst, r.Scheme)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.Patch(ctx, svc, client.Apply, applyOpts...); err != nil {
		return ctrl.Result{}, err
	}

	svc, err = controllers.NewAgentSvc(&inst, r.Scheme)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.Patch(ctx, svc, client.Apply, applyOpts...); err != nil {
		return ctrl.Result{}, err
	}

	if iReadyCond == nil {
		iReadyCond = k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.CreateInProgress, "")
	}

	inst.Status.Endpoint = fmt.Sprintf(controllers.SvcEndpoint, fmt.Sprintf(controllers.SvcName, inst.Name), inst.Namespace)
	inst.Status.URL = controllers.SvcURL(svcLB, consts.SecureListenerPort)

	// RequeueAfter 30 seconds to avoid constantly reconcile errors before statefulSet is ready.
	// Update status when the Service is ready (for the initial provisioning).
	// Also confirm that the StatefulSet is up and running.
	if k8s.ConditionReasonEquals(iReadyCond, k8s.CreateInProgress) {
		elapsed := k8s.ElapsedTimeFromLastTransitionTime(iReadyCond, time.Second)
		if elapsed > instanceProvisionTimeout {
			r.Recorder.Eventf(&inst, corev1.EventTypeWarning, "InstanceReady", fmt.Sprintf("Instance provision timed out after %v", instanceProvisionTimeout))
			msg := fmt.Sprintf("Instance provision timed out. Elapsed Time: %v", elapsed)
			log.Info(msg)
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.CreateInProgress, msg)
			return ctrl.Result{}, nil
		}

		if !r.updateProgressCondition(ctx, inst, req.NamespacedName.Namespace, controllers.CreateInProgress) {
			log.Info("requeue after 30 seconds")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		if inst.Status.URL != "" {
			if !k8s.ConditionReasonEquals(iReadyCond, k8s.CreateComplete) {
				r.Recorder.Eventf(&inst, corev1.EventTypeNormal, "InstanceReady", "Instance has been created successfully. Elapsed Time: %v", elapsed)
			}
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionTrue, k8s.CreateComplete, "")
			inst.Status.CurrentServiceImage = images["service"]
			return ctrl.Result{}, nil
		}
	}

	if inst.Labels == nil {
		inst.Labels = map[string]string{"instance": inst.Name}
		if err := r.Update(ctx, &inst); err != nil {
			log.Error(err, "failed to update the Instance spec (set labels)")
			return ctrl.Result{}, err
		}
	}

	// reach here, the instance should be ready.
	if inst.Spec.Mode == commonv1alpha1.ManuallySetUpStandby {
		log.Info("reconciling instance for manually set up standby: DONE")
		// the code will return here, so we can rely on defer function to update database status.
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.ManuallySetUpStandbyInProgress, fmt.Sprintf("Setting up standby database in progress, remove spec.mode %v to promote the instance", inst.Spec.Mode))
		k8s.InstanceUpsertCondition(&inst.Status, k8s.StandbyReady, v1.ConditionTrue, k8s.CreateComplete, fmt.Sprintf("standby instance creation complete, ready to set up standby database in the instance"))
		return ctrl.Result{}, nil
	}

	if k8s.ConditionStatusEquals(k8s.FindCondition(inst.Status.Conditions, k8s.StandbyReady), v1.ConditionTrue) {
		conn, err := grpc.Dial(fmt.Sprintf("%s:%d", svc.Spec.ClusterIP, consts.DefaultConfigAgentPort), grpc.WithInsecure())
		if err != nil {
			log.Error(err, "failed to create a conn via gRPC.Dial")
			return ctrl.Result{}, err
		}
		defer conn.Close()
		caClient := capb.NewConfigAgentClient(conn)
		// promote the standby instance, bootstrap is part of promotion.
		r.Recorder.Eventf(&inst, corev1.EventTypeNormal, k8s.PromoteStandbyInProgress, "")
		if err := r.bootstrapStandby(ctx, &inst, caClient, log); err != nil {
			r.Recorder.Eventf(&inst, corev1.EventTypeWarning, k8s.PromoteStandbyFailed, fmt.Sprintf("Error promoting standby: %v", err))
			return ctrl.Result{}, err
		}
		// the standby instance has been successfully promoted, set ready condition
		// to true and standby ready to false. Promotion need to be idempotent to
		// ensure the correctness under retry.
		r.Recorder.Eventf(&inst, corev1.EventTypeNormal, k8s.PromoteStandbyComplete, "")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionTrue, k8s.CreateComplete, "")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.StandbyReady, v1.ConditionFalse, k8s.PromoteStandbyComplete, "")
		return ctrl.Result{Requeue: true}, err
	}

	var dbs v1alpha1.DatabaseList
	if err := r.List(ctx, &dbs, client.InNamespace(req.Namespace)); err != nil {
		log.V(1).Info("failed to list databases for instance", "inst.Name", inst.Name)
	} else {
		log.Info("list of queried databases", "dbs", dbs)
	}

	for _, newDB := range dbs.Items {
		// check DB name against existing ones to decide whether this is a new DB
		if !controllers.Contains(inst.Status.DatabaseNames, newDB.Spec.Name) {
			log.Info("found a new DB", "dbName", newDB.Spec.Name)
			inst.Status.DatabaseNames = append(inst.Status.DatabaseNames, newDB.Spec.Name)
		} else {
			log.V(1).Info("not a new DB, skipping the update", "dbName", newDB.Spec.Name)
		}
	}

	log.Info("instance status", "iReadyCond", iReadyCond, "endpoint", inst.Status.Endpoint,
		"url", inst.Status.URL, "databases", inst.Status.DatabaseNames)

	log.Info("reconciling instance: DONE")

	istatus, err := controllers.CheckStatusInstanceFunc(ctx, inst.Name, inst.Spec.CDBName, svc.Spec.ClusterIP, controllers.GetDBDomain(&inst), log)
	if err != nil {
		log.Error(err, "failed to check the database instance status")
		return ctrl.Result{}, err
	}

	isImageSeeded, err := isImageSeeded(ctx, svc.Spec.ClusterIP, log)
	if err != nil {
		log.Error(err, "unable to determine image type")
		return ctrl.Result{}, err
	}

	if !isImageSeeded && istatus == controllers.StatusInProgress {
		if inst.Spec.Restore != nil {
			log.Info("Skip creating a new CDB database, waiting to be restored")
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.AwaitingRestore, "Awaiting restore CDB")
			return ctrl.Result{Requeue: true}, nil
		}

		if k8s.ConditionReasonEquals(instanceReadyCond, k8s.RestoreFailed) {
			log.Error(err, "failed to restore to a new CDB database")
			return ctrl.Result{}, nil
		}

		log.Info("Creating a new CDB database")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.CreateInProgress, "Bootstrapping CDB")
		if err := r.Status().Update(ctx, &inst); err != nil {
			log.Error(err, "failed to update the instance status")
		}

		if err = r.bootstrapCDB(ctx, inst, svc.Spec.ClusterIP, log); err != nil {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.CreateFailed, fmt.Sprintf("Error creating CDB: %v", err))
			log.Error(err, "Error while creating CDB database")
			r.Recorder.Eventf(&inst, corev1.EventTypeWarning, "DatabaseInstanceCreateFailed", fmt.Sprintf("Error creating CDB: %v", err))
			return ctrl.Result{}, err // No point in proceeding if the instance isn't provisioned
		}
		log.Info("Finished creating new CDB database")
	}

	dbiCond = k8s.FindCondition(inst.Status.Conditions, k8s.DatabaseInstanceReady)
	if istatus != controllers.StatusReady {
		log.Info("database instance doesn't appear to be ready yet...")

		elapsed := k8s.ElapsedTimeFromLastTransitionTime(dbiCond, time.Second)
		if elapsed < createDatabaseInstanceTimeout {
			log.Info(fmt.Sprintf("database instance creation in progress for %v, requeue after 30 seconds", elapsed))
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.CreateInProgress, "")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		log.Info(fmt.Sprintf("database instance creation timed out. Elapsed Time: %v", elapsed))
		if !strings.Contains(dbiCond.Message, "Warning") { // so that we would create only one database instance timeout event
			r.Recorder.Eventf(&inst, corev1.EventTypeWarning, k8s.DatabaseInstanceTimeout, "DatabaseInstance has been in progress for over %v, please verify if it is stuck and should be recreated.", createDatabaseInstanceTimeout)
		}
		k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.CreateInProgress, "Warning: db instance is taking a long time to start up - verify that instance has not failed")
		return ctrl.Result{}, nil // return nil so reconcile loop would not retry
	}

	if !k8s.ConditionStatusEquals(dbiCond, v1.ConditionTrue) {
		r.Recorder.Eventf(&inst, corev1.EventTypeNormal, k8s.DatabaseInstanceReady, "DatabaseInstance has been created successfully. Elapsed Time: %v", k8s.ElapsedTimeFromLastTransitionTime(dbiCond, time.Second))
	}

	k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionTrue, k8s.CreateComplete, "")

	log.Info("reconciling database instance: DONE")

	return ctrl.Result{}, nil
}

// isImageSeeded determines from the service image metadata file if the image is seeded or unseeded.
func isImageSeeded(ctx context.Context, clusterIP string, log logr.Logger) (bool, error) {

	log.Info("isImageSeeded: new database requested clusterIP", clusterIP)

	dialTimeout := 1 * time.Minute
	// Establish a connection to a Config Agent.
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	conn, err := grpc.Dial(fmt.Sprintf("%s:%d", clusterIP, consts.DefaultConfigAgentPort), grpc.WithInsecure())
	if err != nil {
		log.Error(err, "isImageSeeded: failed to create a conn via gRPC.Dial")
		return false, err
	}
	defer conn.Close()

	caClient := capb.NewConfigAgentClient(conn)
	serviceImageMetaData, err := caClient.FetchServiceImageMetaData(ctx, &capb.FetchServiceImageMetaDataRequest{})
	if err != nil {
		return false, fmt.Errorf("isImageSeeded: failed on FetchServiceImageMetaData call: %v", err)
	}
	if serviceImageMetaData.CdbName == "" {
		return false, nil
	}
	return true, nil
}

func (r *InstanceReconciler) overrideDefaultImages(config *v1alpha1.Config, images map[string]string, inst *v1alpha1.Instance, log logr.Logger) (ctrl.Result, error) {
	if config != nil {
		log.V(1).Info("customer config loaded", "config", config)

		if config.Spec.Platform != "GCP" && config.Spec.Platform != "BareMetal" && config.Spec.Platform != "Minikube" && config.Spec.Platform != "Kind" {
			return ctrl.Result{}, fmt.Errorf("Unsupported platform: %q", config.Spec.Platform)
		}

		// Replace the default images from the global Config, if so requested.
		log.Info("create instance: prep", "images explicitly requested for this config", config.Spec.Images)
		for k, image := range config.Spec.Images {
			log.Info("key value is", "k", k, "image", image)
			if v2, ok := images[k]; ok {
				log.Info("create instance: prep", "replacing", k, "image of", v2, "with global", image)
				images[k] = image
			}
		}
	} else {
		log.Info("no customer specific config found, assuming all defaults")
	}

	// Replace final images with those explicitly set for the Instance.
	if inst.Spec.Images != nil {
		log.Info("create instance: prep", "images explicitly requested for this instance", inst.Spec.Images)
		for k, v1 := range inst.Spec.Images {
			log.Info("k value is ", "key", k)
			if v2, ok := images[k]; ok {
				r.Log.Info("create instance: prep", "replacing", k, "image of", v2, "with instance specific", v1)
				images[k] = v1
			}
		}
	}

	serviceImageDefined := false
	if inst.Spec.Images != nil {
		if _, ok := inst.Spec.Images["service"]; ok {
			serviceImageDefined = true
			log.Info("service image requested via instance", "service image:", inst.Spec.Images["service"])
		}
	}
	if config != nil {
		if _, ok := config.Spec.Images["service"]; ok {
			serviceImageDefined = true
			log.Info("service image requested via config", "service image:", config.Spec.Images["service"])
		}
	}

	if inst.Spec.CDBName == "" {
		return ctrl.Result{}, fmt.Errorf("bootstrapCDB: CDBName isn't defined in the config")
	}
	if !serviceImageDefined {
		return ctrl.Result{}, fmt.Errorf("bootstrapCDB: Service image isn't defined in the config")
	}
	return ctrl.Result{}, nil
}

func diskSpecs(inst *v1alpha1.Instance, config *v1alpha1.Config) []commonv1alpha1.DiskSpec {
	if inst != nil && inst.Spec.Disks != nil {
		return inst.Spec.Disks
	}
	if config != nil && config.Spec.Disks != nil {
		return config.Spec.Disks
	}
	return defaultDisks
}

// bootstrapCDB is invoked during the instance creation phase for a database
// image not containing a CDB and does the following.
// a) Create a CDB for an unseeded image.
// b) Invoke a provisioning workflow for unseeded images, which will create listener.
func (r *InstanceReconciler) bootstrapCDB(ctx context.Context, inst v1alpha1.Instance, clusterIP string, log logr.Logger) error {
	// TODO: add better error handling.
	if inst.Spec.CDBName == "" || inst.Spec.DBUniqueName == "" {
		return fmt.Errorf("bootstrapCDB: at least one of the following required arguments is not defined: CDBName, DBUniqueName")
	}

	log.Info("bootstrapCDB: new database requested clusterIP", clusterIP)

	// TODO: Remove this timeout workaround once we have the LRO thing figured out.
	dialTimeout := 30 * time.Minute
	// Establish a connection to a Config Agent.
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	conn, err := grpc.Dial(fmt.Sprintf("%s:%d", clusterIP, consts.DefaultConfigAgentPort), grpc.WithInsecure())
	if err != nil {
		log.Error(err, "bootstrapCDB: failed to create a conn via gRPC.Dial")
		return err
	}
	defer conn.Close()

	caClient := capb.NewConfigAgentClient(conn)
	_, err = caClient.CreateCDB(ctx, &capb.CreateCDBRequest{
		Sid:           inst.Spec.CDBName,
		DbUniqueName:  inst.Spec.DBUniqueName,
		DbDomain:      controllers.GetDBDomain(&inst),
		CharacterSet:  inst.Spec.CharacterSet,
		MemoryPercent: int32(inst.Spec.MemoryPercent),
		//DBCA expects the parameters in the following string array format
		// ["key1=val1", "key2=val2","key3=val3"]
		AdditionalParams: mapsToStringArray(inst.Spec.Parameters),
	})
	if err != nil {
		return fmt.Errorf("bootstrapCDB: failed on CreateDatabase gRPC call: %v", err)
	}

	inst.Status.CurrentParameters = inst.Spec.Parameters
	if err := r.Status().Update(ctx, &inst); err != nil {
		log.Error(err, "failed to update an Instance status returning error")
		return err
	}

	caClient = capb.NewConfigAgentClient(conn)
	dbDomain := controllers.GetDBDomain(&inst)
	_, err = caClient.BootstrapDatabase(ctx, &capb.BootstrapDatabaseRequest{
		CdbName:      inst.Spec.CDBName,
		DbUniqueName: inst.Spec.DBUniqueName,
		Dbdomain:     dbDomain,
		Mode:         capb.BootstrapDatabaseRequest_Provision,
	})

	if err != nil {
		return fmt.Errorf("bootstrapCDB: error while running post-creation bootstrapping steps: %v", err)
	}

	return nil
}

func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Log.V(1).Info("SetupWithManager", "images", r.Images)

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Instance{}).
		Owns(&corev1.Service{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ConfigMap{}).
		Watches(
			&source.Kind{Type: &v1alpha1.Database{}},
			&handler.EnqueueRequestForObject{}).
		Complete(r)
}
