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

package exportcontroller

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

var (
	k8sClient         client.Client
	k8sManager        ctrl.Manager
	reconciler        *ExportReconciler
	fakeClientFactory *testhelpers.FakeClientFactory

	fakeDatabaseClientFactory *testhelpers.FakeDatabaseClientFactory
)

func TestExportController(t *testing.T) {
	fakeClientFactory = &testhelpers.FakeClientFactory{}
	fakeDatabaseClientFactory = &testhelpers.FakeDatabaseClientFactory{}

	testhelpers.RunReconcilerTestSuite(t, &k8sClient, &k8sManager, "Export controller", func() []testhelpers.Reconciler {
		reconciler = &ExportReconciler{
			Client:        k8sManager.GetClient(),
			Log:           ctrl.Log.WithName("controllers").WithName("Export"),
			Scheme:        k8sManager.GetScheme(),
			ClientFactory: fakeClientFactory,
			Recorder:      k8sManager.GetEventRecorderFor("export-controller"),

			DatabaseClientFactory: fakeDatabaseClientFactory,
		}

		return []testhelpers.Reconciler{reconciler}
	})
}

var _ = Describe("Export controller", func() {
	const (
		namespace     = "default"
		exportName    = "test-export"
		instanceName  = "test-instance"
		databaseName  = "pdb1"
		adminPassword = "pwd123"
		timeout       = time.Second * 15
		interval      = time.Millisecond * 15
	)

	var (
		instance              *v1alpha1.Instance
		database              *v1alpha1.Database
		export                *v1alpha1.Export
		dbObjKey              client.ObjectKey
		objKey                client.ObjectKey
		fakeConfigAgentClient *testhelpers.FakeConfigAgentClient
		fakeDatabaseClient    *testhelpers.FakeDatabaseClient
	)
	ctx := context.Background()

	BeforeEach(func() {
		By("creating an instance")
		instance = &v1alpha1.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testhelpers.RandName(instanceName),
				Namespace: namespace,
			},
		}
		Expect(k8sClient.Create(ctx, instance)).Should(Succeed())
		instance.Status.Conditions = k8s.Upsert(instance.Status.Conditions, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, "")
		Expect(k8sClient.Status().Update(ctx, instance)).Should(Succeed())

		createdInstance := &v1alpha1.Instance{}
		Eventually(
			func() error {
				return k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: instance.Name}, createdInstance)
			}, timeout, interval).Should(Succeed())

		By("creating a database")
		database = &v1alpha1.Database{
			ObjectMeta: metav1.ObjectMeta{
				Name:      databaseName,
				Namespace: namespace,
			},
			Spec: v1alpha1.DatabaseSpec{
				DatabaseSpec: commonv1alpha1.DatabaseSpec{
					Name:     databaseName,
					Instance: instance.Name,
				},
				AdminPassword: adminPassword,
				Users:         []v1alpha1.UserSpec{},
			},
		}
		Expect(k8sClient.Create(ctx, database)).Should(Succeed())

		dbObjKey = client.ObjectKey{Namespace: namespace, Name: databaseName}
		createdDatabase := &v1alpha1.Database{}
		Eventually(
			func() error {
				return k8sClient.Get(ctx, dbObjKey, createdDatabase)
			}, timeout, interval).Should(Succeed())

		fakeClientFactory.Reset()
		fakeConfigAgentClient = fakeClientFactory.Caclient
		fakeDatabaseClientFactory.Reset()
		fakeDatabaseClient = fakeDatabaseClientFactory.Dbclient
	})

	AfterEach(func() {
		Expect(k8sClient.Delete(ctx, instance)).Should(Succeed())
		Expect(k8sClient.Delete(ctx, database)).Should(Succeed())
		Expect(k8sClient.Delete(ctx, export)).Should(Succeed())
	})

	CreateExport := func() {
		By("creating a new export")
		export = &v1alpha1.Export{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      exportName,
			},
			Spec: v1alpha1.ExportSpec{
				Instance:         instance.Name,
				DatabaseName:     databaseName,
				Type:             "DataPump",
				ExportObjectType: "Schemas",
				ExportObjects:    []string{"scott"},
				FlashbackTime:    &metav1.Time{Time: time.Now()},
			},
		}

		objKey = client.ObjectKey{Namespace: namespace, Name: exportName}
		Expect(k8sClient.Create(ctx, export)).Should(Succeed())
	}

	SetDatabaseReadyStatus := func(cond metav1.ConditionStatus) {
		By("setting database ready status")
		database.Status.Conditions = k8s.Upsert(database.Status.Conditions, k8s.Ready, cond, k8s.CreateComplete, "")
		Expect(k8sClient.Status().Update(ctx, database)).Should(Succeed())
		Eventually(func() (metav1.ConditionStatus, error) {
			return getConditionStatus(ctx, dbObjKey, k8s.Ready)
		}, timeout, interval).Should(Equal(cond))
	}

	Context("export through data pump", func() {
		It("should mark export as pending", func() {
			SetDatabaseReadyStatus(metav1.ConditionFalse)
			CreateExport()

			By("verifying export is pending")
			Eventually(func() (string, error) {
				return getConditionReason(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.ExportPending))

			By("verifying post-conditions")
			Expect(fakeConfigAgentClient.DataPumpExportCalledCnt()).Should(Equal(0))
			Expect(fakeDatabaseClient.DeleteOperationCalledCnt()).Should(Equal(0))
		})

		It("should mark export as complete", func() {
			SetDatabaseReadyStatus(metav1.ConditionTrue)

			By("setting LRO status to Done")
			fakeDatabaseClient.SetNextGetOperationStatus(testhelpers.StatusDone)

			CreateExport()

			By("checking export condition")
			Eventually(func() (string, error) {
				return getConditionReason(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.ExportComplete))

			By("verifying post-conditions")
			Expect(fakeConfigAgentClient.DataPumpExportCalledCnt()).Should(Equal(1))
			Expect(fakeDatabaseClient.DeleteOperationCalledCnt()).Should(Equal(1))
		})

		It("should mark export as failed", func() {
			SetDatabaseReadyStatus(metav1.ConditionTrue)

			By("setting LRO status to DoneWithError")
			fakeDatabaseClient.SetNextGetOperationStatus(testhelpers.StatusDoneWithError)

			CreateExport()

			By("checking export has failed")
			Eventually(func() (string, error) {
				return getConditionReason(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.ExportFailed))

			By("verifying post-conditions")
			Expect(fakeConfigAgentClient.DataPumpExportCalledCnt()).Should(Equal(1))
			Expect(fakeDatabaseClient.DeleteOperationCalledCnt()).Should(Equal(1))
		})
	})
})

func getConditionReason(ctx context.Context, objKey client.ObjectKey, condType string) (string, error) {
	var export v1alpha1.Export

	if err := k8sClient.Get(ctx, objKey, &export); err != nil {
		return "", err
	}

	cond := k8s.FindCondition(export.Status.Conditions, condType)
	if cond == nil {
		return "", fmt.Errorf("%v condition type not found", condType)
	}
	return cond.Reason, nil
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
