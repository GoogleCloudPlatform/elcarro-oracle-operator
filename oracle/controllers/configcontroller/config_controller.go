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

package configcontroller

import (
	"context"
	"flag"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
)

// ConfigReconciler reconciles a Config object
type ConfigReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Images   map[string]string
	Recorder record.EventRecorder
}

var (
	findInstances = (*ConfigReconciler).findInstances
	Patch         = (*ConfigReconciler).Patch
)

// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=configs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=configs/status,verbs=get;update;patch

// Reconcile looks for the config upload requests and populates
// Operator config with the customer requested values.
func (r *ConfigReconciler) Reconcile(_ context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("Config", req.NamespacedName)

	log.Info("reconciling config requests")

	var config v1alpha1.Config
	if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
		log.Error(err, "get config request error")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if config.Spec.LogLevel != nil && config.Spec.LogLevel[controllers.OperatorName] != "" {
		flag.Set("v", config.Spec.LogLevel[controllers.OperatorName])
	} else {
		flag.Set("v", "0")
	}

	instances, err := findInstances(r, ctx, req.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	// This is to allow creating a Config even when no Instances exist yet.
	if len(instances) == 0 {
		log.Info("no Instances found")
		return ctrl.Result{}, nil
	}
	log.Info("Config controller", "Number of instances to be configured", len(instances))

	for _, instance := range instances {
		log.Info("Patching Agent Deployment for", "instance", instance.Name)
		err := r.patchAgentDeployment(ctx, instance, config)
		if err != nil {
			log.Error(err, "failed to patch Agent Deployment for", "instance", instance.Name)
			//fail fast
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// patchAgentDeployment patches the agent deployment for the specified instance using the provided config
func (r *ConfigReconciler) patchAgentDeployment(ctx context.Context, instance v1alpha1.Instance, config v1alpha1.Config) error {
	applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("instance-controller")}
	agentParam := controllers.AgentDeploymentParams{
		Inst:           &instance,
		Config:         &config,
		Scheme:         r.Scheme,
		Name:           fmt.Sprintf(controllers.AgentDeploymentName, instance.Name),
		Images:         r.Images,
		PrivEscalation: false,
		Log:            r.Log,
		Args:           controllers.GetLogLevelArgs(&config),
	}

	agentDeployment, err := controllers.NewAgentDeployment(agentParam)
	if err != nil {
		r.Log.Error(err, "failed to create a Deployment", "agent deployment", agentDeployment)
		return err
	}

	if err := Patch(r, ctx, agentDeployment, client.Apply, applyOpts...); err != nil {
		r.Log.Error(err, "failed to patch the Deployment", "agent deployment.Status", agentDeployment.Status)
		return err
	}
	return nil
}

// findInstances attempts to find all the instances to which a config should be applied.
func (r *ConfigReconciler) findInstances(ctx context.Context, namespace string) ([]v1alpha1.Instance, error) {
	var instances v1alpha1.InstanceList
	if err := r.List(ctx, &instances, client.InNamespace(namespace)); err != nil {
		r.Log.Error(err, "failed to list instances")
		return nil, err
	}
	return instances.Items, nil
}

// SetupWithManager starts the reconciler loop.
func (r *ConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Config{}).
		Complete(r)
}
