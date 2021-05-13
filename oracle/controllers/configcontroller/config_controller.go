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

	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
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
	findInstance = (*ConfigReconciler).findInstance
	patch        = (*ConfigReconciler).Patch
)

// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=configs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=configs/status,verbs=get;update;patch

// Reconcile looks for the config upload requests and populates
// Operator config with the customer requested values.
func (r *ConfigReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("Config", req.NamespacedName)

	log.Info("reconciling config requests")

	var config v1alpha1.Config
	if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
		log.V(1).Error(err, "get config request error")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if config.Spec.LogLevel != nil && config.Spec.LogLevel[controllers.OperatorName] != "" {
		flag.Set("v", config.Spec.LogLevel[controllers.OperatorName])
	} else {
		flag.Set("v", "0")
	}

	inst, err := findInstance(r, ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	// This is to allow creating a Config even when no Instances exist yet.
	if inst == nil {
		log.Info("no Instances found")
		return ctrl.Result{}, nil
	}
	log.V(1).Info("Instance found", "inst", inst)

	applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("instance-controller")}
	agentParam := controllers.AgentDeploymentParams{
		Inst:           inst,
		Scheme:         r.Scheme,
		Name:           fmt.Sprintf(controllers.AgentDeploymentName, inst.Name),
		Images:         r.Images,
		PrivEscalation: false,
		Log:            log,
		Args:           controllers.GetLogLevelArgs(&config),
	}

	agentDeployment, err := controllers.NewAgentDeployment(agentParam)
	if err != nil {
		log.Error(err, "failed to create a Deployment", "agent deployment", agentDeployment)
		return ctrl.Result{}, err
	}

	if err := patch(r, ctx, agentDeployment, client.Apply, applyOpts...); err != nil {
		log.Error(err, "failed to patch the Deployment", "agent deployment.Status", agentDeployment.Status)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// findInstance attempts to find a customer specific instance
// if it's been provided. There should be at most one instance.
func (r *ConfigReconciler) findInstance(ctx context.Context) (*v1alpha1.Instance, error) {
	var insts v1alpha1.InstanceList
	listOptions := []client.ListOption{}
	if err := r.List(ctx, &insts, listOptions...); err != nil {
		r.Log.Error(err, "failed to list instances")
		return nil, err
	}

	if len(insts.Items) == 0 {
		return nil, nil
	}

	if len(insts.Items) != 1 {
		return nil, fmt.Errorf("number of instances != 1, numInstances:%d", len(insts.Items))
	}

	return &insts.Items[0], nil
}

// SetupWithManager starts the reconciler loop.
func (r *ConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Config{}).
		Complete(r)
}
