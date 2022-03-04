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

package databasecontroller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common/sql"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

const (
	DatabaseContainerName = "oracledb"
)

// These variables allow to plug in mock objects for functional tests
var (
	skipLBCheckForTest      = false
	CheckStatusInstanceFunc = controllers.CheckStatusInstanceFunc
)

// DatabaseReconciler reconciles a Database object
type DatabaseReconciler struct {
	client.Client
	Log           logr.Logger
	Scheme        *runtime.Scheme
	ClientFactory controllers.ConfigAgentClientFactory
	Recorder      record.EventRecorder

	DatabaseClientFactory controllers.DatabaseClientFactory
}

func (r *DatabaseReconciler) findPod(ctx context.Context, namespace, instName string) (*corev1.PodList, error) {
	// List the Pods matching the PodTemplate Labels
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{"instance": instName}); err != nil {
		return nil, err
	}

	return &pods, nil
}

func findContainer(pod corev1.Pod, c string) (*corev1.Container, error) {
	for _, con := range pod.Spec.Containers {
		if con.Name == c {
			return &con, nil
		}
	}
	return nil, fmt.Errorf("failed to find a container %s in a pod: %v", c, pod)
}

// updateIsChangeApplied sets status.IsChangeApplied field to false if observedGeneration < generation, it sets it to true if changes are applied.
func (r *DatabaseReconciler) updateIsChangeApplied(ctx context.Context, db *v1alpha1.Database) {
	if db.Status.ObservedGeneration < db.Generation {
		db.Status.IsChangeApplied = v1.ConditionFalse
		db.Status.ObservedGeneration = db.Generation
		r.Log.Info("change detected", "observedGeneration", db.Status.ObservedGeneration, "generation", db.Generation)
	}
	if db.Status.IsChangeApplied == v1.ConditionTrue {
		return
	}
	userUpdateDone := k8s.ConditionStatusEquals(k8s.FindCondition(db.Status.Conditions, k8s.UserReady), v1.ConditionTrue)
	if userUpdateDone {
		db.Status.IsChangeApplied = v1.ConditionTrue
	}
	r.Log.Info("change applied", "observedGeneration", db.Status.ObservedGeneration, "generation", db.Generation)
}

// +kubebuilder:rbac:groups=database.oracle.db.anthosapis.com,resources=databases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=database.oracle.db.anthosapis.com,resources=databases/status,verbs=get;update;patch

// +kubebuilder:rbac:groups=core,resources=services,verbs=list;watch;get;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=get;list;create;update;patch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get;list

// Reconcile is the main method that reconciles the Database resource.
func (r *DatabaseReconciler) Reconcile(_ context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("Database", req.NamespacedName)

	log.Info("reconciling database")

	var db v1alpha1.Database

	if err := r.Get(ctx, req.NamespacedName, &db); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := validateSpec(&db); err != nil {
		return ctrl.Result{}, r.handlePreflightCheckError(ctx, &db, err)
	}

	// Find an Instance resource that the Database belongs to.
	var inst v1alpha1.Instance
	if err := r.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: db.Spec.Instance}, &inst); err != nil {
		return ctrl.Result{}, r.handlePreflightCheckError(ctx, &db, fmt.Errorf("failed to find instance %q for database %q", db.Spec.Instance, db.Name))
	}
	log.Info("found the following instance for the create a new database request", "db.Spec.Instance", db.Spec.Instance, "inst", inst)

	DBDomain := controllers.GetDBDomain(&inst)

	// Find a pod running a database container.
	pods, err := r.findPod(ctx, req.Namespace, db.Spec.Instance)
	if err != nil {
		return ctrl.Result{}, r.handlePreflightCheckError(ctx, &db, fmt.Errorf("failed to find a pod"))
	}
	log.V(2).Info("found a pod", "pods", pods)

	if len(pods.Items) != 1 {
		return ctrl.Result{}, r.handlePreflightCheckError(ctx, &db, fmt.Errorf("expected 1 pod, found %d", len(pods.Items)))
	}

	// Find a database container within that pod.
	if _, err := findContainer(pods.Items[0], DatabaseContainerName); err != nil {
		log.Error(err, "reconciling database - failed to find a database container")
		return ctrl.Result{}, err
	}
	log.V(1).Info("a database container identified")

	// svc is needed to extract the ClusterIP, which is used in all the gRPC calls.
	svc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Name: fmt.Sprintf(controllers.AgentSvcName, db.Spec.Instance), Namespace: req.NamespacedName.Namespace}, svc); err != nil {
		return ctrl.Result{}, err
	}

	// CDBName is specified in Instance specs
	cdbName := inst.Spec.CDBName
	istatus, err := CheckStatusInstanceFunc(ctx, db.Spec.Instance, cdbName, svc.Spec.ClusterIP, DBDomain, log)
	if err != nil {
		log.Error(err, "preflight check failed", "check the database instance status", "failed")
		return ctrl.Result{}, err
	}

	if istatus != controllers.StatusReady {
		return ctrl.Result{}, r.handlePreflightCheckError(ctx, &db, fmt.Errorf("database instance doesn't appear to be ready yet"))
	}

	log.Info("preflight check: database instance is ready")

	// Confirm that an external LB is ready.
	lbSvc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Name: fmt.Sprintf(controllers.SvcName, db.Spec.Instance), Namespace: req.NamespacedName.Namespace}, lbSvc); err != nil {
		return ctrl.Result{}, err
	}

	if len(lbSvc.Status.LoadBalancer.Ingress) == 0 && !skipLBCheckForTest {
		return ctrl.Result{}, fmt.Errorf("preflight check: createDatabase: external LB is NOT ready")
	}
	log.Info("preflight check: createDatabase external LB service is ready", "svcName", lbSvc.Name)

	alreadyExists, err := NewDatabase(ctx, r, &db, svc.Spec.ClusterIP, DBDomain, cdbName, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Eventf(&db, corev1.EventTypeNormal, k8s.CreatedDatabase, fmt.Sprintf("Created new database %q", db.Spec.Name))
	db.Status.Phase = commonv1alpha1.DatabaseReady
	db.Status.Conditions = k8s.Upsert(db.Status.Conditions, k8s.Ready, v1.ConditionTrue, k8s.CreateComplete, "")
	if err := r.Status().Update(ctx, &db); err != nil {
		return ctrl.Result{}, err
	}

	if alreadyExists {
		if err := SyncUsers(ctx, r, &db, svc.Spec.ClusterIP, cdbName, log); err != nil {
			log.Error(err, "failed to sync database")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	log.V(1).Info("[DEBUG] create users", "Database", db.Spec.Name, "Users/Privs", db.Spec.Users)
	if err := NewUsers(ctx, r, &db, svc.Spec.ClusterIP, DBDomain, cdbName, log); err != nil {
		return ctrl.Result{}, err
	}

	// check DB name against existing ones to decide whether this is a new DB
	if !controllers.Contains(inst.Status.DatabaseNames, db.Spec.Name) {
		log.Info("found a new DB", "dbName", db.Spec.Name)
		inst.Status.DatabaseNames = append(inst.Status.DatabaseNames, db.Spec.Name)
	} else {
		log.V(1).Info("not a new DB, skipping the update", "dbName", db.Spec.Name)
	}

	log.Info("instance status", "conditions", inst.Status.Conditions, "endpoint", inst.Status.Endpoint,
		"url", inst.Status.URL, "databases", inst.Status.DatabaseNames)

	if err := r.Status().Update(ctx, &inst); err != nil {
		log.Error(err, "failed to update an Instance status")
		return ctrl.Result{}, err
	}

	log.Info("reconciling database: DONE")

	return ctrl.Result{}, nil
}

func (r *DatabaseReconciler) instanceToDatabases(obj client.Object) []ctrl.Request {
	var requests []ctrl.Request
	for _, name := range obj.(*v1alpha1.Instance).Status.DatabaseNames {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      name,
				Namespace: obj.GetNamespace(),
			}})
	}
	r.Log.Info("Instance event triggered reconcile ", "requests", requests)
	return requests
}

// SetupWithManager starts the reconciler loop.
func (r *DatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {

	// UpdateFunc is used to judge if instance event is a 'DatabaseInstanceReady' event. If that is true, the event will be processed by the database reconciler
	databaseInstanceReadyPredicate := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldInstance, ok := e.ObjectOld.(*v1alpha1.Instance)
			if !ok {
				r.Log.Info("Expected instance", "type", e.ObjectOld.GetObjectKind().GroupVersionKind().String())
				return false
			}
			if cond := k8s.FindCondition(oldInstance.Status.Conditions, k8s.DatabaseInstanceReady); k8s.ConditionStatusEquals(cond, v1.ConditionTrue) {
				return false
			}
			newInstance, ok := e.ObjectNew.(*v1alpha1.Instance)
			if !ok {
				r.Log.Info("Expected instance", "type", e.ObjectNew.GetObjectKind().GroupVersionKind().String())
				return false
			}
			if cond := k8s.FindCondition(newInstance.Status.Conditions, k8s.DatabaseInstanceReady); !k8s.ConditionStatusEquals(cond, v1.ConditionTrue) {
				return false
			}
			r.Log.Info("DatabaseInstanceReady changes to true")
			return true
		},
		CreateFunc:  func(e event.CreateEvent) bool { return false },
		DeleteFunc:  func(e event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}

	// We watch the instance event so we can trigger database creation when instance is ready.
	// Add a databaseInstanceReadyPredicate to avoid constantly triggering reconciliation for every instance event.
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Database{}).
		Owns(&corev1.Service{}).
		Watches(
			&source.Kind{Type: &v1alpha1.Instance{}},
			handler.EnqueueRequestsFromMapFunc(r.instanceToDatabases),
			builder.WithPredicates(databaseInstanceReadyPredicate),
		).
		Complete(r)
}

func (r *DatabaseReconciler) handlePreflightCheckError(ctx context.Context, db *v1alpha1.Database, err error) error {
	r.Log.Error(err, "database preflightCheck failed")
	r.Recorder.Eventf(db, corev1.EventTypeWarning, k8s.CreatePending, err.Error())
	db.Status.Phase = commonv1alpha1.DatabaseCreating
	db.Status.Conditions = k8s.Upsert(db.Status.Conditions, k8s.Ready, v1.ConditionFalse, k8s.CreatePending, err.Error())
	if err := r.Status().Update(ctx, db); err != nil {
		r.Log.Error(err, "failed to update database status")
	}
	return err
}

// validateSpec validates the database spec.
func validateSpec(db *v1alpha1.Database) error {
	// Currently only support validate db spec for user credentials.
	// no sensitive information is logged underlying.
	if (db.Spec.AdminPassword != "") && (db.Spec.AdminPasswordGsmSecretRef != nil) {
		return fmt.Errorf("resources/validateSpec: invalid database admin password spec; you can only specify either admin_password or adminPasswordGsmSecretRef")
	}
	for _, u := range db.Spec.Users {
		if (u.Password != "") && (u.GsmSecretRef != nil) {
			return fmt.Errorf("resources/validateSpec: invalid database user password spec for user %q; you can only specify either password or GsmSecretRef", u.Name)
		}
	}

	if _, err := sql.Identifier(db.Spec.Name); err != nil {
		return fmt.Errorf("resources/validateSpec: pdb name is not valid: %w", err)
	}
	if db.Spec.AdminPassword != "" {
		if _, err := sql.Identifier(db.Spec.AdminPassword); err != nil {
			return fmt.Errorf("resources/validateSpec: admin_password is not valid: %w", err)
		}
	}
	for _, u := range db.Spec.Users {
		if _, err := sql.ObjectName(u.Name); err != nil {
			return fmt.Errorf("resources/validateSpec: invalid user %q: %w", u.Name, err)
		}
		if u.Password != "" {
			if _, err := sql.Identifier(u.Password); err != nil {
				return fmt.Errorf("resources/validateSpec: password for user %q is not valid: %w", u.Name, err)
			}
		}
		for _, privilege := range u.Privileges {
			if !sql.IsPrivilege(string(privilege)) {
				return fmt.Errorf("resources/validateSpec: invalid privilege %q for user %q", privilege, u.Name)
			}
		}
	}

	return nil
}
