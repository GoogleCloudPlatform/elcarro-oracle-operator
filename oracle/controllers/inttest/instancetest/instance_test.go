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

package instancetest

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"

	// Enable GCP auth for k8s client
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

// Made global to be accessible by AfterSuite
var k8sEnv = testhelpers.K8sOperatorEnvironment{}

// In case of Ctrl-C clean up the last valid k8sEnv.
var _ = AfterSuite(func() {
	k8sEnv.Close()
})

var _ = Describe("Instance and Database provisioning", func() {
	var namespace string
	var instanceName string

	BeforeEach(func() {
		defer GinkgoRecover()
		namespace = testhelpers.RandName("instance-crd-test")
		instanceName = "mydb"
		k8sEnv.Init(namespace)
	})

	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			testhelpers.PrintSimpleDebugInfo(k8sEnv, instanceName, "mydb")
		}
		k8sEnv.Close()
	})

	TestInstanceCreationAndDatabaseProvisioning := func(version string, edition string) {
		It("Should create instance and provision database", func() {
			ctx := context.Background()
			k8sClient, err := client.New(k8sEnv.Env.Config, client.Options{})
			Expect(err).ToNot(HaveOccurred())

			By("By creating a new Instance")
			instance := &v1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      instanceName,
					Namespace: namespace,
				},
				Spec: v1alpha1.InstanceSpec{
					// Keep the CDBName in the spec different from the CDB name in the image (GCLOUD).
					// Doing this implicitly test the CDB renaming feature.
					CDBName: "mydb",
					InstanceSpec: commonv1alpha1.InstanceSpec{
						Version: version,
						Disks: []commonv1alpha1.DiskSpec{
							{
								Name: "DataDisk",
								Size: resource.MustParse("100Gi"),
							},
							{
								Name: "LogDisk",
								Size: resource.MustParse("150Gi"),
							},
						},
						MinMemoryForDBContainer: "7.0Gi",
						Images: map[string]string{
							"service": testhelpers.TestImageForVersion(version, edition, ""),
						},
					},
				},
			}
			testhelpers.K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, instance)

			createdInstance := &v1alpha1.Instance{}
			instKey := client.ObjectKey{Namespace: namespace, Name: instanceName}

			By("By checking that Instance is created")
			// Wait until the instance is "Ready" (requires 5+ minutes to download image)
			Eventually(func() metav1.ConditionStatus {
				Expect(k8sClient.Get(ctx, instKey, createdInstance)).Should(Succeed())
				cond := k8s.FindCondition(createdInstance.Status.Conditions, k8s.Ready)
				if cond != nil {
					return cond.Status
				}
				return metav1.ConditionUnknown
			}, 20*time.Minute, 5*time.Second).Should(Equal(metav1.ConditionTrue))

			By("By checking that Database is provisioned")
			Eventually(func() metav1.ConditionStatus {
				Expect(k8sClient.Get(ctx, instKey, createdInstance)).Should(Succeed())
				cond := k8s.FindCondition(createdInstance.Status.Conditions, k8s.DatabaseInstanceReady)
				if cond != nil {
					return cond.Status
				}
				return metav1.ConditionUnknown
			}, 20*time.Minute, 5*time.Second).Should(Equal(metav1.ConditionTrue))

			By("By checking that statefulset/deployment/svc are created")
			var sts appsv1.StatefulSetList
			Expect(k8sClient.List(ctx, &sts, client.InNamespace(namespace))).Should(Succeed())
			Expect(len(sts.Items)).Should(Equal(1))

			var deployment appsv1.DeploymentList
			Expect(k8sClient.List(ctx, &deployment, client.InNamespace(namespace))).Should(Succeed())
			Expect(len(deployment.Items)).Should(Equal(2))

			var svc corev1.ServiceList
			Expect(k8sClient.List(ctx, &svc, client.InNamespace(namespace))).Should(Succeed())
			Expect(len(svc.Items)).Should(Equal(4))
		})
	}

	Context("Oracle 12.2 EE", func() {
		TestInstanceCreationAndDatabaseProvisioning("12.2", "EE")
	})

	Context("Oracle 19.3 EE", func() {
		TestInstanceCreationAndDatabaseProvisioning("19.3", "EE")
	})

	Context("Oracle 18c XE", func() {
		TestInstanceCreationAndDatabaseProvisioning("18c", "XE")
	})
})

func TestInstance(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecsWithDefaultAndCustomReporters(t,
		t.Name(),
		[]Reporter{printer.NewlineReporter{}})
}
