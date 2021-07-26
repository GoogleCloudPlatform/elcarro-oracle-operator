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

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/databasecontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common/sql"
	capb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

func (r *InstanceReconciler) updateProgressCondition(ctx context.Context, inst v1alpha1.Instance, ns, op string, log logr.Logger) bool {
	iReadyCond := k8s.FindCondition(inst.Status.Conditions, k8s.Ready)

	log.Info("updateProgressCondition", "operation", op, "iReadyCond", iReadyCond)
	progress, err := r.statusProgress(ctx, ns, fmt.Sprintf(controllers.StsName, inst.Name), log)
	if err != nil && iReadyCond != nil {
		if progress > 0 {
			k8s.InstanceUpsertCondition(&inst.Status, iReadyCond.Type, iReadyCond.Status, iReadyCond.Reason, fmt.Sprintf("%s: %d%%", op, progress))
		}
		log.Info("updateProgressCondition", "statusProgress", err)
		return false
	}
	return true
}

// updateIsChangeApplied sets instance.Status.IsChangeApplied field to false if observedGeneration < generation, it sets it to true if changes are applied.
// TODO: add logic to handle restore/recovery
func (r *InstanceReconciler) updateIsChangeApplied(inst *v1alpha1.Instance, log logr.Logger) {
	if inst.Status.ObservedGeneration < inst.Generation {
		inst.Status.IsChangeApplied = v1.ConditionFalse
		inst.Status.ObservedGeneration = inst.Generation
		log.Info("change detected", "observedGeneration", inst.Status.ObservedGeneration, "generation", inst.Generation)
	}
	if inst.Status.IsChangeApplied == v1.ConditionTrue {
		return
	}
	parameterUpdateDone := inst.Spec.Parameters == nil || reflect.DeepEqual(inst.Status.CurrentParameters, inst.Spec.Parameters)
	if parameterUpdateDone {
		inst.Status.IsChangeApplied = v1.ConditionTrue
	}
	log.Info("change applied", "observedGeneration", inst.Status.ObservedGeneration, "generation", inst.Generation)
}

func (r *InstanceReconciler) createStatefulSet(ctx context.Context, inst *v1alpha1.Instance, sp controllers.StsParams, applyOpts []client.PatchOption, log logr.Logger) (ctrl.Result, error) {
	newPVCs, err := controllers.NewPVCs(sp)
	if err != nil {
		log.Error(err, "NewPVCs failed")
		return ctrl.Result{}, err
	}
	newPodTemplate := controllers.NewPodTemplate(sp, inst.Spec.CDBName, controllers.GetDBDomain(inst))
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
	return ctrl.Result{}, nil
}

func (r *InstanceReconciler) createAgentDeployment(ctx context.Context, inst v1alpha1.Instance, config *v1alpha1.Config, images map[string]string, enabledServices []commonv1alpha1.Service, applyOpts []client.PatchOption, log logr.Logger) (ctrl.Result, error) {
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
		log.Info("createAgentDeployment: error in function NewAgentDeployment")
		log.Error(err, "failed to create a Deployment", "agent deployment", agentDeployment)
		return ctrl.Result{}, err
	}
	log.Info("createAgentDeployment: function NewAgentDeployment succeeded")
	if err := r.Patch(ctx, agentDeployment, client.Apply, applyOpts...); err != nil {
		log.Error(err, "failed to patch the Deployment", "agent deployment.Status", agentDeployment.Status)
		return ctrl.Result{}, err
	}
	log.Info("createAgentDeployment: function Patch succeeded")
	if err := r.Status().Update(ctx, &inst); err != nil {
		log.Error(err, "failed to update an Instance status agent image returning error")
	}
	return ctrl.Result{}, nil
}

func (r *InstanceReconciler) createServices(ctx context.Context, inst v1alpha1.Instance, services []string, applyOpts []client.PatchOption) (*corev1.Service, *corev1.Service, error) {
	var svcLB *corev1.Service
	for _, s := range services {
		svc, err := controllers.NewSvc(&inst, r.Scheme, s)
		if err != nil {
			return nil, nil, nil
		}

		if err := r.Patch(ctx, svc, client.Apply, applyOpts...); err != nil {
			return nil, nil, nil
		}

		if s == "lb" {
			svcLB = svc
		}
	}

	svc, err := controllers.NewDBDaemonSvc(&inst, r.Scheme)
	if err != nil {
		return nil, nil, err
	}

	if err := r.Patch(ctx, svc, client.Apply, applyOpts...); err != nil {
		return nil, nil, err
	}

	svc, err = controllers.NewAgentSvc(&inst, r.Scheme)
	if err != nil {
		return nil, nil, err
	}

	if err := r.Patch(ctx, svc, client.Apply, applyOpts...); err != nil {
		return nil, nil, err
	}
	return svcLB, svc, nil
}

// isImageSeeded determines from the service image metadata file if the image is seeded or unseeded.
func (r *InstanceReconciler) isImageSeeded(ctx context.Context, inst *v1alpha1.Instance, log logr.Logger) (bool, error) {
	log.Info("isImageSeeded: new database requested", inst.GetName())
	caClient, closeConn, err := r.ClientFactory.New(ctx, r, inst.Namespace, inst.Name)
	if err != nil {
		log.Error(err, "failed to create config agent client")
		return false, err
	}
	defer closeConn()
	serviceImageMetaData, err := caClient.FetchServiceImageMetaData(ctx, &capb.FetchServiceImageMetaDataRequest{})
	if err != nil {
		return false, fmt.Errorf("isImageSeeded: failed on FetchServiceImageMetaData call: %v", err)
	}
	if serviceImageMetaData.CdbName == "" {
		return false, nil
	}
	return true, nil
}

func (r *InstanceReconciler) overrideDefaultImages(config *v1alpha1.Config, images map[string]string, inst *v1alpha1.Instance, log logr.Logger) error {
	if config != nil {
		log.V(1).Info("customer config loaded", "config", config)

		if config.Spec.Platform != "GCP" && config.Spec.Platform != "BareMetal" && config.Spec.Platform != "Minikube" && config.Spec.Platform != "Kind" {
			return fmt.Errorf("Unsupported platform: %q", config.Spec.Platform)
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
				log.Info("create instance: prep", "replacing", k, "image of", v2, "with instance specific", v1)
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
		return fmt.Errorf("overrideDefaultImages: CDBName isn't defined in the config")
	}
	if !serviceImageDefined {
		return fmt.Errorf("overrideDefaultImages: Service image isn't defined in the config")
	}
	return nil
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

// statusProgress tracks the progress of an ongoing instance creation and returns the progress in terms of percentage.
func (r *InstanceReconciler) statusProgress(ctx context.Context, ns, name string, log logr.Logger) (int, error) {
	var sts appsv1.StatefulSetList
	if err := r.List(ctx, &sts, client.InNamespace(ns)); err != nil {
		log.Error(err, "failed to get a list of StatefulSets to check status")
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
	log.Info("found the right StatefulSet", "foundSts", &foundSts.Name,
		"sts.Status.CurrentReplicas", &foundSts.Status.CurrentReplicas, "sts.Status.ReadyReplicas", foundSts.Status.ReadyReplicas)

	if foundSts.Status.CurrentReplicas != 1 {
		return 10, fmt.Errorf("StatefulSet is not ready yet? (failed to find the expected number of current replicas): %d", foundSts.Status.CurrentReplicas)
	}

	if foundSts.Status.ReadyReplicas != 1 {
		return 50, fmt.Errorf("StatefulSet is not ready yet? (failed to find the expected number of ready replicas): %d", foundSts.Status.ReadyReplicas)
	}

	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{"statefulset": name}); err != nil {
		log.Error(err, "failed to get a list of Pods to check status")
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
	log.Info("found the right Pod", "pod.Name", &foundPod.Name, "pod.Status", foundPod.Status.Phase, "#containers", len(foundPod.Status.ContainerStatuses))

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
