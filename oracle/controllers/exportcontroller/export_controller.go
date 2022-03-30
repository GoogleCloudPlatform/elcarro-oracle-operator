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

package exportcontroller

import (
	"context"
	"fmt"
	"strings"
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
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

// ExportReconciler reconciles an export object.
type ExportReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	DatabaseClientFactory controllers.DatabaseClientFactory
}

const (
	reconcileTimeout = 3 * time.Minute
)

// readyConditionWrapper simplifies updating and using Ready condition
// of Export's status.
type readyConditionWrapper struct {
	exp          *v1alpha1.Export
	changed      bool
	defaultState string
}

func (w *readyConditionWrapper) getState() string {
	readyCond := k8s.FindCondition(w.exp.Status.Conditions, k8s.Ready)
	if readyCond == nil {
		w.setState(w.defaultState, "")
	}

	return k8s.FindCondition((&w.exp.Status).Conditions, k8s.Ready).Reason
}

func (w *readyConditionWrapper) setState(condReason, message string) {
	status := &w.exp.Status

	condStatus := metav1.ConditionFalse
	if condReason == k8s.ExportComplete {
		condStatus = metav1.ConditionTrue
	}

	status.Conditions = k8s.Upsert(status.Conditions, k8s.Ready, condStatus, condReason, message)
	w.changed = true
}

func (w *readyConditionWrapper) elapsedSinceLastStateChange() time.Duration {
	return k8s.ElapsedTimeFromLastTransitionTime(k8s.FindCondition(w.exp.Status.Conditions, k8s.Ready), time.Second)
}

var (
	requeueSoon  = ctrl.Result{RequeueAfter: 30 * time.Second}
	requeueLater = ctrl.Result{RequeueAfter: time.Minute}
)

// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=exports,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=exports/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=instances,verbs=get;list;watch
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=instances/status,verbs=get
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=databases,verbs=get;list;watch
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=databases/status,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is a generic reconcile function for Export resources.
func (r *ExportReconciler) Reconcile(_ context.Context, req ctrl.Request) (result ctrl.Result, recErr error) {
	log := r.Log.WithValues("Export", req.NamespacedName)
	log.Info("reconciling export")
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	exp := &v1alpha1.Export{}
	if err := r.Get(ctx, req.NamespacedName, exp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	expStatusWrapper := &readyConditionWrapper{exp: exp, defaultState: k8s.ExportPending}
	defer func() {
		if !expStatusWrapper.changed {
			return
		}
		if err := r.Status().Update(ctx, exp); err != nil {
			log.Error(err, "failed to update the export status")
			if recErr == nil {
				recErr = err
			}
		}
	}()

	switch expStatusWrapper.getState() {
	case k8s.ExportPending:
		return r.handleNotStartedExport(ctx, log, expStatusWrapper, req)
	case k8s.ExportInProgress:
		return r.handleRunningExport(ctx, log, expStatusWrapper, req)
	default:
		log.Info(fmt.Sprintf("export is in the state %q, no action needed", expStatusWrapper.getState()))
		return ctrl.Result{}, nil
	}

}

func (r *ExportReconciler) handleNotStartedExport(ctx context.Context, log logr.Logger, expWrapper *readyConditionWrapper, req ctrl.Request) (ctrl.Result, error) {
	var (
		db   = &v1alpha1.Database{}
		inst = &v1alpha1.Instance{}
		exp  = expWrapper.exp
	)

	// get referenced objects: database and instance
	dbKey := types.NamespacedName{
		Namespace: req.Namespace,
		Name:      exp.Spec.DatabaseName,
	}
	if err := r.Get(ctx, dbKey, db); err != nil {
		log.Error(err, "error getting database", "database", dbKey)
		return ctrl.Result{}, err
	}

	instKey := types.NamespacedName{
		Namespace: req.Namespace,
		Name:      exp.Spec.Instance,
	}
	if err := r.Get(ctx, instKey, inst); err != nil {
		log.Error(err, "error getting instance", "instance", instKey)
		return ctrl.Result{}, err
	}

	// validate
	if exp.Spec.Instance != db.Spec.Instance {
		return ctrl.Result{}, fmt.Errorf("instance names in Export and Database specs do not match:"+
			" %q != %q", exp.Spec.Instance, db.Spec.Instance)
	}
	if len(exp.Spec.ExportObjects) == 0 {
		return ctrl.Result{}, fmt.Errorf("no object to export, exportObjects: %v", exp.Spec.ExportObjects)
	}

	dbReady := k8s.ConditionStatusEquals(
		k8s.FindCondition(db.Status.Conditions, k8s.Ready),
		metav1.ConditionTrue)

	// if can start, begin export
	if dbReady {
		dataPumpExportReq := &controllers.DataPumpExportRequest{
			PdbName:       db.Spec.Name,
			DbDomain:      inst.Spec.DBDomain,
			ObjectType:    exp.Spec.ExportObjectType,
			Objects:       strings.Join(exp.Spec.ExportObjects, ","),
			GcsPath:       exp.Spec.GcsPath,
			GcsLogPath:    exp.Spec.GcsLogPath,
			LroInput:      &controllers.LROInput{OperationId: lroOperationID(exp)},
			FlashbackTime: getFlashbackTime(exp.Spec.FlashbackTime),
		}
		resp, err := controllers.DataPumpExport(ctx, r, r.DatabaseClientFactory, inst.Namespace, inst.Name, *dataPumpExportReq)

		if err != nil {
			if !controllers.IsAlreadyExistsError(err) {
				expWrapper.setState(k8s.ExportPending, fmt.Sprintf("failed to start export: %v", err))
				return ctrl.Result{}, fmt.Errorf("failed to start export: %v", err)

			}
			log.Info("Export operation was already running")
		} else {
			log.Info("started DataPumpExport operation", "response", resp)
		}

		// Export started successfully
		expWrapper.setState(k8s.ExportInProgress, "")

	} else {
		log.Info("database is not yet ready")
	}

	return requeueSoon, nil
}

func (r *ExportReconciler) handleRunningExport(ctx context.Context, log logr.Logger, expWrapper *readyConditionWrapper, req ctrl.Request) (ctrl.Result, error) {
	exp := expWrapper.exp
	operationID := lroOperationID(exp)

	// check export LRO status
	operation, err := controllers.GetLROOperation(ctx, r.DatabaseClientFactory, r.Client, operationID, exp.GetNamespace(), exp.Spec.Instance)
	if err != nil {
		log.Error(err, "GetLROOperation returned an error")
		return ctrl.Result{}, err
	}
	log.Info("GetLROOperation", "response", operation)

	if !operation.Done {
		return requeueLater, nil
	}

	// handle export LRO completion
	log.Info("LRO is DONE", "operationID", operationID)
	defer func() {
		_ = controllers.DeleteLROOperation(ctx, r.DatabaseClientFactory, r.Client, operationID, exp.Namespace, exp.Spec.Instance)
	}()

	if operation.GetError() != nil {
		expWrapper.setState(
			k8s.ExportFailed,
			fmt.Sprintf("Failed to export objectType %s objects %v on %s to %s: %s",
				exp.Spec.ExportObjectType, exp.Spec.ExportObjects,
				time.Now().Format(time.RFC3339), exp.Spec.GcsPath, operation.GetError().GetMessage()))

		r.Recorder.Eventf(exp, corev1.EventTypeWarning, k8s.ExportFailed, fmt.Sprintf("Export error: %v", operation.GetError().GetMessage()))

		return ctrl.Result{}, err
	}

	// successful completion
	if expWrapper.getState() != k8s.ExportComplete {
		r.Recorder.Eventf(exp, corev1.EventTypeNormal, k8s.ExportComplete,
			"Export has completed successfully. Elapsed Time: %v", expWrapper.elapsedSinceLastStateChange())
	}
	expWrapper.setState(k8s.ExportComplete, fmt.Sprintf("Exported objectType %s objects %v on %s to %s",
		exp.Spec.ExportObjectType, exp.Spec.ExportObjects,
		time.Now().Format(time.RFC3339), exp.Spec.GcsPath))

	return ctrl.Result{}, nil
}

// SetupWithManager configures the reconciler.
func (r *ExportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Export{}).
		Complete(r)
}

func lroOperationID(exp *v1alpha1.Export) string {
	return fmt.Sprintf("Export_%s", exp.GetUID())
}

func getFlashbackTime(t *metav1.Time) string {
	var flashbackTime = ""
	if t != nil {
		flashbackTime = fmt.Sprintf("TO_TIMESTAMP('%s', 'DD-MM-YYYY HH24:MI:SS')", t.Format("02-01-2006 15:04:05"))
	}
	return flashbackTime
}
