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
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	commonutils "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/pkg/utils"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

var CheckStatusInstanceFunc = controllers.CheckStatusInstanceFunc

// InstanceReconciler reconciles an Instance object.
type InstanceReconciler struct {
	client.Client
	Log           logr.Logger
	SchemeVal     *runtime.Scheme
	Images        map[string]string
	Recorder      record.EventRecorder
	InstanceLocks *sync.Map

	DatabaseClientFactory controllers.DatabaseClientFactory
}

func (r *InstanceReconciler) Scheme() *runtime.Scheme {
	return r.SchemeVal
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
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;create;update
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete

// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=databases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=databases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=configs,verbs=get;list;watch;create;update;patch;delete

const (
	physicalRestore                      = "PhysicalRestore"
	InstanceReadyTimeout                 = 120 * time.Minute
	DatabaseInstanceReadyTimeoutSeeded   = 30 * time.Minute
	DatabaseInstanceReadyTimeoutUnseeded = 60 * time.Minute // 60 minutes because it can take 50+ minutes to create an unseeded CDB
	dateFormat                           = "20060102"
	DefaultStsPatchingTimeout            = 25 * time.Minute
	reconcileTimeout                     = 3 * time.Minute
)

func (r *InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, respErr error) {
	ctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
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
		r.updateIsChangeApplied(&inst, log)
		if err := r.Status().Update(ctx, &inst); err != nil {
			log.Error(err, "failed to update the instance status")
			if respErr == nil {
				respErr = err
			}
		}
	}()

	if !inst.DeletionTimestamp.IsZero() {
		return r.reconcileInstanceDeletion(ctx, req, log)
	}

	// Add finalizer to clean up underlying objects in case of deletion.
	if !controllerutil.ContainsFinalizer(&inst, controllers.FinalizerName) {
		log.Info("adding a finalizer to the Instance object.")
		controllerutil.AddFinalizer(&inst, controllers.FinalizerName)
		if err := r.Update(ctx, &inst); err != nil {
			return ctrl.Result{}, err
		}
	}

	diskSpace, err := commonutils.DiskSpaceTotal(&inst)
	if err != nil {
		log.Error(err, "failed to calculate the total disk space")
	}
	log.Info("common instance", "total allocated disk space across all instance disks [Gi]", diskSpace/1024/1024/1024)

	instanceReadyCond := k8s.FindCondition(inst.Status.Conditions, k8s.Ready)
	dbInstanceCond := k8s.FindCondition(inst.Status.Conditions, k8s.DatabaseInstanceReady)

	if inst.Spec.Mode == commonv1alpha1.Pause {
		if instanceReadyCond == nil || dbInstanceCond == nil || instanceReadyCond.Reason != k8s.CreateComplete {
			log.Info("Ignoring pause mode since only instances in a stable state can be paused.")
		} else {
			r.InstanceLocks.Store(fmt.Sprintf("%s-%s", inst.Namespace, inst.Name), true)
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.PauseMode, "Instance switched to pause mode")
			log.Info("Instance has been set to pause for further reconciliation reset the pause mode")
			return ctrl.Result{}, nil
		}
	}

	var enabledServices []commonv1alpha1.Service
	for service, enabled := range inst.Spec.Services {
		if enabled {
			enabledServices = append(enabledServices, service)
		}
	}

	// If the instance is ready and DR enabled, we can set up standby DR.
	if k8s.ConditionReasonEquals(instanceReadyCond, k8s.StandbyDRInProgress) && isStandbyDR(&inst) {
		return r.standbyStateMachine(ctx, &inst, log)
	}

	if result, err := r.parameterUpdateStateMachine(ctx, req, inst, log); err != nil {
		return result, err
	}

	// If the instance and database is ready, we can set the instance parameters
	if k8s.ConditionStatusEquals(instanceReadyCond, v1.ConditionTrue) &&
		k8s.ConditionStatusEquals(dbInstanceCond, v1.ConditionTrue) && (inst.Spec.EnableDnfs != inst.Status.DnfsEnabled) {
		log.Info("instance and db is ready, modifying dNFS")
		if err := r.setDnfs(ctx, inst, inst.Spec.EnableDnfs); err != nil {
			return ctrl.Result{}, err
		}
		inst.Status.DnfsEnabled = inst.Spec.EnableDnfs
		if inst.Status.DnfsEnabled {
			log.Info("dNFS successfully enabled")
		} else {
			log.Info("dNFS successfully disabled")
		}
	}

	instanceReadyCond = k8s.FindCondition(inst.Status.Conditions, k8s.Ready)
	dbInstanceCond = k8s.FindCondition(inst.Status.Conditions, k8s.DatabaseInstanceReady)

	// Load default preferences (aka "config") if provided by a customer.
	config, err := r.loadConfig(ctx, req.NamespacedName.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	images := CloneMap(r.Images)

	if err := r.overrideDefaultImages(config, images, &inst, log); err != nil {
		return ctrl.Result{}, err
	}

	applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("instance-controller")}

	cm, err := controllers.NewConfigMap(&inst, r.Scheme(), fmt.Sprintf(controllers.CmName, inst.Name))
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
		Scheme:         r.Scheme(),
		Namespace:      req.NamespacedName.Namespace,
		Images:         images,
		SvcName:        fmt.Sprintf(controllers.SvcName, inst.Name),
		StsName:        fmt.Sprintf(controllers.StsName, inst.Name),
		PrivEscalation: false,
		ConfigMap:      cm,
		Disks:          controllers.DiskSpecs(&inst, config),
		Config:         config,
		Log:            log,
		Services:       enabledServices,
	}

	if IsPatchingStateMachineEntryCondition(inst.Spec.Services, inst.Status.ActiveImages, sp.Images, inst.Status.LastFailedImages, instanceReadyCond, dbInstanceCond) ||
		inst.Status.CurrentActiveStateMachine == "PatchingStateMachine" {
		databasePatchingTimeout := DefaultStsPatchingTimeout
		if inst.Spec.DatabasePatchingTimeout != nil {
			databasePatchingTimeout = inst.Spec.DatabasePatchingTimeout.Duration
		}
		result, err, done := r.patchingStateMachine(req, instanceReadyCond, dbInstanceCond, &inst, ctx, &sp, config, databasePatchingTimeout, log)
		if err != nil {
			log.Error(err, "patchingStateMachine failed")
		}
		if done {
			return result, err
		}
	}

	// If there is a Restore section in the spec the reconciliation will be handled
	// by restore state machine until the Spec.Restore section is removed again.
	if inst.Spec.Restore != nil {
		// Ask the restore state machine to reconcile
		result, err := r.restoreStateMachine(req, instanceReadyCond, dbInstanceCond, &inst, ctx, sp, log)
		if err != nil {
			log.Error(err, "restoreStateMachine failed")
			return result, err
		}
		if !result.IsZero() {
			return result, err
		}
		// No error and no result - state machine is done, proceed with main reconciler
	}

	//if we return something we have to requeue
	res, err := r.handleResize(ctx, &inst, instanceReadyCond, dbInstanceCond, sp, applyOpts, log)
	if err != nil {
		return ctrl.Result{}, err
	} else if !res.IsZero() {
		return res, nil
	}

	if k8s.ConditionStatusEquals(instanceReadyCond, v1.ConditionTrue) && k8s.ConditionStatusEquals(dbInstanceCond, v1.ConditionTrue) {
		log.Info("instance has already been provisioned and ready")
		if res, err := r.reconcileMonitoring(ctx, &inst, log, images); err != nil || res.RequeueAfter > 0 {
			return res, err
		}
		return ctrl.Result{}, r.updateDatabaseIncarnationStatus(ctx, &inst, r.Log)
	}

	if result, err := r.createStatefulSet(ctx, &inst, sp, applyOpts, log); err != nil {
		return result, err
	}

	dbLoadBalancer, err := r.createDBLoadBalancer(ctx, &inst, applyOpts)
	if err != nil {
		return ctrl.Result{}, err
	}

	_, _, err = r.createDataplaneServices(ctx, inst, applyOpts)
	if err != nil {
		return ctrl.Result{}, err
	}

	if instanceReadyCond == nil {
		instanceReadyCond = k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.CreateInProgress, "")
	}

	inst.Status.Endpoint = fmt.Sprintf(controllers.SvcEndpoint, fmt.Sprintf(controllers.SvcName, inst.Name), inst.Namespace)
	inst.Status.URL = commonutils.LoadBalancerURL(dbLoadBalancer, consts.SecureListenerPort)

	// RequeueAfter 30 seconds to avoid constantly reconcile errors before statefulSet is ready.
	// Update status when the Service is ready (for the initial provisioning).
	// Also confirm that the StatefulSet is up and running.
	if k8s.ConditionReasonEquals(instanceReadyCond, k8s.CreateInProgress) {
		elapsed := k8s.ElapsedTimeFromLastTransitionTime(instanceReadyCond, time.Second)
		if elapsed > InstanceReadyTimeout {
			r.Recorder.Eventf(&inst, corev1.EventTypeWarning, "InstanceReady", fmt.Sprintf("Instance provision timed out after %v", InstanceReadyTimeout))
			msg := fmt.Sprintf("Instance provision timed out. Elapsed Time: %v", elapsed)
			log.Info(msg)
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.CreateInProgress, msg)
			return ctrl.Result{}, nil
		}

		if !r.updateProgressCondition(ctx, inst, req.NamespacedName.Namespace, controllers.CreateInProgress, log) {
			log.Info("requeue after 30 seconds")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		if inst.Status.URL != "" {
			if !k8s.ConditionReasonEquals(instanceReadyCond, k8s.CreateComplete) {
				r.Recorder.Eventf(&inst, corev1.EventTypeNormal, "InstanceReady", "Instance has been created successfully. Elapsed Time: %v", elapsed)
			}
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionTrue, k8s.CreateComplete, "")
			inst.Status.ActiveImages = CloneMap(sp.Images)
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

	if isStandbyDR(&inst) {
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.StandbyDRInProgress, "standby DR in progress")
		return ctrl.Result{Requeue: true}, nil
	}

	// When we reach here, the instance should be ready.
	if inst.Spec.Mode == commonv1alpha1.ManuallySetUpStandby {
		log.Info("reconciling instance for manually set up standby: DONE")
		// the code will return here, so we can rely on defer function to update database status.
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.ManuallySetUpStandbyInProgress, fmt.Sprintf("Setting up standby database in progress, remove spec.mode %v to promote the instance", inst.Spec.Mode))
		k8s.InstanceUpsertCondition(&inst.Status, k8s.StandbyReady, v1.ConditionTrue, k8s.CreateComplete, fmt.Sprintf("standby instance creation complete, ready to set up standby database in the instance"))
		return ctrl.Result{}, nil
	}

	if k8s.ConditionStatusEquals(k8s.FindCondition(inst.Status.Conditions, k8s.StandbyReady), v1.ConditionTrue) {
		// promote the standby instance, bootstrap is part of promotion.
		r.Recorder.Eventf(&inst, corev1.EventTypeNormal, k8s.PromoteStandbyInProgress, "")
		if err := r.bootstrapStandby(ctx, &inst); err != nil {
			r.Recorder.Eventf(&inst, corev1.EventTypeWarning, k8s.PromoteStandbyFailed, fmt.Sprintf("Error promoting standby: %v", err))
			return ctrl.Result{}, err
		}
		// the standby instance has been successfully promoted, set ready condition
		// to true and standby ready to false. Promotion need to be idempotent to
		// ensure the correctness under retry.
		r.Recorder.Eventf(&inst, corev1.EventTypeNormal, k8s.PromoteStandbyComplete, "")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionTrue, k8s.CreateComplete, "")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionTrue, k8s.CreateComplete, "")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.StandbyReady, v1.ConditionFalse, k8s.PromoteStandbyComplete, "")
		return ctrl.Result{Requeue: true}, err
	}

	var dbs v1alpha1.DatabaseList
	if err := r.List(ctx, &dbs, client.InNamespace(req.Namespace)); err != nil {
		log.V(1).Info("failed to list databases for instance", "inst.Name", inst.Name)
	} else {
		log.Info("list of queried databases", "dbs", dbs)
	}

	log.Info("instance status", "instanceReadyCond", instanceReadyCond, "endpoint", inst.Status.Endpoint,
		"url", inst.Status.URL, "databases", inst.Status.DatabaseNames)

	log.Info("reconciling instance: DONE")

	result, err := r.reconcileDatabaseInstance(ctx, &inst, r.Log, images)
	log.Info("reconciling database instance: DONE", "result", result, "err", err)

	return result, nil
}

// Create a name for the createCDB LRO operation based on instance GUID.
func lroCreateCDBOperationID(instance v1alpha1.Instance) string {
	return fmt.Sprintf("CreateCDB_%s", instance.GetUID())
}

// Create a name for the bootstrapCDB LRO operation based on instance GUID.
func lroBootstrapCDBOperationID(instance v1alpha1.Instance) string {
	return fmt.Sprintf("BootstrapCDB_%s", instance.GetUID())
}

func (r *InstanceReconciler) reconcileInstanceDeletion(ctx context.Context, req ctrl.Request, log logr.Logger) (ctrl.Result, error) {
	log.Info("Deleting Instance...", "InstanceName", req.NamespacedName.Name)

	// NOTE: must be kept in sync with reconcileMonitoring
	var monitor appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: fmt.Sprintf("%s-monitor", req.Name)}, &monitor); err == nil {
		if err := r.Delete(ctx, &monitor); err != nil {
			log.Error(err, "failed to delete monitoring deployment", "InstanceName", req.Name, "MonitorDeployment", monitor.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	} else if !apierrors.IsNotFound(err) { // retry on other errors.
		return ctrl.Result{}, err
	}

	var inst v1alpha1.Instance
	if err := r.Get(ctx, req.NamespacedName, &inst); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if len(inst.Status.DatabaseNames) == 0 {
		controllerutil.RemoveFinalizer(&inst, controllers.FinalizerName)
		if err := r.Update(ctx, &inst); err != nil {
			log.Error(err, "failed to remove a finalizer from an Instance", "InstanceName", inst.Name, "FinalizerName", controllers.FinalizerName)
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	for _, dbName := range inst.Status.DatabaseNames {
		var db v1alpha1.Database
		if err := r.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: dbName}, &db); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		log.Info("instance deletion in progress. deleting an attached Database(PDB) first", "InstanceName", inst.Name, "DatabaseName", db.Name)
		if err := r.Delete(ctx, &db); err != nil {
			log.Error(err, "failed to delete database(PDB) attached to Instance", "InstanceName", inst.Name, "DatabaseName", db.Name)
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// reconcileDatabaseInstance reconciling the underlying database instance.
// Successful state transition for seeded instance:
// nil->BootstrapPending->BootstrapInProgress->CreateFailed/CreateComplete
// Successful state transition for unseeded instance:
// nil->CreatePending->CreateInProgress->BootstrapPending->BootstrapInProgress->ReconcileServices->CreateFailed/CreateComplete
// Successful state transition for instance with restoreSpec:
// nil->RestorePending->CreateComplete
func (r *InstanceReconciler) reconcileDatabaseInstance(ctx context.Context, inst *v1alpha1.Instance, log logr.Logger, images map[string]string) (ctrl.Result, error) {
	instanceReadyCond := k8s.FindCondition(inst.Status.Conditions, k8s.Ready)
	dbInstanceCond := k8s.FindCondition(inst.Status.Conditions, k8s.DatabaseInstanceReady)
	log.Info("reconciling database instance: ", "instanceReadyCond", instanceReadyCond, "dbInstanceCond", dbInstanceCond)

	// reconcile database only when instance is ready, but requeue.
	if !k8s.ConditionStatusEquals(instanceReadyCond, v1.ConditionTrue) {
		return ctrl.Result{Requeue: true}, nil
	}

	isImageSeeded, err := r.isImageSeeded(ctx, inst, log)
	if err != nil {
		log.Error(err, "unable to determine image type")
		return ctrl.Result{}, err
	}

	if dbInstanceCond == nil {
		if inst.Spec.Restore != nil {
			log.Info("Skip bootstrap CDB database, waiting to be restored")
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.RestorePending, "Awaiting restore CDB")
		} else if !isImageSeeded {
			log.Info("Unseeded image used, waiting to be created")
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.CreatePending, "Awaiting create CDB")
		} else {
			log.Info("Seeded image used, waiting to be bootstrapped")
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.BootstrapPending, "Awaiting bootstrap CDB")
		}
		return ctrl.Result{Requeue: true}, r.Status().Update(ctx, inst)
	}

	// Check for timeout
	if !k8s.ConditionStatusEquals(dbInstanceCond, v1.ConditionTrue) {
		elapsed := k8s.ElapsedTimeFromLastTransitionTime(dbInstanceCond, time.Second)
		createDatabaseInstanceTimeout := DatabaseInstanceReadyTimeoutSeeded
		if !isImageSeeded {
			createDatabaseInstanceTimeout = DatabaseInstanceReadyTimeoutUnseeded
		}
		if elapsed < createDatabaseInstanceTimeout {
			log.Info(fmt.Sprintf("database instance creation in progress for %v", elapsed))
		} else {
			log.Info(fmt.Sprintf("database instance creation timed out. Elapsed Time: %v", elapsed))
			if !strings.Contains(dbInstanceCond.Message, "Warning") { // so that we would create only one database instance timeout event
				r.Recorder.Eventf(inst, corev1.EventTypeWarning, k8s.DatabaseInstanceTimeout, "DatabaseInstance has been in progress for over %v, please try delete and recreate.", createDatabaseInstanceTimeout)
			}
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, dbInstanceCond.Reason, "Warning: db instance is taking too long to start up - please try delete and recreate")
			return ctrl.Result{}, nil
		}
	}

	switch dbInstanceCond.Reason {
	case k8s.CreatePending:
		// Launch the CreateCDB LRO
		req := &controllers.CreateCDBRequest{
			Sid:           inst.Spec.CDBName,
			DbUniqueName:  inst.Spec.DBUniqueName,
			DbDomain:      controllers.GetDBDomain(inst),
			CharacterSet:  inst.Spec.CharacterSet,
			MemoryPercent: int32(inst.Spec.MemoryPercent),
			//DBCA expects the parameters in the following string array format
			// ["key1=val1", "key2=val2","key3=val3"]
			AdditionalParams: mapsToStringArray(inst.Spec.Parameters),
			LroInput:         &controllers.LROInput{OperationId: lroCreateCDBOperationID(*inst)},
		}
		_, err = controllers.CreateCDB(ctx, r, r.DatabaseClientFactory, inst.GetNamespace(), inst.GetName(), *req)
		if err != nil {
			if !controllers.IsAlreadyExistsError(err) {
				log.Error(err, "CreateCDB failed")
				return ctrl.Result{}, err
			}
		} else {
			log.Info("CreateCDB started")
		}
		k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.CreateInProgress, "Database creation in progress")
		return ctrl.Result{Requeue: true}, r.Status().Update(ctx, inst)
	case k8s.CreateInProgress:
		id := lroCreateCDBOperationID(*inst)
		done, err := controllers.IsLROOperationDone(ctx, r.DatabaseClientFactory, r.Client, id, inst.GetNamespace(), inst.GetName())
		if !done {
			log.Info("CreateCDB still in progress, waiting")
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
		controllers.DeleteLROOperation(ctx, r.DatabaseClientFactory, r.Client, id, inst.Namespace, inst.Name)
		if err != nil {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.CreateFailed, "CreateCDB LRO returned error")
			log.Error(err, "CreateCDB LRO returned error")
			return ctrl.Result{Requeue: true}, r.Status().Update(ctx, inst)
		}
		log.Info("CreateCDB done successfully")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.BootstrapPending, "")
		// Reconcile again
		return ctrl.Result{Requeue: true}, r.Status().Update(ctx, inst)
	case k8s.BootstrapPending:
		// Launch the BootstrapDatabase LRO
		bootstrapMode := controllers.BootstrapDatabaseRequest_ProvisionUnseeded
		if isImageSeeded {
			bootstrapMode = controllers.BootstrapDatabaseRequest_ProvisionSeeded
		}
		req := &controllers.BootstrapDatabaseRequest{
			CdbName:      inst.Spec.CDBName,
			DbUniqueName: inst.Spec.DBUniqueName,
			Dbdomain:     controllers.GetDBDomain(inst),
			Mode:         bootstrapMode,
			LroInput:     &controllers.LROInput{OperationId: lroBootstrapCDBOperationID(*inst)},
		}
		lro, err := controllers.BootstrapDatabase(ctx, r, r.DatabaseClientFactory, inst.Namespace, inst.Name, *req)

		if err != nil {
			if !controllers.IsAlreadyExistsError(err) {
				log.Error(err, "BootstrapDatabase failed")
				return ctrl.Result{}, err
			}
		} else if lro.GetDone() {
			// handle synchronous version of BootstrapDatabase
			r.Log.Info("encountered synchronous version of BootstrapDatabase")
			r.Log.Info("BootstrapDatabase DONE")
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.ReconcileServices, "Services starting")
			return ctrl.Result{Requeue: true}, r.Status().Update(ctx, inst)
		}
		log.Info("BootstrapDatabase started")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.BootstrapInProgress, "Database bootstrap in progress")
		return ctrl.Result{Requeue: true}, r.Status().Update(ctx, inst)
	case k8s.BootstrapInProgress:
		id := lroBootstrapCDBOperationID(*inst)
		done, err := controllers.IsLROOperationDone(ctx, r.DatabaseClientFactory, r.Client, id, inst.GetNamespace(), inst.GetName())
		if !done {
			log.Info("BootstrapDatabase still in progress, waiting")
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
		controllers.DeleteLROOperation(ctx, r.DatabaseClientFactory, r.Client, id, inst.Namespace, inst.Name)
		if err != nil {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.CreateFailed, "BootstrapDatabase LRO returned error")
			log.Error(err, "BootstrapDatabase LRO returned error")
			return ctrl.Result{Requeue: true}, r.Status().Update(ctx, inst)
		}
		log.Info("BootstrapDatabase done successfully")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.ReconcileServices, "Services starting")
		// Reconcile again
		return ctrl.Result{Requeue: true}, r.Status().Update(ctx, inst)
	case k8s.ReconcileServices:
		res, err := r.reconcileMonitoring(ctx, inst, log, images)
		if err == nil && res.RequeueAfter == 0 {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionTrue, k8s.CreateComplete, "")
			return ctrl.Result{Requeue: true}, r.Status().Update(ctx, inst)
		}
		return res, err
	case k8s.RestorePending:
		if k8s.ConditionReasonEquals(instanceReadyCond, k8s.RestoreComplete) {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionTrue, k8s.CreateComplete, "")
			return ctrl.Result{Requeue: true}, r.Status().Update(ctx, inst)
		}
	default:
		r.Log.Info("reconcileDatabaseInstance: no action needed, proceed with main reconciliation")
	}

	return ctrl.Result{}, nil
}

// setDnfs enables dNFS protocol in Oracle database.
func (r *InstanceReconciler) setDnfs(ctx context.Context, inst v1alpha1.Instance, enable bool) error {
	dbClient, closeConn, err := r.DatabaseClientFactory.New(ctx, r, inst.GetNamespace(), inst.Name)
	if err != nil {
		return err
	}
	defer closeConn()

	if _, err := dbClient.SetDnfsState(ctx, &dbdpb.SetDnfsStateRequest{
		Enable: enable,
	}); err != nil {
		return fmt.Errorf("error while enabling dNFS: %v", err)
	}

	_, err = dbClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
		DatabaseName: inst.Spec.CDBName,
		Option:       "immediate",
	})
	if err != nil {
		return fmt.Errorf("BounceDatabase: error while shutting db: %v", err)
	}

	_, err = dbClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:         dbdpb.BounceDatabaseRequest_STARTUP,
		DatabaseName:      inst.Spec.CDBName,
		AvoidConfigBackup: false,
	})
	if err != nil {
		return fmt.Errorf("dbClient/BounceDatabase: error while starting db: %v", err)
	}

	return nil
}

func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Log.V(1).Info("SetupWithManager", "images", r.Images)

	// configPredicate is used to determine if watched events should cause
	// all instances in the namespace to be reconciled. Right now only
	// Images in the config affect running instances and can cause Patching
	// so we only trigger instance reconciliation on Create/Delete and
	// Update when Images changes.
	configPredicate := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldConfig, ok := e.ObjectOld.(*v1alpha1.Config)
			if !ok {
				return false
			}
			newConfig, ok := e.ObjectNew.(*v1alpha1.Config)
			if !ok {
				return false
			}
			return !cmp.Equal(oldConfig.Spec.Images, newConfig.Spec.Images)
		},
		CreateFunc:  func(e event.CreateEvent) bool { return true },
		DeleteFunc:  func(e event.DeleteEvent) bool { return true },
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}

	instancesForConfig := func(obj client.Object) []ctrl.Request {
		var requests []ctrl.Request
		var insts v1alpha1.InstanceList
		if err := r.List(context.Background(), &insts, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		for _, inst := range insts.Items {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      inst.Name,
					Namespace: inst.Namespace,
				}})
		}
		r.Log.Info("Config event triggered instance reconcile ", "requests", requests)
		return requests
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Instance{}).
		Owns(&corev1.Service{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ConfigMap{}).
		Watches(
			&source.Kind{Type: &v1alpha1.Config{}},
			handler.EnqueueRequestsFromMapFunc(instancesForConfig),
			builder.WithPredicates(configPredicate),
		).
		Complete(r)
}

func lroOperationID(opType string, instance *v1alpha1.Instance) string {
	switch opType {
	case physicalRestore:
		return fmt.Sprintf("%s_%s_%s", opType, instance.GetUID(), instance.Status.LastRestoreTime.Format(time.RFC3339))
	default:
		return fmt.Sprintf("%s_%s", opType, instance.GetUID())
	}
}
