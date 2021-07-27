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
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
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
	physicalRestore                       = "PhysicalRestore"
	InstanceProvisionTimeoutSeeded        = 20 * time.Minute
	InstanceProvisionTimeoutUnseeded      = 50 * time.Minute // 50 minutes because it can take 40+ minutes for unseeded CDB creations
	CreateDatabaseInstanceTimeoutSeeded   = 20 * time.Minute
	CreateDatabaseInstanceTimeoutUnSeeded = 50 * time.Minute // 50 minutes because it can take 40+ minutes for unseeded CDB creations
	dateFormat                            = "20060102"
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

	images := make(map[string]string)
	for k, v := range r.Images {
		images[k] = v
	}

	if err := r.overrideDefaultImages(config, images, &inst, log); err != nil {
		return ctrl.Result{}, err
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

	// Create LB/NodePort Services if needed.
	svcLB, svc, err := r.createServices(ctx, inst, services, applyOpts)
	if err != nil {
		return ctrl.Result{}, err
	}

	if instanceReadyCond == nil {
		instanceReadyCond = k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.CreateInProgress, "")
	}

	inst.Status.Endpoint = fmt.Sprintf(controllers.SvcEndpoint, fmt.Sprintf(controllers.SvcName, inst.Name), inst.Namespace)
	inst.Status.URL = controllers.SvcURL(svcLB, consts.SecureListenerPort)

	isImageSeeded, err := r.isImageSeeded(ctx, &inst, log)
	if err != nil {
		log.Error(err, "unable to determine image type")
		return ctrl.Result{}, err
	}

	instanceProvisionTimeout := InstanceProvisionTimeoutUnseeded
	if isImageSeeded {
		instanceProvisionTimeout = InstanceProvisionTimeoutSeeded
	}

	// RequeueAfter 30 seconds to avoid constantly reconcile errors before statefulSet is ready.
	// Update status when the Service is ready (for the initial provisioning).
	// Also confirm that the StatefulSet is up and running.
	if k8s.ConditionReasonEquals(instanceReadyCond, k8s.CreateInProgress) {
		elapsed := k8s.ElapsedTimeFromLastTransitionTime(instanceReadyCond, time.Second)
		if elapsed > instanceProvisionTimeout {
			r.Recorder.Eventf(&inst, corev1.EventTypeWarning, "InstanceReady", fmt.Sprintf("Instance provision timed out after %v", instanceProvisionTimeout))
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

	log.Info("instance status", "instanceReadyCond", instanceReadyCond, "endpoint", inst.Status.Endpoint,
		"url", inst.Status.URL, "databases", inst.Status.DatabaseNames)

	log.Info("reconciling instance: DONE")

	istatus, err := controllers.CheckStatusInstanceFunc(ctx, inst.Name, inst.Spec.CDBName, svc.Spec.ClusterIP, controllers.GetDBDomain(&inst), log)
	if err != nil {
		log.Error(err, "failed to check the database instance status")
		return ctrl.Result{}, err
	}

	if istatus == controllers.StatusInProgress {
		if inst.Spec.Restore != nil {
			log.Info("Skip bootstrap CDB database, waiting to be restored")
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

		if err = r.bootstrapCDB(ctx, inst, svc.Spec.ClusterIP, log, isImageSeeded); err != nil {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.CreateFailed, fmt.Sprintf("Error bootstrapping CDB: %v", err))
			log.Error(err, "Error while bootstrapping CDB database")
			r.Recorder.Eventf(&inst, corev1.EventTypeWarning, "DatabaseInstanceCreateFailed", fmt.Sprintf("Error creating CDB: %v", err))
			return ctrl.Result{}, err // No point in proceeding if the instance isn't provisioned
		}
		log.Info("Finished bootstrapping CDB database")
	}

	dbInstanceCond = k8s.FindCondition(inst.Status.Conditions, k8s.DatabaseInstanceReady)
	if istatus != controllers.StatusReady {
		log.Info("database instance doesn't appear to be ready yet...")

		elapsed := k8s.ElapsedTimeFromLastTransitionTime(dbInstanceCond, time.Second)
		createDatabaseInstanceTimeout := CreateDatabaseInstanceTimeoutUnSeeded
		if isImageSeeded {
			createDatabaseInstanceTimeout = CreateDatabaseInstanceTimeoutSeeded
		}
		if elapsed < createDatabaseInstanceTimeout {
			log.Info(fmt.Sprintf("database instance creation in progress for %v, requeue after 30 seconds", elapsed))
			k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.CreateInProgress, "")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		log.Info(fmt.Sprintf("database instance creation timed out. Elapsed Time: %v", elapsed))
		if !strings.Contains(dbInstanceCond.Message, "Warning") { // so that we would create only one database instance timeout event
			r.Recorder.Eventf(&inst, corev1.EventTypeWarning, k8s.DatabaseInstanceTimeout, "DatabaseInstance has been in progress for over %v, please verify if it is stuck and should be recreated.", createDatabaseInstanceTimeout)
		}
		k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionFalse, k8s.CreateInProgress, "Warning: db instance is taking a long time to start up - verify that instance has not failed")
		return ctrl.Result{}, nil // return nil so reconcile loop would not retry
	}

	if !k8s.ConditionStatusEquals(dbInstanceCond, v1.ConditionTrue) {
		r.Recorder.Eventf(&inst, corev1.EventTypeNormal, k8s.DatabaseInstanceReady, "DatabaseInstance has been created successfully. Elapsed Time: %v", k8s.ElapsedTimeFromLastTransitionTime(dbInstanceCond, time.Second))
	}

	k8s.InstanceUpsertCondition(&inst.Status, k8s.DatabaseInstanceReady, v1.ConditionTrue, k8s.CreateComplete, "")

	log.Info("reconciling database instance: DONE")

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
