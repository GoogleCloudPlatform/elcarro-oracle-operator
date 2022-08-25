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
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
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
		"logging_sidecar": "loggingSidecarImage",
	}
	reconciler *InstanceReconciler

	fakeDatabaseClientFactory *testhelpers.FakeDatabaseClientFactory
)

func TestInstanceController(t *testing.T) {

	// Mock functions
	CheckStatusInstanceFunc = func(ctx context.Context, r client.Reader, dbClientFactory controllers.DatabaseClientFactory, instName, cdbName, namespace, clusterIP, DBDomain string, log logr.Logger) (string, error) {
		return "Ready", nil
	}

	fakeDatabaseClientFactory = &testhelpers.FakeDatabaseClientFactory{}
	var locker = sync.Map{}

	testhelpers.CdToRoot(t)
	testhelpers.RunFunctionalTestSuite(t, &k8sClient, &k8sManager,
		[]*runtime.SchemeBuilder{&v1alpha1.SchemeBuilder.SchemeBuilder},
		"Instance controller",
		func() []testhelpers.Reconciler {
			reconciler = &InstanceReconciler{
				Client:    k8sManager.GetClient(),
				Log:       ctrl.Log.WithName("controllers").WithName("Instance"),
				SchemeVal: k8sManager.GetScheme(),
				// We need a clone of 'images' to avoid race conditions between reconciler
				// goroutine and the test goroutine.
				Images:        CloneMap(images),
				Recorder:      k8sManager.GetEventRecorderFor("instance-controller"),
				InstanceLocks: &locker,

				DatabaseClientFactory: fakeDatabaseClientFactory,
			}

			return []testhelpers.Reconciler{reconciler}
		},
		[]string{}, // Use default CRD locations
	)
}

var _ = Describe("Instance controller", func() {

	BeforeEach(func() {

		fakeDatabaseClientFactory.Reset()
		fakeDatabaseClientFactory.Dbclient.SetMethodToResp(
			"FetchServiceImageMetaData", &dbdpb.FetchServiceImageMetaDataResponse{
				Version:     "19.3",
				CdbName:     "GCLOUD",
				OracleHome:  "/u01/app/oracle/product/19.3/db",
				SeededImage: true,
			})
	})

	Context("New instance", testInstanceProvision)
	Context("Existing instance restore from RMAN backup", testInstanceRestore)
	Context("instance status observedGeneration and isChangeApplied fields", testInstanceParameterUpdate)
	Context("Test pause mode", testInstancePauseUpdate)
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
		//Expect(len(deployment.Items)).To(Equal(1))

		var services corev1.ServiceList
		Expect(k8sClient.List(ctx, &services, client.InNamespace(Namespace))).Should(Succeed())
		expectedNames := []string{
			"kubernetes",
			"test-instance-provision-svc",
			"test-instance-provision-dbdaemon-svc",
		}
		sort.Strings(expectedNames)
		serviceNames := []string{}
		for _, item := range services.Items {
			serviceNames = append(serviceNames, item.Name)
		}
		sort.Strings(serviceNames)
		Expect(serviceNames).To(Equal(expectedNames))

		By("setting Instance as Ready")
		fakeDatabaseClientFactory.Dbclient.SetAsyncBootstrapDatabase(true)
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
		Eventually(fakeDatabaseClientFactory.Dbclient.DeleteOperationCalledCnt()).Should(BeNumerically(">=", 1))
		Expect(fakeDatabaseClientFactory.Dbclient.BootstrapDatabaseAsyncCalledCnt()).Should(BeNumerically(">=", 1))

		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, objKey, instance)
	})
}

func testInstancePauseUpdate() {
	const (
		Namespace    = "default"
		InstanceName = "test-instance-parameter"
		timeout      = time.Second * 25
		interval     = time.Millisecond * 15
	)
	It("should update observedGeneration", func() {
		objKey := client.ObjectKey{Namespace: Namespace, Name: InstanceName}
		By("creating a new Instance")
		ctx := context.Background()
		instance := v1alpha1.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      objKey.Name,
				Namespace: objKey.Namespace,
			},
			Spec: v1alpha1.InstanceSpec{
				CDBName: "GCLOUD",
				InstanceSpec: commonv1alpha1.InstanceSpec{
					Images: images,
				},
			},
		}
		Expect(k8sClient.Create(ctx, &instance)).Should(Succeed())
		createdInstance := &v1alpha1.Instance{}
		Eventually(
			func() error {
				return k8sClient.Get(ctx, objKey, createdInstance)
			}, timeout, interval).Should(Succeed())

		By("set instance mode in spec to pause")
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			if err := k8sClient.Get(ctx, objKey, &instance); err != nil {
				return err
			}
			instance.Status = v1alpha1.InstanceStatus{
				InstanceStatus: commonv1alpha1.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:               k8s.Ready,
							Status:             metav1.ConditionTrue,
							Reason:             k8s.CreateComplete,
							LastTransitionTime: metav1.Now().Rfc3339Copy(),
						},
						{
							Type:               k8s.DatabaseInstanceReady,
							Status:             metav1.ConditionTrue,
							Reason:             k8s.CreateComplete,
							LastTransitionTime: metav1.Now().Rfc3339Copy(),
						},
					},
				},
			}
			return k8sClient.Status().Update(ctx, &instance)
		})).Should(Succeed())

		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			if err := k8sClient.Get(ctx, objKey, &instance); err != nil {
				return err
			}
			instance.Spec.Mode = "Pause"
			return k8sClient.Update(ctx, &instance)
		})).Should(Succeed())

		// Wait for Instance status to show the PauseMode state
		Eventually(func() bool {
			Expect(k8sClient.Get(ctx, objKey, &instance)).Should(Succeed())
			cond := k8s.FindCondition(instance.Status.Conditions, k8s.Ready)
			return k8s.ConditionReasonEquals(cond, k8s.PauseMode)
		}, timeout, interval).Should(Equal(true))
		Expect(k8sClient.Delete(ctx, &instance)).Should(Succeed())
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
