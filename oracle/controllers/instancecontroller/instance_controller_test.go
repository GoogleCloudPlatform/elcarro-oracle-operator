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
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

var (
	k8sClient  client.Client
	k8sManager ctrl.Manager
	images     = map[string]string{
		"dbinit":          "dbInitImage",
		"service":         "serviceImage",
		"config":          "configAgentImage",
		"logging_sidecar": "loggingSidecarImage",
	}
	reconciler        *InstanceReconciler
	fakeClientFactory *testhelpers.FakeClientFactory

	fakeDatabaseClientFactory *testhelpers.FakeDatabaseClientFactory
)

func TestInstanceController(t *testing.T) {

	// Mock functions
	CheckStatusInstanceFunc = func(ctx context.Context, instName, cdbName, clusterIP, DBDomain string, log logr.Logger) (string, error) {
		return "Ready", nil
	}

	fakeClientFactory = &testhelpers.FakeClientFactory{}
	fakeDatabaseClientFactory = &testhelpers.FakeDatabaseClientFactory{}

	testhelpers.RunReconcilerTestSuite(t, &k8sClient, &k8sManager, "Instance controller", func() []testhelpers.Reconciler {
		reconciler = &InstanceReconciler{
			Client: k8sManager.GetClient(),
			Log:    ctrl.Log.WithName("controllers").WithName("Instance"),
			Scheme: k8sManager.GetScheme(),
			// We need a clone of 'images' to avoid race conditions between reconciler
			// goroutine and the test goroutine.
			Images:        CloneMap(images),
			ClientFactory: fakeClientFactory,
			Recorder:      k8sManager.GetEventRecorderFor("instance-controller"),

			DatabaseClientFactory: fakeDatabaseClientFactory,
		}

		return []testhelpers.Reconciler{reconciler}
	})
}

var _ = Describe("Instance controller", func() {

	BeforeEach(func() {
		fakeClientFactory.Reset()

		fakeDatabaseClientFactory.Reset()
		fakeDatabaseClientFactory.Dbclient.SetMethodToResp(
			"FetchServiceImageMetaData", &dbdpb.FetchServiceImageMetaDataResponse{
				Version:     "12.2",
				CdbName:     "GCLOUD",
				OracleHome:  "/u01/app/oracle/product/12.2/db",
				SeededImage: true,
			})
	})

	Context("New instance", testInstanceProvision)

	Context("instance status observedGeneration and isChangeApplied fields", testInstanceParameterUpdate)

	Context("Existing instance restore from RMAN backup", testInstanceRestore)
})

func testInstanceProvision() {
	const (
		Namespace    = "default"
		InstanceName = "test-instance-provision"

		timeout  = time.Second * 25
		interval = time.Millisecond * 15
	)
	It("Should reconcile instance and database instance successfully", func() {
		By("creating a new Instance")
		ctx := context.Background()
		instance := &v1alpha1.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      InstanceName,
				Namespace: Namespace,
			},
			Spec: v1alpha1.InstanceSpec{
				CDBName: "GCLOUD",
				InstanceSpec: commonv1alpha1.InstanceSpec{
					Images: images,
				},
			},
		}
		Expect(k8sClient.Create(ctx, instance)).Should(Succeed())

		objKey := client.ObjectKey{Namespace: Namespace, Name: InstanceName}

		By("checking that statefulset/deployment/svc are created")
		Eventually(
			func() error {
				var createdInst v1alpha1.Instance
				if err := k8sClient.Get(ctx, objKey, &createdInst); err != nil {
					return err
				}
				if cond := k8s.FindCondition(createdInst.Status.Conditions, k8s.Ready); !k8s.ConditionReasonEquals(cond, k8s.CreateInProgress) {
					return errors.New("expected update has not happened yet")
				}
				return nil
			}, timeout, interval).Should(Succeed())

		var sts appsv1.StatefulSetList
		Expect(k8sClient.List(ctx, &sts, client.InNamespace(Namespace))).Should(Succeed())
		Expect(len(sts.Items)).To(Equal(1))

		var deployment appsv1.DeploymentList
		Expect(k8sClient.List(ctx, &deployment, client.InNamespace(Namespace))).Should(Succeed())
		Expect(len(deployment.Items)).To(Equal(1))

		var services corev1.ServiceList
		Expect(k8sClient.List(ctx, &services, client.InNamespace(Namespace))).Should(Succeed())
		expectedNames := []string{
			"kubernetes",
			"test-instance-provision-svc",
			"test-instance-provision-dbdaemon-svc",
			"test-instance-provision-agent-svc",
		}
		sort.Strings(expectedNames)
		serviceNames := []string{}
		for _, item := range services.Items {
			serviceNames = append(serviceNames, item.Name)
		}
		sort.Strings(serviceNames)
		Expect(serviceNames).To(Equal(expectedNames))

		By("setting Instance as Ready")
		fakeClientFactory.Caclient.SetAsyncBootstrapDatabase(true)
		fakeDatabaseClientFactory.Dbclient.SetNextGetOperationStatus(testhelpers.StatusRunning)

		createdInstance := &v1alpha1.Instance{}
		testhelpers.K8sUpdateStatusWithRetry(k8sClient, ctx, objKey, createdInstance, func(obj *client.Object) {
			(*obj).(*v1alpha1.Instance).Status = v1alpha1.InstanceStatus{
				InstanceStatus: commonv1alpha1.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:               k8s.Ready,
							Status:             metav1.ConditionTrue,
							Reason:             k8s.CreateComplete,
							LastTransitionTime: metav1.Now().Rfc3339Copy(),
						},
					},
				},
			}
		})

		By("Verifying database bootstrap LRO was started")
		Eventually(func() (string, error) {
			return getConditionReason(ctx, objKey, k8s.DatabaseInstanceReady)
		}, timeout, interval).Should(Equal(k8s.BootstrapInProgress))

		By("Verifying database instance is Ready on bootstrap LRO completion")
		fakeDatabaseClientFactory.Dbclient.SetNextGetOperationStatus(testhelpers.StatusDone)

		Eventually(func() (metav1.ConditionStatus, error) {
			return getConditionStatus(ctx, objKey, k8s.DatabaseInstanceReady)
		}, timeout, interval).Should(Equal(metav1.ConditionTrue))
		// There might be more than one call to DeleteOperation
		// from the reconciler loop with the same LRO id.
		// This should be expected and not harmful.
		Eventually(fakeClientFactory.Caclient.DeleteOperationCalledCnt()).Should(BeNumerically(">=", 1))
		Expect(fakeClientFactory.Caclient.BootstrapDatabaseCalledCnt()).Should(BeNumerically(">=", 1))

		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, objKey, instance)
	})
}

func getConditionReason(ctx context.Context, objKey client.ObjectKey, cType string) (string, error) {
	var instance v1alpha1.Instance
	if err := k8sClient.Get(ctx, objKey, &instance); err != nil {
		return "", err
	}

	if cond := k8s.FindCondition(instance.Status.Conditions, cType); cond != nil {
		return cond.Reason, nil
	}
	return "", nil
}

func getConditionStatus(ctx context.Context, objKey client.ObjectKey, cType string) (metav1.ConditionStatus, error) {
	var instance v1alpha1.Instance
	if err := k8sClient.Get(ctx, objKey, &instance); err != nil {
		return "", err
	}
	if cond := k8s.FindCondition(instance.Status.Conditions, cType); cond != nil {
		return cond.Status, nil
	}
	return metav1.ConditionUnknown, nil
}
