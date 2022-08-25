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
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

var (
	k8sClient                 client.Client
	k8sManager                ctrl.Manager
	DatabaseName              = testhelpers.RandName("db1")
	Namespace                 = testhelpers.RandName("ns1")
	fakeDatabaseClientFactory *testhelpers.FakeDatabaseClientFactory
)

func TestDatabaseController(t *testing.T) {
	// Mock function returns.
	skipLBCheckForTest = true
	CheckStatusInstanceFunc = func(ctx context.Context, r client.Reader, dbClientFactory controllers.DatabaseClientFactory, instName, cdbName, namespace, clusterIP, DBDomain string, log logr.Logger) (string, error) {
		return "Ready", nil
	}
	fakeDatabaseClientFactory = &testhelpers.FakeDatabaseClientFactory{}
	// Run test suite for database reconciler.
	testhelpers.CdToRoot(t)
	testhelpers.RunFunctionalTestSuite(t,
		&k8sClient,
		&k8sManager,
		[]*runtime.SchemeBuilder{&v1alpha1.SchemeBuilder.SchemeBuilder},
		"Database controller",
		func() []testhelpers.Reconciler {
			return []testhelpers.Reconciler{&DatabaseReconciler{
				Client:                k8sManager.GetClient(),
				Log:                   ctrl.Log.WithName("controllers").WithName("Database"),
				Scheme:                k8sManager.GetScheme(),
				Recorder:              k8sManager.GetEventRecorderFor("database-controller"),
				DatabaseClientFactory: fakeDatabaseClientFactory,
			}}
		},
		[]string{}, // Use default CRD locations
	)
}

var _ = Describe("Database controller", func() {
	// Define utility constants for object names and testing timeouts and intervals.
	const (
		instanceName  = "mydb"
		podName       = "podname"
		svcName       = "mydb-svc"
		svcAgentName  = "mydb-agent-svc"
		adminPassword = "pwd123"
		userName      = "joice"
		password      = "guess"
		privileges    = "connect"
		timeout       = time.Second * 15
		interval      = time.Millisecond * 15
	)

	ctx := context.Background()

	createdInstance := &v1alpha1.Instance{}
	createdNs := &v1.Namespace{}
	createdPod := &v1.Pod{}
	createdAgentSvc := &v1.Service{}
	createdSvc := &v1.Service{}
	createdDatabase := &v1alpha1.Database{}

	// Currently we only have one create database tests, after each
	// serves as finally resource clean-up.
	AfterEach(func() {
		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, client.ObjectKey{Namespace: Namespace, Name: DatabaseName}, createdDatabase)
		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, client.ObjectKey{Namespace: Namespace, Name: instanceName}, createdInstance)
		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, client.ObjectKey{Name: podName, Namespace: Namespace}, createdPod)
		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, client.ObjectKey{Name: svcAgentName, Namespace: Namespace}, createdAgentSvc)
		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, client.ObjectKey{Name: svcName, Namespace: Namespace}, createdSvc)
		// Namespace objects can not be completely deleted in testenv.
		testhelpers.K8sDeleteWithRetryNoWait(k8sClient, ctx, client.ObjectKey{Name: Namespace}, createdNs)
	})

	Context("Setup database with manager", func() {
		It("Should create a database successfully", func() {
			By("By creating a namespace")

			ns := &v1.Namespace{
				TypeMeta: metav1.TypeMeta{Kind: "namespace"},
				ObjectMeta: metav1.ObjectMeta{
					Name: Namespace,
				},
			}
			testhelpers.K8sCreateAndGet(k8sClient, ctx, client.ObjectKey{Name: Namespace}, ns, createdNs)

			By("By creating a new instance")
			instance := &v1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      instanceName,
					Namespace: Namespace,
					Labels:    map[string]string{"instance": instanceName},
				},
			}
			testhelpers.K8sCreateAndGet(k8sClient, ctx, client.ObjectKey{Namespace: Namespace, Name: instanceName}, instance, createdInstance)

			By("By creating a pod")
			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: Namespace,
					Labels:    map[string]string{"instance": instanceName},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:    "oracledb",
							Image:   "image",
							Command: []string{"cmd"},
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceCPU: resource.MustParse("0m"),
								},
							},
						},
					},
				},
			}
			testhelpers.K8sCreateAndGet(k8sClient, ctx, client.ObjectKey{Name: podName, Namespace: Namespace}, pod, createdPod)

			By("By creating a service")
			svc := &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcName,
					Namespace: Namespace,
				},
				Spec: v1.ServiceSpec{Ports: []v1.ServicePort{{Port: 8787}}},
			}
			objKey := client.ObjectKey{Name: svcName, Namespace: Namespace}
			testhelpers.K8sCreateAndGet(k8sClient, ctx, objKey, svc, createdSvc)

			By("By creating an agent service")
			AgentSvc := &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcAgentName,
					Namespace: Namespace,
				},
				Spec: v1.ServiceSpec{Ports: []v1.ServicePort{{Port: 8788}}},
			}
			testhelpers.K8sCreateAndGet(k8sClient, ctx, client.ObjectKey{Name: svcAgentName, Namespace: Namespace}, AgentSvc, createdAgentSvc)

			By("By creating/reconciling a database")
			// Note that reconcile will be called automatically
			// by kubernetes runtime on database creation.
			database := &v1alpha1.Database{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: Namespace,
					Name:      DatabaseName,
					Labels:    map[string]string{"instance": instanceName},
				},
				Spec: v1alpha1.DatabaseSpec{
					DatabaseSpec: commonv1alpha1.DatabaseSpec{
						Name:     DatabaseName,
						Instance: instanceName,
					},
					AdminPassword: adminPassword,
					Users: []v1alpha1.UserSpec{
						{UserSpec: commonv1alpha1.UserSpec{Name: userName, CredentialSpec: commonv1alpha1.CredentialSpec{Password: password}}, Privileges: []v1alpha1.PrivilegeSpec{privileges}},
						{UserSpec: commonv1alpha1.UserSpec{Name: "testUser2", CredentialSpec: commonv1alpha1.CredentialSpec{Password: password}}, Privileges: []v1alpha1.PrivilegeSpec{privileges}},
						{UserSpec: commonv1alpha1.UserSpec{Name: "testUser3", CredentialSpec: commonv1alpha1.CredentialSpec{Password: password}}, Privileges: []v1alpha1.PrivilegeSpec{privileges}},
						{UserSpec: commonv1alpha1.UserSpec{Name: "testUser4", CredentialSpec: commonv1alpha1.CredentialSpec{Password: password}}, Privileges: []v1alpha1.PrivilegeSpec{privileges}},
					},
				},
			}
			DbObjKey := client.ObjectKey{Namespace: Namespace, Name: DatabaseName}
			testhelpers.K8sCreateAndGet(k8sClient, ctx, DbObjKey, database, createdDatabase)

			By("By checking that the updated database succeeded")
			var updatedDatabase v1alpha1.Database
			Expect(k8sClient.Get(ctx, DbObjKey, &updatedDatabase)).Should(Succeed())
			Eventually(func() (commonv1alpha1.DatabasePhase, error) {
				return getPhase(ctx, DbObjKey)
			}, timeout, interval).Should(Equal(commonv1alpha1.DatabaseReady))

			By("checking database ready status")
			Eventually(func() (metav1.ConditionStatus, error) {
				return getConditionStatus(ctx, DbObjKey, k8s.Ready)
			}, timeout, interval).Should(Equal(metav1.ConditionTrue))

			By("checking the user names")
			Eventually(func() ([]string, error) {
				return getUserNames(ctx, DbObjKey)
			}, timeout, interval).Should(Equal([]string{userName, "testUser2", "testUser3", "..."}))

			By("checking user ready status")
			Eventually(func() (metav1.ConditionStatus, error) {
				return getConditionStatus(ctx, DbObjKey, k8s.UserReady)
			}, timeout, interval).Should(Equal(metav1.ConditionTrue))
		})
	})
})

func getPhase(ctx context.Context, objKey client.ObjectKey) (commonv1alpha1.DatabasePhase, error) {
	var database v1alpha1.Database
	if err := k8sClient.Get(ctx, objKey, &database); err != nil {
		return "", err
	}
	return database.Status.Phase, nil
}

func getConditionStatus(ctx context.Context, objKey client.ObjectKey, condType string) (metav1.ConditionStatus, error) {
	var database v1alpha1.Database
	if err := k8sClient.Get(ctx, objKey, &database); err != nil {
		return metav1.ConditionFalse, err
	}
	if cond := k8s.FindCondition(database.Status.Conditions, condType); cond != nil {
		return cond.Status, nil
	}
	return metav1.ConditionFalse, nil
}

func getUserNames(ctx context.Context, objKey client.ObjectKey) ([]string, error) {
	var database v1alpha1.Database
	if err := k8sClient.Get(ctx, objKey, &database); err != nil {
		return []string{""}, err
	}
	return database.Status.UserNames, nil
}
