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

package importcontroller

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

var (
	k8sClient         client.Client
	k8sManager        ctrl.Manager
	reconciler        *ImportReconciler
	fakeClientFactory *testhelpers.FakeClientFactory

	fakeDatabaseClientFactory *testhelpers.FakeDatabaseClientFactory
)

func TestImportController(t *testing.T) {
	fakeClientFactory = &testhelpers.FakeClientFactory{}
	fakeDatabaseClientFactory = &testhelpers.FakeDatabaseClientFactory{}

	testhelpers.RunReconcilerTestSuite(t, &k8sClient, &k8sManager, "Import controller", func() []testhelpers.Reconciler {
		reconciler = &ImportReconciler{
			Client:        k8sManager.GetClient(),
			Log:           ctrl.Log.WithName("controllers").WithName("Import"),
			Scheme:        k8sManager.GetScheme(),
			ClientFactory: fakeClientFactory,
			Recorder:      k8sManager.GetEventRecorderFor("import-controller"),

			DatabaseClientFactory: fakeDatabaseClientFactory,
		}

		return []testhelpers.Reconciler{reconciler}
	})
}

var _ = Describe("Import controller", func() {
	const (
		namespace    = "default"
		databaseName = "pdb1"

		timeout  = time.Second * 15
		interval = time.Millisecond * 50
	)

	ctx := context.Background()

	var fakeConfigAgentClient *testhelpers.FakeConfigAgentClient
	var fakeDatabaseClient *testhelpers.FakeDatabaseClient

	var (
		instance        *v1alpha1.Instance
		database        *v1alpha1.Database
		imp             *v1alpha1.Import
		importObjectKey client.ObjectKey
	)

	BeforeEach(func() {
		// create instance
		instance = &v1alpha1.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testhelpers.RandName("instance"),
				Namespace: namespace,
			},
		}
		Expect(k8sClient.Create(ctx, instance)).Should(Succeed())

		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: instance.Name}, &v1alpha1.Instance{})
		}, timeout, interval).Should(Succeed())

		// create database
		database = &v1alpha1.Database{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      testhelpers.RandName("db"),
			},
			Spec: v1alpha1.DatabaseSpec{
				DatabaseSpec: commonv1alpha1.DatabaseSpec{
					Name:     databaseName,
					Instance: instance.Name,
				},
				AdminPassword: "123456",
				Users:         []v1alpha1.UserSpec{},
			},
		}

		Expect(k8sClient.Create(ctx, database)).Should(Succeed())

		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: database.Name}, &v1alpha1.Database{})
		}, timeout, interval).Should(Succeed())

		fakeClientFactory.Reset()
		fakeConfigAgentClient = fakeClientFactory.Caclient
		fakeDatabaseClientFactory.Reset()
		fakeDatabaseClient = fakeDatabaseClientFactory.Dbclient

		// define import, expect each test case create one
		importObjectKey = client.ObjectKey{Namespace: namespace, Name: testhelpers.RandName("import")}
		imp = &v1alpha1.Import{
			ObjectMeta: metav1.ObjectMeta{
				Name:      importObjectKey.Name,
				Namespace: importObjectKey.Namespace,
			},
			Spec: v1alpha1.ImportSpec{
				Instance:     instance.Name,
				DatabaseName: database.Name,
				GcsPath:      "gs://ex_bucket/import.dmp",
			},
		}
	})

	AfterEach(func() {
		Expect(k8sClient.Delete(ctx, database)).Should(Succeed())
		Expect(k8sClient.Delete(ctx, instance)).Should(Succeed())
		Expect(k8sClient.Delete(ctx, imp)).Should(Succeed())
	})

	Context("Database is ready", func() {

		BeforeEach(func() {
			dbKey := client.ObjectKey{Namespace: database.Namespace, Name: database.Name}
			database.Status.Conditions = k8s.Upsert(database.Status.Conditions, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, "")

			Expect(k8sClient.Status().Update(ctx, database)).Should(Succeed())

			Eventually(func() metav1.ConditionStatus {
				cond, err := getDatabaseReadyCondition(ctx, dbKey)
				if err != nil || cond == nil {
					return metav1.ConditionFalse
				}
				return cond.Status
			}, timeout, interval).Should(Equal(metav1.ConditionTrue))
		})

		It("Should succeed when LRO completes successfully", func() {
			By("simulating successful DataPumpImport LRO completion")
			fakeDatabaseClient.SetNextGetOperationStatus(testhelpers.StatusDone)

			By("creating a new import")
			Expect(k8sClient.Create(ctx, imp)).Should(Succeed())

			By("verifying post-conditions")
			Eventually(func() (metav1.ConditionStatus, error) {
				return getConditionStatus(ctx, importObjectKey, k8s.Ready)
			}, timeout, interval).Should(Equal(metav1.ConditionTrue))
			Eventually(fakeConfigAgentClient.DataPumpImportCalledCnt, timeout, interval).Should(Equal(1))
			Eventually(fakeDatabaseClient.DeleteOperationCalledCnt, timeout, interval).Should(Equal(1))

			readyCond, err := getCondition(ctx, importObjectKey, k8s.Ready)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(readyCond.Reason).Should(Equal(k8s.ImportComplete))
		})

		It("Should handle LRO failure", func() {
			By("simulating failed DataPumpImport LRO completion")
			fakeDatabaseClient.SetNextGetOperationStatus(testhelpers.StatusDoneWithError)

			By("creating a new import")
			Expect(k8sClient.Create(ctx, imp)).Should(Succeed())

			By("verifying post-conditions")
			Eventually(func() (string, error) {
				return getConditionReason(ctx, importObjectKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.ImportFailed))
			Eventually(fakeConfigAgentClient.DataPumpImportCalledCnt, timeout, interval).Should(Equal(1))
			Eventually(fakeDatabaseClient.DeleteOperationCalledCnt, timeout, interval).Should(Equal(1))
		})
	})

	Context("Database is not ready", func() {

		BeforeEach(func() {
			dbKey := client.ObjectKey{Namespace: database.Namespace, Name: database.Name}
			database.Status.Conditions = k8s.Upsert(database.Status.Conditions, k8s.Ready, metav1.ConditionFalse, k8s.CreatePending, "")

			Expect(k8sClient.Status().Update(ctx, database)).Should(Succeed())

			Eventually(func() string {
				cond, err := getDatabaseReadyCondition(ctx, dbKey)
				if err != nil || cond == nil {
					return ""
				}
				return cond.Reason
			}, timeout, interval).Should(Equal(k8s.CreatePending))
		})

		It("Should keep Import in Pending state", func() {
			By("creating a new import")
			Expect(k8sClient.Create(ctx, imp)).Should(Succeed())

			Eventually(func() (string, error) {
				return getConditionReason(ctx, importObjectKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.ImportPending))

			// give controller some time to run import if it unexpectedly is going to
			time.Sleep(100 * time.Millisecond)
			Expect(fakeConfigAgentClient.DataPumpImportCalledCnt()).Should(Equal(0))
			Expect(getConditionReason(ctx, importObjectKey, k8s.Ready)).Should(Equal(k8s.ImportPending))
		})
	})
})

func getConditionStatus(ctx context.Context, objKey client.ObjectKey, condType string) (metav1.ConditionStatus, error) {
	cond, err := getCondition(ctx, objKey, condType)
	if cond == nil {
		return metav1.ConditionFalse, err
	}
	return cond.Status, err
}

func getConditionReason(ctx context.Context, objKey client.ObjectKey, condType string) (string, error) {
	cond, err := getCondition(ctx, objKey, condType)
	if cond == nil {
		return "", err
	}
	return cond.Reason, err
}

func getCondition(ctx context.Context, objKey client.ObjectKey, condType string) (*metav1.Condition, error) {
	imp := &v1alpha1.Import{}
	if err := k8sClient.Get(ctx, objKey, imp); err != nil {
		return nil, err
	}
	return k8s.FindCondition(imp.Status.Conditions, condType), nil
}

func getDatabaseReadyCondition(ctx context.Context, objKey client.ObjectKey) (*metav1.Condition, error) {
	db := &v1alpha1.Database{}
	if err := k8sClient.Get(ctx, objKey, db); err != nil {
		return &metav1.Condition{}, err
	}

	return k8s.FindCondition(db.Status.Conditions, k8s.Ready), nil
}
