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

package agentpatchingtest

import (
	"fmt"
	"testing"
	"time"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	// Enable GCP auth for k8s client
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
)

func TestAgentPatching(t *testing.T) {
	klog.SetOutput(GinkgoWriter)
	logf.SetLogger(klogr.NewWithOptions(klogr.WithFormat(klogr.FormatKlog)))
	RegisterFailHandler(Fail)
	RunSpecsWithDefaultAndCustomReporters(t,
		t.Name(),
		[]Reporter{printer.NewlineReporter{}})
}

// Global variable, to be accessible by AfterSuite
var k8sEnv = testhelpers.K8sOperatorEnvironment{}

// In case of Ctrl-C clean up the last valid k8sEnv.
var _ = AfterSuite(func() {
	k8sEnv.Close()
})

var _ = Describe("Agent Patching", func() {
	klog.SetOutput(GinkgoWriter)
	logf.SetLogger(klogr.NewWithOptions(klogr.WithFormat(klogr.FormatKlog)))
	instanceName := "mydb"
	v1Images := map[string]string{}
	v2Images := map[string]string{}

	BeforeEach(func() {
		defer GinkgoRecover()
		namespace := testhelpers.RandName("agentpatching-test")
		k8sEnv.Init(namespace, namespace)

		// Create a v2 copy of all images
		v1Images, v2Images = testhelpers.CreateV1V2Images(k8sEnv)
	})

	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			testhelpers.PrintLogs(k8sEnv.CPNamespace, k8sEnv.DPNamespace, k8sEnv.Env, []string{"manager", "dbdaemon", "oracledb"}, []string{instanceName})
			testhelpers.PrintClusterObjects()
		}
		k8sEnv.Close()
	})

	It("Test Normal Agent Patching Flow", func() {
		v2Images["service"] = testhelpers.TestImageForVersion("19.3", "EE", "")

		prepareInstanceAndPatchAgents(instanceName, v2Images)

		instKey := client.ObjectKey{Namespace: k8sEnv.DPNamespace, Name: instanceName}

		// Wait for PatchingBackupStarted
		testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionFalse, k8s.PatchingBackupStarted, 45*time.Second)

		// Wait until the instance is "Ready" again
		testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 15*time.Minute)

		// Check that test data is still there
		testhelpers.VerifySimpleData(k8sEnv)

		// Check other agent images in the instance STS
		images := getPodImages("instance=" + instanceName + ",task-type=" + controllers.DatabaseTaskType)
		Expect(images["init:dbinit"]).To(Equal(v2Images["dbinit"]))
		Expect(images["listener-log-sidecar"]).To(Equal(v2Images["logging_sidecar"]))
	})

	It("Test Faulty DbInit Patching Flow", func() {
		// Update only dbinit, with broken url
		newImages := map[string]string{
			"dbinit":          "faulty_url",
			"logging_sidecar": v1Images["logging_sidecar"],
			"monitoring":      v1Images["monitoring"],
			"operator":        v1Images["operator"],
			"service":         testhelpers.TestImageForVersion("19.3", "EE", ""),
		}

		prepareInstanceAndPatchAgents(instanceName, newImages)

		instKey := client.ObjectKey{Namespace: k8sEnv.DPNamespace, Name: instanceName}

		// Wait for PatchingBackupStarted
		testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionFalse, k8s.PatchingBackupStarted, 45*time.Second)

		// Wait for StatefulSetPatchingInProgress
		testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionFalse, k8s.StatefulSetPatchingInProgress, 15*time.Minute)

		// Wait for PatchingRecoveryCompleted (set the patching timeout to 5 mins)
		createdInstance := &v1alpha1.Instance{}
		testhelpers.K8sUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
			instKey,
			createdInstance,
			func(obj *client.Object) {
				instanceToUpdate := (*obj).(*v1alpha1.Instance)
				instanceToUpdate.Spec.DatabasePatchingTimeout = &metav1.Duration{Duration: time.Minute * 5}
			})

		testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.PatchingRecoveryCompleted, 20*time.Minute)
	})
})

func prepareInstanceAndPatchAgents(instanceName string, newImages map[string]string) {
	testhelpers.CreateSimpleInstance(k8sEnv, instanceName, "19.3", "EE")

	instKey := client.ObjectKey{Namespace: k8sEnv.DPNamespace, Name: instanceName}
	testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, 20*time.Minute)

	testhelpers.CreateSimplePDB(k8sEnv, instanceName)
	testhelpers.InsertSimpleData(k8sEnv)

	By("DB is ready, starting agent patching")

	// Start applying patching spec
	createdInstance := &v1alpha1.Instance{}

	testhelpers.K8sUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
		instKey,
		createdInstance,
		func(obj *client.Object) {
			instanceToUpdate := (*obj).(*v1alpha1.Instance)
			oneHourBefore := metav1.NewTime(time.Now().Add(-1 * time.Hour))
			twoHours := metav1.Duration{Duration: 2 * time.Hour}
			instanceToUpdate.Spec.MaintenanceWindow = &commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{{Start: &oneHourBefore, Duration: &twoHours}}}
			instanceToUpdate.Spec.Images = newImages
			instanceToUpdate.Spec.Services = map[commonv1alpha1.Service]bool{"Patching": true}
		})
}

func getPodImages(filter string) map[string]string {
	clientSet, err := kubernetes.NewForConfig(k8sEnv.Env.Config)
	Expect(err).ShouldNot(HaveOccurred())
	pod, err := testhelpers.FindPodFor(k8sEnv.Ctx, clientSet, k8sEnv.DPNamespace, filter)
	Expect(err).ShouldNot(HaveOccurred())

	By(fmt.Sprintf("%v", pod.Status.ContainerStatuses))
	images := map[string]string{}
	for _, cs := range pod.Status.ContainerStatuses {
		images[cs.Name] = cs.Image
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		images["init:"+cs.Name] = cs.Image
	}
	return images
}
