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
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	commonutils "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/pkg/utils"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
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

	DatabaseClientFactory controllers.DatabaseClientFactory
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
	physicalRestore                      = "PhysicalRestore"
	InstanceReadyTimeout                 = 20 * time.Minute
	DatabaseInstanceReadyTimeoutSeeded   = 20 * time.Minute
	DatabaseInstanceReadyTimeoutUnseeded = 60 * time.Minute // 60 minutes because it can take 50+ minutes to create an unseeded CDB
	dateFormat                           = "20060102"
)

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
		r.updateIsChangeApplied(&inst, log)
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
		Disks:          controllers.DiskSpecs(&inst, config),
		Config:         config,
		Log:            log,
		Services:       enabledServices,
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

	if k8s.ConditionStatusEquals(instanceReadyCond, v1.ConditionTrue) && k8s.ConditionStatusEquals(dbInstanceCond, v1.ConditionTrue) {
		log.Info("instance has already been provisioned and ready")
		return ctrl.Result{}, nil
	}

	if result, err := r.createStatefulSet(ctx, &inst, sp, applyOpts, log); err != nil {
		return result, err
	}

	if result, err := r.createAgentDeployment(ctx, inst, config, images, enabledServices, applyOpts, log); err != nil {
		return result, err
	}

	dbLoadBalancer, err := r.createDBLoadBalancer(ctx, &inst, applyOpts)
	if err != nil {
		return ctrl.Result{}, err
	}

	_, agentSvc, err := r.createDataplaneServices(ctx, inst, applyOpts)
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
		conn, err := grpc.Dial(fmt.Sprintf("%s:%d", agentSvc.Spec.ClusterIP, consts.DefaultConfigAgentPort), grpc.WithInsecure())
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

	log.Info("instance status", "instanceReadyCond", instanceReadyCond, "endpoint", inst.Status.Endpoint,
		"url", inst.Status.URL, "databases", inst.Status.DatabaseNames)

	log.Info("reconciling instance: DONE")

	result, err := r.reconcileDatabaseInstance(ctx, &inst, r.Log)
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

// reconcileDatabaseInstance reconciling the underlying database instance.
// Successful state transition for seeded instance:
// nil->BootstrapPending->BootstrapInProgress->CreateFailed/CreateComplete
// Successful state transition for unseeded instance:
// nil->CreatePending->CreateInProgress->BootstrapPending->BootstrapInProgress->CreateFailed/CreateComplete
// Successful state transition for instance with restoreSpec:
// nil->RestorePending->CreateComplete
func (r *InstanceReconciler) reconcileDatabaseInstance(ctx context.Context, inst *v1alpha1.Instance, log logr.Logger) (ctrl.Result, error) {
	instanceReadyCond := k8s.FindCondition(inst.Status.Conditions, k8s.Ready)
	dbInstanceCond := k8s.FindCondition(inst.Status.Conditions, k8s.DatabaseInstanceReady)
	log.Info("reconciling database instance: ", "instanceReadyCond", instanceReadyCond, "dbInstanceCond", dbInstanceCond)

	// reconcile database only when instance is ready
	if !k8s.ConditionStatusEquals(instanceReadyCond, v1.ConditionTrue) {
		return ctrl.Result{}, nil
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
		caClient, closeConn, err := r.ClientFactory.New(ctx, r, inst.Namespace, inst.Name)
		if err != nil {
			log.Error(err, "failed to create config agent client")
			return ctrl.Result{}, err
		}
		defer closeConn()
		_, err = caClient.CreateCDB(ctx, &capb.CreateCDBRequest{
			Sid:           inst.Spec.CDBName,
			DbUniqueName:  inst.Spec.DBUniqueName,
			DbDomain:      controllers.GetDBDomain(inst),
			CharacterSet:  inst.Spec.CharacterSet,
			MemoryPercent: int32(inst.Spec.MemoryPercent),
			//DBCA expects the parameters in the following string array format
			// ["key1=val1", "key2=val2","key3=val3"]
			AdditionalParams: mapsToStringArray(inst.Spec.Parameters),
			LroInput:         &capb.LROInput{OperationId: lroCreateCDBOperationID(*inst)},
		})
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
		caClient, closeConn, err := r.ClientFactory.New(ctx, r, inst.Namespace, inst.Name)
		if err != nil {
			log.Error(err, "failed to create config agent client")
			return ctrl.Result{}, err
		}
		defer closeConn()
		bootstrapMode := capb.BootstrapDatabaseRequest_ProvisionUnseeded
		if isImageSeeded {
			bootstrapMode = capb.BootstrapDatabaseRequest_ProvisionSeeded
		}

		lro, err := caClient.BootstrapDatabase(ctx, &capb.BootstrapDatabaseRequest{
			CdbName:      inst.Spec.CDBName,
			DbUniqueName: inst.Spec.DBUniqueName,
			Dbdomain:     controllers.GetDBDomain(inst),
			Mode:         bootstrapMode,
			LroInput:     &capb.LROInput{OperationId: lroBootstrapCDBOperationID(*inst)},
		})

		if err != nil {
			if !controllers.IsAlreadyExistsError(err) {
				log.Error(err, "BootstrapDatabase failed")
				return ctrl.Result{}, err
			}
		} else if lro.GetDone() {
			// handle synchronous version of BootstrapDatabase
			r.Log.Info("encountered synchronous version of BootstrapDatabase")
			r.Log.Info("BootstrapDatabase DONE")
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionTrue, k8s.CreateComplete, "")
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
		k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionTrue, k8s.CreateComplete, "")
		// Reconcile again
		return ctrl.Result{Requeue: true}, r.Status().Update(ctx, inst)
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
