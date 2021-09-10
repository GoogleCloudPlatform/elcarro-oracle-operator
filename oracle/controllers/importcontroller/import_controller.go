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

package importcontroller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	capb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

// ImportReconciler reconciles an Import object.
type ImportReconciler struct {
	client.Client
	Log           logr.Logger
	Scheme        *runtime.Scheme
	ClientFactory controllers.ConfigAgentClientFactory
	Recorder      record.EventRecorder

	DatabaseClientFactory controllers.DatabaseClientFactory
}

const (
	reconcileTimeout = 3 * time.Minute
)

// readyConditionWrapper simplifies updating and using Ready condition
// of Import's status.
type readyConditionWrapper struct {
	imp          *v1alpha1.Import
	changed      bool
	defaultState string
}

func (w *readyConditionWrapper) getState() string {
	readyCond := k8s.FindCondition(w.imp.Status.Conditions, k8s.Ready)
	if readyCond == nil {
		w.setState(w.defaultState, "")
	}

	return k8s.FindCondition((&w.imp.Status).Conditions, k8s.Ready).Reason
}

func (w *readyConditionWrapper) setState(condReason, message string) {
	status := &w.imp.Status

	condStatus := metav1.ConditionFalse
	if condReason == k8s.ImportComplete {
		condStatus = metav1.ConditionTrue
	}

	status.Conditions = k8s.Upsert(status.Conditions, k8s.Ready, condStatus, condReason, message)
	w.changed = true
}

func (w *readyConditionWrapper) elapsedSinceLastStateChange() time.Duration {
	return k8s.ElapsedTimeFromLastTransitionTime(k8s.FindCondition(w.imp.Status.Conditions, k8s.Ready), time.Second)
}

var (
	requeueSoon  = ctrl.Result{RequeueAfter: 30 * time.Second}
	requeueLater = ctrl.Result{RequeueAfter: time.Minute}
)

// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=imports,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=imports/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=instances,verbs=get;list;watch
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=instances/status,verbs=get
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=databases,verbs=get;list;watch
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=databases/status,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is a generic reconcile function for Import resources.
func (r *ImportReconciler) Reconcile(_ context.Context, req ctrl.Request) (result ctrl.Result, recErr error) {
	log := r.Log.WithValues("Import", req.NamespacedName)
	log.Info("reconciling import")
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	imp := &v1alpha1.Import{}
	if err := r.Get(ctx, req.NamespacedName, imp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	impStatusWrapper := &readyConditionWrapper{imp: imp, defaultState: k8s.ImportPending}
	defer func() {
		if !impStatusWrapper.changed {
			return
		}
		if err := r.Status().Update(ctx, imp); err != nil {
			log.Error(err, "failed to update the import status")
			if recErr == nil {
				recErr = err
			}
		}
	}()

	switch impStatusWrapper.getState() {
	case k8s.ImportPending:
		return r.handleNotStartedImport(ctx, log, impStatusWrapper, req)
	case k8s.ImportInProgress:
		return r.handleRunningImport(ctx, log, impStatusWrapper, req)
	default:
		log.Info(fmt.Sprintf("import is in the state %q, no action needed", impStatusWrapper.getState()))
		return ctrl.Result{}, nil
	}

}

func (r *ImportReconciler) handleNotStartedImport(ctx context.Context, log logr.Logger, impWrapper *readyConditionWrapper, req ctrl.Request) (ctrl.Result, error) {
	var (
		db   = &v1alpha1.Database{}
		inst = &v1alpha1.Instance{}
		imp  = impWrapper.imp
	)

	// get referenced objects: database and instance
	dbKey := types.NamespacedName{
		Namespace: req.Namespace,
		Name:      imp.Spec.DatabaseName,
	}
	if err := r.Get(ctx, dbKey, db); err != nil {
		log.Error(err, "error getting database", "database", dbKey)
		return ctrl.Result{}, err
	}

	instKey := types.NamespacedName{
		Namespace: req.Namespace,
		Name:      imp.Spec.Instance,
	}
	if err := r.Get(ctx, instKey, inst); err != nil {
		log.Error(err, "error getting instance", "instance", instKey)
		return ctrl.Result{}, err
	}

	// validate
	if imp.Spec.Instance != db.Spec.Instance {
		return ctrl.Result{}, fmt.Errorf("instance names in Import and Database specs do not match:"+
			" %q != %q", imp.Spec.Instance, db.Spec.Instance)
	}

	dbReady := k8s.ConditionStatusEquals(
		k8s.FindCondition(db.Status.Conditions, k8s.Ready),
		metav1.ConditionTrue)

	// if can start, begin import
	if dbReady {
		caClient, closeConn, err := r.ClientFactory.New(ctx, r, req.Namespace, imp.Spec.Instance)
		if err != nil {
			log.Error(err, "failed to create config agent client")
			return ctrl.Result{}, err
		}
		defer closeConn()

		resp, err := caClient.DataPumpImport(ctx, &capb.DataPumpImportRequest{
			PdbName:    db.Spec.Name,
			DbDomain:   inst.Spec.DBDomain,
			GcsPath:    imp.Spec.GcsPath,
			GcsLogPath: imp.Spec.GcsLogPath,
			LroInput:   &capb.LROInput{OperationId: lroOperationID(imp)},
		})
		if err != nil {
			if !controllers.IsAlreadyExistsError(err) {
				impWrapper.setState(k8s.ImportPending, fmt.Sprintf("failed to start import: %v", err))
				return ctrl.Result{}, fmt.Errorf("failed to start import: %v", err)

			}
			log.Info("Import operation was already running")

		} else {
			log.Info("started DataPumpImport operation", "response", resp)
		}

		// Import started successfully
		impWrapper.setState(k8s.ImportInProgress, "")

	} else {
		log.Info("database is not yet ready")
	}

	return requeueSoon, nil
}

func (r *ImportReconciler) handleRunningImport(ctx context.Context, log logr.Logger, impWrapper *readyConditionWrapper, req ctrl.Request) (ctrl.Result, error) {
	imp := impWrapper.imp
	operationID := lroOperationID(imp)

	// check import LRO status
	operation, err := controllers.GetLROOperation(ctx, r.DatabaseClientFactory, operationID, imp.Spec.Instance)
	if err != nil {
		log.Error(err, "GetLROOperation returned an error")
		return ctrl.Result{}, err
	}
	log.Info("GetLROOperation", "response", operation)

	if !operation.Done {
		return requeueLater, nil
	}

	// handle import LRO completion
	log.Info("LRO is DONE", "operationID", operationID)
	defer func() {
		_ = controllers.DeleteLROOperation(r.ClientFactory, ctx, r, req.Namespace, operationID, imp.Spec.Instance)
	}()

	if operation.GetError() != nil {
		impWrapper.setState(
			k8s.ImportFailed,
			fmt.Sprintf("Failed to import on %s from %s: %s",
				time.Now().Format(time.RFC3339), imp.Spec.GcsPath, operation.GetError().GetMessage()))

		r.Recorder.Eventf(imp, corev1.EventTypeWarning, k8s.ImportFailed, fmt.Sprintf("Import error: %v", operation.GetError().GetMessage()))

		return ctrl.Result{}, err
	}

	// successful completion
	if impWrapper.getState() != k8s.ImportComplete {
		r.Recorder.Eventf(imp, corev1.EventTypeNormal, k8s.ImportComplete,
			"Import has completed successfully. Elapsed Time: %v", impWrapper.elapsedSinceLastStateChange())
	}
	impWrapper.setState(
		k8s.ImportComplete,
		fmt.Sprintf("Imported data on %s from %s",
			time.Now().Format(time.RFC3339), imp.Spec.GcsPath))

	return ctrl.Result{}, nil
}

// SetupWithManager configures the reconciler.
func (r *ImportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Import{}).
		Complete(r)
}

func lroOperationID(imp *v1alpha1.Import) string {
	return fmt.Sprintf("Import_%s", imp.GetUID())
}
