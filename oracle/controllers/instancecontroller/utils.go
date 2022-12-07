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
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/pkg/utils"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common/sql"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/security"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
	"github.com/google/go-cmp/cmp"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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
	if iReadyCond != nil && progress > 0 {
		k8s.InstanceUpsertCondition(&inst.Status, iReadyCond.Type, iReadyCond.Status, iReadyCond.Reason, fmt.Sprintf("%s: %d%%", op, progress))
		log.Info("updateProgressCondition", "statusProgress", err)
	}
	return err == nil
}

// updateIsChangeApplied sets instance.Status.IsChangeApplied field to false if observedGeneration < generation, it sets it to true if changes are applied.
// TODO: add logic to handle restore/recovery
func (r *InstanceReconciler) updateIsChangeApplied(inst *v1alpha1.Instance, log logr.Logger) {
	if inst.Status.ObservedGeneration < inst.Generation {
		log.Info("change detected", "observedGeneration", inst.Status.ObservedGeneration, "generation", inst.Generation)
		inst.Status.IsChangeApplied = v1.ConditionFalse
		inst.Status.ObservedGeneration = inst.Generation
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

	baseSTS := &appsv1.StatefulSet{}
	sts.DeepCopyInto(baseSTS)
	if _, err := ctrl.CreateOrUpdate(ctx, r, baseSTS, func() error {
		sts.Spec.DeepCopyInto(&baseSTS.Spec)
		return nil
	}); err != nil {
		log.Error(err, "failed to create the StatefulSet", "sts.Status", sts.Status)
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *InstanceReconciler) reconcileMonitoring(ctx context.Context, inst *v1alpha1.Instance, log logr.Logger, images map[string]string) (ctrl.Result, error) {
	requeueDuration := 0 * time.Second

	deploymentName := fmt.Sprintf("%s-monitor", inst.Name)
	monitoringUserSecretName := fmt.Sprintf("%s-secret", deploymentName)
	monitoringUser := "gcsql$monitor"
	monitoringSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: inst.Namespace,
			Name:      monitoringUserSecretName,
		},
	}

	if result, err := ctrl.CreateOrUpdate(ctx, r.Client, monitoringSecret, func() error {
		if err := ctrlutil.SetOwnerReference(monitoringSecret, inst, r.Scheme()); err != nil {
			return err
		}
		if monitoringSecret.Data == nil {
			monitoringSecret.Data = make(map[string][]byte)
		}
		if len(monitoringSecret.Data["username"]) == 0 {
			monitoringSecret.Data["username"] = []byte(monitoringUser)
		}
		if len(monitoringSecret.Data["password"]) == 0 {
			monitoringPass, _ := security.RandOraclePassword()
			monitoringSecret.Data["password"] = []byte(monitoringPass)
		}
		return nil
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("creating monitoring secret %s/%s: %w", monitoringSecret.Namespace, monitoringSecret.Name, err)
	} else if result != ctrlutil.OperationResultNone {
		// Wait until we are sure the secret is reconciled to create the user.
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	dbdClient, closeConn, err := r.DatabaseClientFactory.New(ctx, r, inst.GetNamespace(), inst.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer closeConn()

	// Only if user doesnt exist.
	// Create cdb user with access to all pdb.
	resp, err := dbdClient.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{fmt.Sprintf("select username from dba_users where username='%s'", strings.ToUpper(monitoringUser))},
	})

	if err == nil && len(resp.GetMsg()) < 1 {
		if _, err := dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
			Commands: []string{
				fmt.Sprintf("create user %s identified by %s", monitoringUser, string(monitoringSecret.Data["password"])),
				fmt.Sprintf("grant %s to %s container=all", "connect, select any dictionary", monitoringUser),
				fmt.Sprintf("alter user %s set container_data=all container=current", monitoringUser),
			},
			Suppress: true,
		}); err != nil {
			log.Error(err, "Creating the monitoring user failed")
			requeueDuration = 30 * time.Second
		}
	} else if err != nil {
		// Wait for the database to be available
		requeueDuration = 30 * time.Second
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: inst.Namespace,
			Name:      deploymentName,
		},
	}
	if _, err := ctrl.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		if err := ctrlutil.SetOwnerReference(deployment, inst, r.Scheme()); err != nil {
			return err
		}
		one := int32(1)
		matchLabels := map[string]string{"instance": inst.Name, "task-type": controllers.MonitorTaskType}
		deployment.Spec = appsv1.DeploymentSpec{
			Replicas: &one,
			// Must match the agent deployment.
			Selector: &metav1.LabelSelector{
				MatchLabels: matchLabels,
			},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType},
			Template: controllers.MonitoringPodTemplate(inst, monitoringSecret, images),
		}
		deployment.Spec.Template.Labels = matchLabels
		return nil
	}); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: requeueDuration}, nil
}

// CreateDBLoadBalancer returns the service for the database.
func (r *InstanceReconciler) createDBLoadBalancer(ctx context.Context, inst *v1alpha1.Instance, applyOpts []client.PatchOption) (*corev1.Service, error) {
	sourceCidrRanges := []string{"0.0.0.0/0"}
	if len(inst.Spec.SourceCidrRanges) > 0 {
		sourceCidrRanges = inst.Spec.SourceCidrRanges
	}
	var svcAnnotations map[string]string

	lbType := corev1.ServiceTypeLoadBalancer
	svcNameFull := fmt.Sprintf(controllers.SvcName, inst.Name)
	svcAnnotations = utils.LoadBalancerAnnotations(inst.Spec.DBLoadBalancerOptions)

	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: svcNameFull, Namespace: inst.Namespace, Annotations: svcAnnotations},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"instance":  inst.Name,
				"task-type": controllers.DatabaseTaskType,
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "secure-listener",
					Protocol:   "TCP",
					Port:       consts.SecureListenerPort,
					TargetPort: intstr.FromInt(consts.SecureListenerPort),
				},
				{
					Name:       "ssl-listener",
					Protocol:   "TCP",
					Port:       consts.SSLListenerPort,
					TargetPort: intstr.FromInt(consts.SSLListenerPort),
				},
			},
			Type:                     lbType,
			LoadBalancerIP:           utils.LoadBalancerIpAddress(inst.Spec.DBLoadBalancerOptions),
			LoadBalancerSourceRanges: sourceCidrRanges,
		},
	}

	// Set the Instance resource to own the Service resource.
	if err := ctrl.SetControllerReference(inst, svc, r.Scheme()); err != nil {
		return nil, err
	}

	if err := r.Patch(ctx, svc, client.Apply, applyOpts...); err != nil {
		return nil, err
	}

	return svc, nil
}

func (r *InstanceReconciler) createDataplaneServices(ctx context.Context, inst v1alpha1.Instance, applyOpts []client.PatchOption) (dbDaemonSvc *corev1.Service, agentSvc *corev1.Service, err error) {
	dbDaemonSvc, err = controllers.NewDBDaemonSvc(&inst, r.Scheme())
	if err != nil {
		return nil, nil, err
	}

	if err := r.Patch(ctx, dbDaemonSvc, client.Apply, applyOpts...); err != nil {
		return nil, nil, err
	}

	agentSvc, err = controllers.NewAgentSvc(&inst, r.Scheme())
	if err != nil {
		return nil, nil, err
	} else if agentSvc == nil {
		return dbDaemonSvc, nil, nil
	}

	if agentSvc.Spec.Ports == nil {
		return dbDaemonSvc, agentSvc, nil
	}
	if err := r.Patch(ctx, agentSvc, client.Apply, applyOpts...); err != nil {
		return nil, nil, err
	}
	return dbDaemonSvc, agentSvc, nil
}

// isImageSeeded determines from the service image metadata file if the image is seeded or unseeded.
func (r *InstanceReconciler) isImageSeeded(ctx context.Context, inst *v1alpha1.Instance, log logr.Logger) (bool, error) {
	log.Info("isImageSeeded: requesting image metadata...", "instance", inst.GetName())
	dbClient, closeConn, err := r.DatabaseClientFactory.New(ctx, r, inst.GetNamespace(), inst.GetName())

	if err != nil {
		log.Error(err, "failed to create database client")
		return false, err
	}
	defer closeConn()
	serviceImageMetaData, err := dbClient.FetchServiceImageMetaData(ctx, &dbdpb.FetchServiceImageMetaDataRequest{})
	if err != nil {
		return false, fmt.Errorf("isImageSeeded: failed on FetchServiceImageMetaData call: %v", err)
	}
	if !serviceImageMetaData.SeededImage {
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

	for _, podCondition := range foundPod.Status.Conditions {
		if podCondition.Type == "Ready" && podCondition.Status == "False" {
			log.Info("statusProgress: podCondition.Type ready is False")
			return 85, fmt.Errorf("failed to find the right Pod %s in status Running: %s", name+"-0", foundPod.Status.Phase)
		}
		if podCondition.Type == "ContainersReady" && podCondition.Status == "False" {
			msg := "statusProgress: podCondition.Type ContainersReady is False"
			log.Info(msg)
			return 85, fmt.Errorf(msg)
		}
	}

	for _, c := range foundPod.Status.ContainerStatuses {
		if !c.Ready {
			msg := fmt.Sprintf("container %s is not ready", c.Name)
			log.Info(msg)
			return 85, fmt.Errorf(msg)
		}
		log.Info("container %s is ready", c.Name)
	}
	for _, c := range foundPod.Status.InitContainerStatuses {
		if !c.Ready {
			msg := fmt.Sprintf("init container %s is not ready", c.Name)
			log.Info(msg)
			return 85, fmt.Errorf(msg)
		}
		msg := fmt.Sprintf("container %s is ready", c.Name)
		log.Info(msg)
	}

	log.Info("Stateful set creation is complete")
	return 100, nil
}

func IsPatchingStateMachineEntryCondition(enabledServices map[commonv1alpha1.Service]bool, activeImages map[string]string, spImages map[string]string, lastFailedImages map[string]string, instanceReadyCond *v1.Condition, dbInstanceCond *v1.Condition) bool {
	if !(enabledServices[commonv1alpha1.Patching] &&
		instanceReadyCond != nil &&
		dbInstanceCond != nil &&
		k8s.ConditionStatusEquals(instanceReadyCond, v1.ConditionTrue)) {
		return false
	}
	if !reflect.DeepEqual(activeImages, spImages) && (lastFailedImages == nil || !reflect.DeepEqual(lastFailedImages, spImages)) {
		return true
	}
	return false
}

func (r *InstanceReconciler) isOracleUpAndRunning(ctx context.Context, inst *v1alpha1.Instance, namespace string, log logr.Logger) (bool, error) {
	status, err := CheckStatusInstanceFunc(ctx, r, r.DatabaseClientFactory, inst.Name, inst.Spec.CDBName, inst.Namespace, "", controllers.GetDBDomain(inst), log)
	if err != nil {
		log.Info("dbdaemon startup still in progress, waiting")
		return false, nil
	}
	if status != controllers.StatusReady {
		log.Info("Oracle startup still in progress, waiting")
		return false, nil
	}
	return true, nil
}

func (r *InstanceReconciler) updateDatabaseIncarnationStatus(ctx context.Context, inst *v1alpha1.Instance, log logr.Logger) error {
	incResp, err := controllers.FetchDatabaseIncarnation(ctx, r, r.DatabaseClientFactory, inst.Namespace, inst.Name)
	if err != nil {
		return fmt.Errorf("failed to fetch current database incarnation: %v", err)
	}

	if inst.Status.CurrentDatabaseIncarnation != incResp.Incarnation {
		inst.Status.LastDatabaseIncarnation = inst.Status.CurrentDatabaseIncarnation
	}
	inst.Status.CurrentDatabaseIncarnation = incResp.Incarnation
	return nil
}

func CloneMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

// Initiate a config_agent_helpers ApplyDataPatch() call
// Create an LRO job "DatabasePatch_%s", instance.GetUID()
// making the method idempotent (per instance)
// Return err on failure, nil on success
func (r *InstanceReconciler) startDatabasePatching(req ctrl.Request, ctx context.Context, inst v1alpha1.Instance, log logr.Logger) error {
	log.Info("startDatabasePatching initiated")

	// Call async ApplyDataPatch
	log.Info("config_agent_helpers.ApplyDataPatch", "LRO", lroPatchingOperationID(inst))
	resp, err := controllers.ApplyDataPatch(ctx, r, r.DatabaseClientFactory, inst.Namespace, inst.Name, controllers.ApplyDataPatchRequest{
		LroInput: &controllers.LROInput{OperationId: lroPatchingOperationID(inst)},
	})
	if err != nil {
		return fmt.Errorf("failed on ApplyDataPatch gRPC call: %w", err)
	}
	log.Info("config_agent_helpers.ApplyDataPatch", "response", resp)
	return nil
}

// Check for patching LRO job status
// Return (true, nil) if job is done
// Return (false, nil) if job still in progress
// Return (false, err) if the job failed
func (r *InstanceReconciler) isDatabasePatchingDone(ctx context.Context, req ctrl.Request,
	inst v1alpha1.Instance, log logr.Logger) (bool, error) {

	// Get operation id
	id := lroPatchingOperationID(inst)
	operation, err := controllers.GetLROOperation(ctx, r.DatabaseClientFactory, r, id, req.Namespace, inst.Name)
	if err != nil {
		log.Info("GetLROOperation returned error", "error", err)
		return false, nil
	}
	log.Info("GetLROOperation", "response", operation)
	if !operation.Done {
		// Still waiting
		return false, nil
	}

	log.Info("LRO is DONE", "id", id)
	if err := controllers.DeleteLROOperation(ctx, r.DatabaseClientFactory, r, id, req.Namespace, inst.Name); err != nil {
		return false, fmt.Errorf("DeleteLROOperation returned an error: %w", err)
	}

	// remote LRO completed unsuccessfully
	if operation.GetError() != nil {
		return false, fmt.Errorf("config_agent.ApplyDataPatch() failed: %v", operation.GetError())
	}
	return true, nil
}

func lroPatchingOperationID(instance v1alpha1.Instance) string {
	return fmt.Sprintf("DatabasePatch_%s", instance.GetUID())
}

// AcquireInstanceMaintenanceLock gives caller an exclusive maintenance
// access to the specified instance object.
// 'inst' points to an existing instance object (will be updated after the call)
// 'owner' identifies the owning controller e.g. 'instancecontroller'
// Convention:
// If the call succeeds the caller can safely assume
// that it has exclusive access now.
// If the call fails the caller needs to retry acquiring the lock.
//
// Function is idempotent, caller can acquire the lock multiple times.
//
// Note: The call will commit the instance object to k8s (with all changes),
// updating the supplied 'inst' object and making all other
// references stale.
func AcquireInstanceMaintenanceLock(ctx context.Context, k8sClient client.Client, inst *v1alpha1.Instance, owner string) error {
	var result error = nil
	if inst.Status.LockedByController == "" {
		inst.Status.LockedByController = owner
		result = nil
	} else if inst.Status.LockedByController == owner {
		result = nil
	} else {
		result = fmt.Errorf("requested owner: %s, instance already locked by %v", owner, inst.Status.LockedByController)
	}
	// Will return an error if 'inst' is stale.
	if err := k8sClient.Status().Update(ctx, inst); err != nil {
		return fmt.Errorf("requested owner: %s, failed to update the instance status: %v", owner, err)
	}
	return result
}

// ReleaseInstanceMaintenanceLock releases exclusive maintenance
// access to the specified instance object.
// 'inst' points to an existing instance object (will be updated after the call)
// 'owner' identifies the owning controller e.g. 'instancecontroller'
// Convention:
// If the call succeeds the caller can safely assume
// that lock was released.
// If the call fails the caller needs to retry releasing the lock.
//
// Call is idempotent, caller can release it multiple times.
// If caller's not owning the lock the call will return success
// without affecting the ownership.
//
// Note: The call will commit the instance object to k8s (with all changes),
// updating the supplied 'inst' object and making all other
// references stale.
func ReleaseInstanceMaintenanceLock(ctx context.Context, k8sClient client.Client, inst *v1alpha1.Instance, owner string) error {
	var result error = nil
	if inst.Status.LockedByController == "" {
		result = nil
	} else if inst.Status.LockedByController == owner {
		inst.Status.LockedByController = ""
		result = nil
	} else {
		// Return success even if it's owned by someone else
		result = nil
	}
	// Will return an error if 'inst' is stale.
	if err := k8sClient.Status().Update(ctx, inst); err != nil {
		return fmt.Errorf("requested owner: %s, failed to update the instance status: %v", owner, err)
	}
	return result
}

func (r *InstanceReconciler) handleResize(ctx context.Context, inst *v1alpha1.Instance, instanceReadyCond *v1.Condition, dbInstanceCond *v1.Condition, sp controllers.StsParams, applyOpts []client.PatchOption, log logr.Logger) (ctrl.Result, error) {

	if !k8s.ConditionStatusEquals(instanceReadyCond, v1.ConditionTrue) && !k8s.ConditionStatusEquals(dbInstanceCond, v1.ConditionTrue) && !k8s.ConditionReasonEquals(instanceReadyCond, k8s.ResizingInProgress) {
		return ctrl.Result{}, nil
	}
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sp.StsName,
			Namespace: inst.Namespace,
		},
	}

	if err := r.Get(ctx, client.ObjectKeyFromObject(sts), sts); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("failed to get statefulset: %v", err)
	} else if apierrors.IsNotFound(err) {
		log.Info("Recreating Stateful set")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.ResizingInProgress, "Recreating statefulset")
		if _, err := r.createStatefulSet(ctx, inst, sp, applyOpts, log); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	dbContainer := findContainer(sts.Spec.Template.Spec.Containers, controllers.DatabaseContainerName)
	if dbContainer == nil {
		return ctrl.Result{}, fmt.Errorf("could not find database container in pod template")
	}

	// CPU/Memory resize
	if !cmp.Equal(inst.Spec.DatabaseResources, dbContainer.Resources) {
		log.Info("Instance CPU/MEM resize required")
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.ResizingInProgress, "Resizing cpu/memory")

		_, err := ctrl.CreateOrUpdate(ctx, r.Client, sts, func() error {
			dbContainer := findContainer(sts.Spec.Template.Spec.Containers, controllers.DatabaseContainerName)
			if dbContainer == nil {
				return fmt.Errorf("could not find database container in pod temmplate")
			}
			dbContainer.Resources = inst.Spec.DatabaseResources
			return nil
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update statefulset resources: %v", err)
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	newSts, err := r.buildStatefulSet(ctx, inst, sp, nil, log)
	if err != nil {
		return ctrl.Result{}, err
	}
	done, err := tryResizeDisksOf(ctx, r.Client, newSts, log)
	if err != nil {
		return ctrl.Result{}, err
	} else if !done {
		k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionFalse, k8s.ResizingInProgress, "Resizing disk")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if k8s.ConditionReasonEquals(instanceReadyCond, k8s.ResizingInProgress) {
		ready, msg := IsReadyWithObj(sts)
		if ready && cmp.Equal(inst.Spec.DatabaseResources, dbContainer.Resources) {
			k8s.InstanceUpsertCondition(&inst.Status, k8s.Ready, v1.ConditionTrue, k8s.CreateComplete, msg)
			return ctrl.Result{Requeue: true}, nil
		}

		if err := utils.VerifyPodsStatus(ctx, r.Client, sts); errors.Is(err, utils.ErrPodUnschedulable) {
			return ctrl.Result{}, fmt.Errorf("Unschedulable pod %v", err)
		} else if errors.Is(err, utils.ErrNoResources) {
			return ctrl.Result{}, fmt.Errorf("Insufficient Resources %v", err)
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func tryResizeDisksOf(ctx context.Context, c client.Client, newSts *appsv1.StatefulSet, log logr.Logger) (bool, error) {
	oldSts := &appsv1.StatefulSet{}
	key := client.ObjectKeyFromObject(newSts)

	if err := c.Get(ctx, key, oldSts); err != nil {
		if apierrors.IsNotFound(err) {
			// no existing sts, we only delete sts after all PVCs has been resized,
			// so this is either a new sts or PVC has been resized.
			// Still return false here so that the request is requeued and sts is recreated in the following cycle.s
			return false, nil
		}
		return false,
			fmt.Errorf("error getting statefulset [%v]: %v", key, err)
	}

	if !oldSts.DeletionTimestamp.IsZero() {
		// sts has been requested to delete, let's wait for it to be completely gone
		return false, nil
	}

	changedDisks := FilterDiskWithSizeChanged(
		oldSts.Spec.VolumeClaimTemplates,
		newSts.Spec.VolumeClaimTemplates,
		log,
	)

	if len(changedDisks) == 0 {
		// no disk has size changed
		return true, nil
	}

	// sanity check: all sc can expand
	if err := PvcsCanBeExpanded(ctx, c, newSts, changedDisks); err != nil {
		return false,
			fmt.Errorf("pvcs may not be expanded")
	}
	// actually do the update
	done, err := resizePvcs(ctx, c, newSts, changedDisks, log)
	if err != nil {
		return false, err
	}

	if !done {
		return false, nil
	}

	// resize done, now let's delete the sts
	if err := c.Delete(ctx, oldSts); err != nil {
		return false,
			fmt.Errorf("error while deleting sts [%v]: %v", key, err)
	}
	return false, nil

}

// resizePvcs resizes the provided PVC templates, and return true if resize has
// completed; false if it's done but PVC still undergoing resizing; or error if
// and error occured during the resizing process
func resizePvcs(ctx context.Context, c client.Client, sts *appsv1.StatefulSet, disks []*corev1.PersistentVolumeClaim, log logr.Logger,
) (bool, error) {
	done := true
	var requested []string
	var completed []string

	for i := 0; i < int(*sts.Spec.Replicas); i++ {
		for _, pvc := range disks {

			key := utils.ObjectKeyOf(sts, pvc, i)
			newSize := *pvc.Spec.Resources.Requests.Storage()

			pvc := &corev1.PersistentVolumeClaim{}
			if err := c.Get(ctx, key, pvc); err != nil {
				return false,
					fmt.Errorf("error getting pvc [%v]: %v", key, err)
			}

			if newSize.Equal(*pvc.Spec.Resources.Requests.Storage()) {
				// spec has been updated before, check status
				if !newSize.Equal(*pvc.Status.Capacity.Storage()) {
					// status is not equal, meaning the resizing is still in-progress
					done = false
				} else {
					completed = append(completed, key.String())
				}
			} else {
				// spec is not updated yet, update it
				done = false
				oldCliObj := pvc.DeepCopyObject().(client.Object)
				pvc.Spec.Resources.Requests[corev1.ResourceStorage] = newSize
				if err := Patch(ctx, c, pvc, oldCliObj); err != nil {
					return false,
						fmt.Errorf("error resizing pvc [%v]: %v", key, err)
				} else {
					requested = append(requested, key.String())
				}
			}
		}
	}

	if len(requested) != 0 || len(completed) != 0 {
		log.Info(
			fmt.Sprintf(
				"PVC resizing, requested [%v], completed [%v]",
				strings.Join(requested, ", "),
				strings.Join(completed, ", ")))
	}

	return done, nil
}

// IsReadyWithObj returns true if the statefulset has a non-zero number of
// desired replicas and has the same number of actual pods running and ready.
func IsReadyWithObj(sts *appsv1.StatefulSet) (ready bool, msg string) {
	var want int32
	if sts.Spec.Replicas != nil {
		want = *sts.Spec.Replicas
	}
	have := sts.Status.ReadyReplicas
	if want == have {
		if want == 0 {
			return false, fmt.Sprintf("Statefulset %s/%s has zero replicas", sts.Namespace, sts.Name)
		}
		return true, fmt.Sprintf("Statefulset %s/%s is running", sts.Namespace, sts.Name)
	}
	return false, fmt.Sprintf("StatefulSet is not ready (current replicas: %d expected replicas: %d)", have, want)
}

func (r *InstanceReconciler) buildStatefulSet(ctx context.Context, inst *v1alpha1.Instance, sp controllers.StsParams, applyOpts []client.PatchOption, log logr.Logger) (*appsv1.StatefulSet, error) {

	newPVCs, err := controllers.NewPVCs(sp)
	if err != nil {
		log.Error(err, "NewPVCs failed")
		return nil, err
	}
	newPodTemplate := controllers.NewPodTemplate(sp, inst.Spec.CDBName, controllers.GetDBDomain(inst))
	sts, err := controllers.NewSts(sp, newPVCs, newPodTemplate)
	if err != nil {
		log.Error(err, "failed to create a StatefulSet", "sts", sts)
		return nil, err
	}
	log.Info("StatefulSet constructed", "sts", sts, "sts.Status", sts.Status, "inst.Status", inst.Status)
	return sts, nil
}

func findContainer(containers []corev1.Container, name string) *corev1.Container {
	for i, c := range containers {
		if c.Name == name {
			return &containers[i]
		}
	}
	return nil
}

// FilterDiskWithSizeChanged compare an old STS to a new STS and identify volumes that changed from old to new.
func FilterDiskWithSizeChanged(old, new []corev1.PersistentVolumeClaim, log logr.Logger) []*corev1.PersistentVolumeClaim {
	oldDisks := make(map[string]*resource.Quantity)
	changedDisks := make([]*corev1.PersistentVolumeClaim, 0, len(new))

	for _, c := range old {
		oldDisks[c.GetName()] = c.Spec.Resources.Requests.Storage()
	}

	sb := strings.Builder{}
	sb.WriteString("Detected disks with new size: ")

	for i, c := range new {
		newSize := c.Spec.Resources.Requests.Storage()
		if oldSize, ok := oldDisks[c.GetName()]; ok && !oldSize.Equal(*newSize) {
			sb.WriteString(fmt.Sprintf("[%v:%v->%v]", c.GetName(), oldSize.String(), newSize.String()))
			changedDisks = append(changedDisks, &new[i])
		}
	}

	if len(changedDisks) != 0 {
		log.Info(sb.String())
	}

	return changedDisks
}

// pvcsCanBeExpanded checks all the pvcs has a storage class that can be expanded, and return an error if any one PVC
// cannot be expanded.
func PvcsCanBeExpanded(ctx context.Context, r client.Reader, sts *appsv1.StatefulSet,
	pvcs []*corev1.PersistentVolumeClaim,
) error {
	for i := 0; i < int(*sts.Spec.Replicas); i++ {
		for _, pvc := range pvcs {

			// Need to get the actual PVC from kubernetes since empty storage class while creating could mean either manually
			// provisioned or default storage class.
			key := utils.ObjectKeyOf(sts, pvc, i)

			if err := CheckSinglePvc(ctx, r, key); err != nil {
				return err
			}

		}
	}

	return nil
}

// check pvc spec
func CheckSinglePvc(ctx context.Context, r client.Reader, key client.ObjectKey) error {
	tpvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, key, tpvc); err != nil {
		return fmt.Errorf("error getting pvc [%v]: %v", key, err)
	}

	if tpvc.Spec.StorageClassName == nil || *tpvc.Spec.StorageClassName == "" {
		return fmt.Errorf("cannot resize manually provisioned pvc [%v]", key)
	}

	scName := *tpvc.Spec.StorageClassName
	sc := &storagev1.StorageClass{}
	if err := r.Get(ctx, client.ObjectKey{Name: scName}, sc); err != nil {
		return fmt.Errorf("error getting storageclass [%v]: %v", scName, err)
	}

	if sc.AllowVolumeExpansion == nil || !*sc.AllowVolumeExpansion {
		return fmt.Errorf("storageclass [%v] does not allow expansion for volume [%v]", scName, tpvc.GetName())
	}
	return nil
}

// Patch attempts to patch the given object.
func Patch(ctx context.Context, cli client.Client, newCliObj client.Object, oldCliObj client.Object) error {
	// Make sure we're comparing objects with the same resourceVersion so that
	// the patch data (i.e., the diff) doesn't include a resourceVersion.
	// Otherwise, we could get a "the object has been modified; please apply
	// your changes to the latest version and try again" error if the
	// resourceVersion we're using is out of date.
	oldCliObj.SetResourceVersion(newCliObj.GetResourceVersion())

	patch := client.MergeFrom(oldCliObj)

	// The client.Patch function will make a patch request even if the patch
	// data is empty. So we'll check to see if a request is needed first to
	// avoid making empty requests.
	specOrMetaChanged, statusChanged, err := isObjectChanged(ctx, patch, newCliObj)
	if err != nil {
		return err
	}

	if specOrMetaChanged {
		newCloned := newCliObj
		if statusChanged {
			// Patch() will change the object passed in. We'll pass in a cloned
			// object so we still have the original for the status update below.
			newCloned = newCliObj.DeepCopyObject().(client.Object)
		}
		if err := cli.Patch(ctx, newCloned, patch); err != nil {
			return err
		}
	}
	if statusChanged {
		if err := cli.Status().Patch(ctx, newCliObj, patch); err != nil {
			return err
		}
	}

	// Replace h.old with the updated object, otherwise subsequent calls to
	// Patch() will be comparing the changes with an outdated version of the
	// object
	oldCliObj = newCliObj.DeepCopyObject().(client.Object)

	return nil
}

func isObjectChanged(ctx context.Context, patch client.Patch, obj client.Object) (specOrMetaChanged, statusChanged bool, err error) {
	data, err := patch.Data(obj)
	if err != nil {
		return false, false, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return false, false, err
	}

	_, statusChanged = result["status"]
	specOrMetaChanged = len(result) > 0 && !(len(result) == 1 && statusChanged)
	return specOrMetaChanged, statusChanged, nil
}
