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

package backupcontroller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	snapv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
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
	reconciler        *BackupReconciler
	fakeClientFactory *testhelpers.FakeClientFactory
)

func TestBackupController(t *testing.T) {
	fakeClientFactory = &testhelpers.FakeClientFactory{}

	testhelpers.RunReconcilerTestSuite(t, &k8sClient, &k8sManager, "Backup controller", func() []testhelpers.Reconciler {
		reconciler = &BackupReconciler{
			Client:        k8sManager.GetClient(),
			Log:           ctrl.Log.WithName("controllers").WithName("Backup"),
			Scheme:        k8sManager.GetScheme(),
			ClientFactory: fakeClientFactory,
			Recorder:      k8sManager.GetEventRecorderFor("backup-controller"),
		}

		return []testhelpers.Reconciler{reconciler}
	})
}

var _ = Describe("Backup controller", func() {
	// Define utility constants for object names and testing timeouts and intervals.
	const (
		Namespace    = "default"
		BackupName   = "test-backup"
		InstanceName = "test-instance"

		timeout  = time.Second * 15
		interval = time.Millisecond * 15
	)

	var instance v1alpha1.Instance

	ctx := context.Background()

	BeforeEach(func() {
		instance = v1alpha1.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testhelpers.RandName(InstanceName),
				Namespace: Namespace,
			},
			Spec: v1alpha1.InstanceSpec{
				InstanceSpec: commonv1alpha1.InstanceSpec{
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
				},
			},
		}
		createdInstance := v1alpha1.Instance{}
		objKey := client.ObjectKey{Namespace: Namespace, Name: instance.Name}
		testhelpers.K8sCreateAndGet(k8sClient, ctx, objKey, &instance, &createdInstance)

		instance.Status.Conditions = k8s.Upsert(instance.Status.Conditions, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, "")
		Expect(k8sClient.Status().Update(ctx, &instance)).Should(Succeed())
		Eventually(func() (metav1.ConditionStatus, error) {
			return getInstanceConditionStatus(ctx, objKey, k8s.Ready)
		}, timeout, interval).Should(Equal(metav1.ConditionTrue))

		fakeClientFactory.Reset()
	})

	AfterEach(func() {
		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, &instance)
		createdBackups := &v1alpha1.BackupList{}
		Expect(k8sClient.List(ctx, createdBackups)).Should(Succeed())
		for _, backup := range createdBackups.Items {
			testhelpers.K8sDeleteWithRetry(k8sClient, ctx, &backup)
		}
	})

	Context("New backup through snapshot", func() {
		It("Should create volume snapshots correctly", func() {
			By("By creating a Snapshot type backup of the instance")
			backup := &v1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: Namespace,
					Name:      BackupName,
				},
				Spec: v1alpha1.BackupSpec{
					BackupSpec: commonv1alpha1.BackupSpec{
						Instance: instance.Name,
						Type:     commonv1alpha1.BackupTypeSnapshot,
					},
				},
			}

			objKey := client.ObjectKey{Namespace: Namespace, Name: BackupName}
			testhelpers.K8sCreateWithRetry(k8sClient, ctx, backup)

			Eventually(func() (string, error) {
				return getConditionReason(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.BackupInProgress))

			var snapshots snapv1.VolumeSnapshotList
			Expect(k8sClient.List(ctx, &snapshots, client.InNamespace(Namespace))).Should(Succeed())
			Expect(len(snapshots.Items)).Should(Equal(2))
		})

		It("Should mark backup as failed because of invalid instance name", func() {
			By("By creating a Snapshot type backup of the instance")
			backup := &v1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: Namespace,
					Name:      BackupName,
				},
				Spec: v1alpha1.BackupSpec{
					BackupSpec: commonv1alpha1.BackupSpec{
						Instance: "bad-instance-name",
						Type:     commonv1alpha1.BackupTypeSnapshot,
					},
				},
			}

			objKey := client.ObjectKey{Namespace: Namespace, Name: BackupName}
			testhelpers.K8sCreateWithRetry(k8sClient, ctx, backup)

			Eventually(func() (string, error) {
				return getConditionReason(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.BackupFailed))
		})

		It("Should mark backup as failed because of instance is not ready", func() {
			By("By marking instance as not ready")
			instance.Status.Conditions = k8s.Upsert(instance.Status.Conditions, k8s.Ready, metav1.ConditionFalse, k8s.CreateInProgress, "")
			Expect(k8sClient.Status().Update(ctx, &instance)).Should(Succeed())
			By("By creating a Snapshot type backup of the instance")
			backup := &v1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: Namespace,
					Name:      BackupName,
				},
				Spec: v1alpha1.BackupSpec{
					BackupSpec: commonv1alpha1.BackupSpec{
						Instance: instance.Name,
						Type:     commonv1alpha1.BackupTypeSnapshot,
					},
				},
			}
			objKey := client.ObjectKey{Namespace: Namespace, Name: BackupName}
			testhelpers.K8sCreateWithRetry(k8sClient, ctx, backup)

			Eventually(func() (string, error) {
				return getConditionReason(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.BackupFailed))
		})
	})

	Context("New backup through RMAN with VerifyExists mode", func() {
		It("Should verify RMAN backup correctly", func() {
			oldFunc := preflightCheck
			preflightCheck = func(ctx context.Context, r *BackupReconciler, namespace, instName string, log logr.Logger) error {
				return nil
			}
			defer func() { preflightCheck = oldFunc }()

			By("By creating a RMAN type backup with VerifyExists mode of the instance")
			backup := &v1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: Namespace,
					Name:      BackupName,
				},
				Spec: v1alpha1.BackupSpec{
					BackupSpec: commonv1alpha1.BackupSpec{
						Instance: instance.Name,
						Type:     commonv1alpha1.BackupTypePhysical,
					},
					Mode:    v1alpha1.VerifyExists,
					GcsPath: "gs://elcarro_functional_test",
				},
			}

			objKey := client.ObjectKey{Namespace: Namespace, Name: BackupName}
			testhelpers.K8sCreateWithRetry(k8sClient, ctx, backup)

			By("By checking that a physical backup is verified")
			Eventually(func() (string, error) {
				return getConditionReason(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.BackupReady))
			Expect(fakeClientFactory.Caclient.VerifyPhysicalBackupCalledCnt()).Should(BeNumerically(">=", 1))
		})
	})

	Context("New backup through RMAN in LRO async environment", func() {
		It("Should create RMAN backup correctly", func() {
			oldFunc := preflightCheck
			preflightCheck = func(ctx context.Context, r *BackupReconciler, namespace, instName string, log logr.Logger) error {
				return nil
			}
			defer func() { preflightCheck = oldFunc }()

			// configure fake ConfigAgent to be in LRO mode
			fakeConfigAgentClient := fakeClientFactory.Caclient
			fakeConfigAgentClient.SetAsyncPhysicalBackup(true)
			fakeConfigAgentClient.SetNextGetOperationStatus(testhelpers.StatusRunning)

			By("By creating a RMAN type backup of the instance")
			backup := &v1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: Namespace,
					Name:      BackupName,
				},
				Spec: v1alpha1.BackupSpec{
					BackupSpec: commonv1alpha1.BackupSpec{
						Instance: instance.Name,
						Type:     commonv1alpha1.BackupTypePhysical,
					},
				},
			}

			objKey := client.ObjectKey{Namespace: Namespace, Name: BackupName}
			var createdBackup v1alpha1.Backup
			testhelpers.K8sCreateAndGet(k8sClient, ctx, objKey, backup, &createdBackup)

			By("By checking that physical backup was started")
			// test env should trigger reconciliation in background
			// and reconciler is expected to start backup.
			Eventually(func() (string, error) {
				return getConditionReason(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.BackupInProgress))
			Expect(fakeConfigAgentClient.PhysicalBackupCalledCnt()).Should(Equal(1))

			By("By checking that reconciler watches backup LRO status")
			getOperationCallsCntBefore := fakeConfigAgentClient.GetOperationCalledCnt()

			Expect(triggerReconcile(ctx, objKey)).Should(Succeed())
			Eventually(func() int {
				return fakeConfigAgentClient.GetOperationCalledCnt()
			}, timeout, interval).ShouldNot(Equal(getOperationCallsCntBefore))

			var updatedBackup v1alpha1.Backup
			Expect(k8sClient.Get(ctx, objKey, &updatedBackup)).Should(Succeed())
			Expect(k8s.FindCondition(updatedBackup.Status.Conditions, k8s.Ready).Reason).Should(Equal(k8s.BackupInProgress))

			By("By checking that physical backup is Ready on backup LRO completion")
			fakeConfigAgentClient.SetNextGetOperationStatus(testhelpers.StatusDone)
			Expect(triggerReconcile(ctx, objKey)).Should(Succeed())
			Eventually(func() (string, error) {
				return getConditionReason(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.BackupReady))

			Eventually(fakeConfigAgentClient.DeleteOperationCalledCnt, timeout, interval).Should(Equal(1))
		})

		It("Should mark unsuccessful RMAN backup as Failed", func() {
			oldFunc := preflightCheck
			preflightCheck = func(ctx context.Context, r *BackupReconciler, namespace, instName string, log logr.Logger) error {
				return nil
			}
			defer func() { preflightCheck = oldFunc }()

			// configure fake ConfigAgent to be in LRO mode with a
			// failed operation result.
			fakeConfigAgentClient := fakeClientFactory.Caclient
			fakeConfigAgentClient.SetAsyncPhysicalBackup(true)
			fakeConfigAgentClient.SetNextGetOperationStatus(testhelpers.StatusDoneWithError)

			By("By creating a RMAN type backup of the instance")
			backup := &v1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: Namespace,
					Name:      BackupName,
				},
				Spec: v1alpha1.BackupSpec{
					BackupSpec: commonv1alpha1.BackupSpec{
						Instance: instance.Name,
						Type:     commonv1alpha1.BackupTypePhysical,
					},
				},
			}

			objKey := client.ObjectKey{Namespace: Namespace, Name: BackupName}
			var createdBackup v1alpha1.Backup
			testhelpers.K8sCreateAndGet(k8sClient, ctx, objKey, backup, &createdBackup)

			By("By checking that physical backup was resolved to the Failed state")
			// test env should trigger reconciliation in background.
			Eventually(func() (string, error) {
				return getConditionReason(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.BackupFailed))

			var inst v1alpha1.Instance
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: instance.Name}, &inst)).Should(Succeed())
			Expect(inst.Status.BackupID).Should(Equal(""))
		})
	})
})

func getConditionReason(ctx context.Context, objKey client.ObjectKey, condType string) (string, error) {
	var backup v1alpha1.Backup
	if err := k8sClient.Get(ctx, objKey, &backup); err != nil {
		return "", err
	}

	cond := k8s.FindCondition(backup.Status.Conditions, condType)
	if cond == nil {
		return "", fmt.Errorf("%v condition type not found", condType)
	}
	return cond.Reason, nil
}

func getInstanceConditionStatus(ctx context.Context, objKey client.ObjectKey, condType string) (metav1.ConditionStatus, error) {
	var instance v1alpha1.Instance
	if err := k8sClient.Get(ctx, objKey, &instance); err != nil {
		return "", err
	}

	cond := k8s.FindCondition(instance.Status.Conditions, condType)
	if cond == nil {
		return "", fmt.Errorf("%v condition type not found", condType)
	}
	return cond.Status, nil
}

// triggerReconcile invokes k8s reconcile action by updating
// an irrelevant field.
func triggerReconcile(ctx context.Context, objKey client.ObjectKey) error {
	var backup v1alpha1.Backup
	if err := k8sClient.Get(ctx, objKey, &backup); err != nil {
		return err
	}

	backup.Spec.SectionSize++

	err := k8sClient.Update(ctx, &backup)
	if errors.IsConflict(err) {
		return nil
	}
	return err
}
