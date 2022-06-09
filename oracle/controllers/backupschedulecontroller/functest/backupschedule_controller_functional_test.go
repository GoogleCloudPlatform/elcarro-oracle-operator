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

package backupschedulecontroller_func_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/backupschedulecontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/cronanythingcontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

var (
	k8sClient  client.Client
	k8sManager ctrl.Manager
)

type fakeBackupReconiler struct {
	client.Client
}

func (f *fakeBackupReconiler) Reconcile(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
	ctx := context.TODO()
	var backup v1alpha1.Backup
	if err := f.Get(ctx, req.NamespacedName, &backup); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	readyCond := k8s.FindCondition(backup.Status.Conditions, k8s.Ready)
	if k8s.ConditionReasonEquals(readyCond, k8s.BackupReady) {
		return ctrl.Result{}, nil
	}
	backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionTrue, k8s.BackupReady, "")
	backup.Status.Phase = commonv1alpha1.BackupSucceeded
	if err := f.Status().Update(ctx, &backup); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (f *fakeBackupReconiler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Backup{}).Complete(f)
}

func TestBackupsScheduleController(t *testing.T) {
	testhelpers.CdToRoot(t)
	testhelpers.RunFunctionalTestSuite(t, &k8sClient, &k8sManager,
		[]*runtime.SchemeBuilder{&v1alpha1.SchemeBuilder.SchemeBuilder},
		"BackupSchedule controller",
		func() []testhelpers.Reconciler {
			backupReconciler := &fakeBackupReconiler{k8sClient}
			backupScheduleReconciler := backupschedulecontroller.NewBackupScheduleReconciler(
				k8sManager,
				&backupschedulecontroller.RealBackupScheduleControl{Client: k8sManager.GetClient()},
				&cronanythingcontroller.RealCronAnythingControl{Client: k8sManager.GetClient()},
				&backupschedulecontroller.RealBackupControl{Client: k8sManager.GetClient()},
			)
			cronanythingReconciler, err := cronanythingcontroller.NewCronAnythingReconciler(k8sManager, ctrl.Log.WithName("controllers").WithName("CronAnything"), &cronanythingcontroller.RealCronAnythingControl{
				Client: k8sManager.GetClient(),
			})
			if err != nil {
				t.Fatalf("failed to create cronanythingcontroller for backup schedule test")
			}
			return []testhelpers.Reconciler{backupReconciler, backupScheduleReconciler, cronanythingReconciler}
		},
		[]string{}, // Use default CRD locations
	)
}

var _ = Describe("BackupSchedule controller", func() {
	// Define utility constants for object names and testing timeouts and intervals.
	const (
		namespace          = "default"
		backupScheduleName = "test-backup-schedule"
		instanceName       = "test-instance"

		timeout  = time.Second * 15
		interval = time.Millisecond * 15
	)

	var instance v1alpha1.Instance

	ctx := context.Background()

	BeforeEach(func() {
		instance = v1alpha1.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testhelpers.RandName(instanceName),
				Namespace: namespace,
			},
			Spec: v1alpha1.InstanceSpec{
				InstanceSpec: commonv1alpha1.InstanceSpec{
					Disks: []commonv1alpha1.DiskSpec{
						{
							Name: "DataDisk",
						},
						{
							Name: "LogDisk",
						},
					},
				},
			}}
		Expect(k8sClient.Create(ctx, &instance)).Should(Succeed())
		instance.Status.Conditions = k8s.Upsert(instance.Status.Conditions, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, "")
		Expect(k8sClient.Status().Update(ctx, &instance)).Should(Succeed())

		createdInstance := &v1alpha1.Instance{}
		// We'll need to retry getting this newly created Instance, given that creation may not immediately happen.
		Eventually(
			func() error {
				return k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: instance.Name}, createdInstance)
			}, timeout, interval).Should(Succeed())
	})

	AfterEach(func() {
		Expect(k8sClient.Delete(ctx, &instance)).Should(Succeed())
		createdBackupSchedules := &v1alpha1.BackupScheduleList{}
		Expect(k8sClient.List(ctx, createdBackupSchedules)).Should(Succeed())
		for _, backupSchedule := range createdBackupSchedules.Items {
			Expect(k8sClient.Delete(ctx, &backupSchedule)).To(Succeed())
		}
		createdBackups := &v1alpha1.BackupList{}
		Expect(k8sClient.List(ctx, createdBackups)).Should(Succeed())

		for _, backup := range createdBackups.Items {
			Expect(k8sClient.Delete(ctx, &backup)).To(Succeed())
		}
	})
	Context("New backup schedule", func() {
		It("Should create backups based on schedule", func() {
			testBackupCreation(namespace, backupScheduleName, instanceName)
		})
	})

	Context("New backup schedule with retention policy", func() {
		It("Should prune backups based on retention policy", func() {
			testBackupRetention(namespace, backupScheduleName, instanceName)
		})
	})
})

func testBackupCreation(namespace, backupScheduleName, instanceName string) {
	By("By creating a BackupSchedule of the instance")
	backupSchedule := &v1alpha1.BackupSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      backupScheduleName,
		},
		Spec: v1alpha1.BackupScheduleSpec{
			BackupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: instanceName,
					Type:     commonv1alpha1.BackupTypeSnapshot,
				},
			},
			BackupScheduleSpec: commonv1alpha1.BackupScheduleSpec{
				Schedule:                "* * * * *",
				StartingDeadlineSeconds: pointer.Int64Ptr(5),
			},
		},
	}
	Expect(k8sClient.Create(context.TODO(), backupSchedule)).Should(Succeed())
	By("Checking for the first Backup to be created")
	Eventually(func() (int, error) {
		return getBackupsTotal()
	}, time.Minute*2, time.Second).Should(Equal(1))
	By("Checking for the second Backup to be created")
	Eventually(func() (int, error) {
		return getBackupsTotal()
	}, time.Second*65, time.Second).Should(Equal(2))
	By("Checking for the third Backup to be created")
	Eventually(func() (int, error) {
		return getBackupsTotal()
	}, time.Second*65, time.Second).Should(Equal(3))
}

func testBackupRetention(namespace, backupScheduleName, instanceName string) {
	By("By creating a BackupSchedule of the instance")
	backupSchedule := &v1alpha1.BackupSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      backupScheduleName,
		},
		Spec: v1alpha1.BackupScheduleSpec{
			BackupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: instanceName,
					Type:     commonv1alpha1.BackupTypeSnapshot,
				},
			},
			BackupScheduleSpec: commonv1alpha1.BackupScheduleSpec{
				Schedule:                "* * * * *",
				StartingDeadlineSeconds: pointer.Int64Ptr(5),
				BackupRetentionPolicy: &commonv1alpha1.BackupRetentionPolicy{
					BackupRetention: pointer.Int32Ptr(2),
				},
			},
		},
	}
	Expect(k8sClient.Create(context.TODO(), backupSchedule)).Should(Succeed())
	By("Checking for the first Backup to be created")
	var toBeDelete v1alpha1.Backup
	toBeDeleteKey := client.ObjectKey{Namespace: toBeDelete.Namespace, Name: toBeDelete.Name}
	Eventually(func() (int, error) {
		backups, err := getBackups()
		if err != nil {
			return -1, err
		}
		if len(backups) == 1 {
			toBeDelete = backups[0]
		}
		return len(backups), nil
	}, time.Minute*2, time.Second).Should(Equal(1))

	By("Checking for the first Backup to be deleted")
	Eventually(func() bool {
		backup := &v1alpha1.Backup{}
		return apierrors.IsNotFound(k8sClient.Get(context.TODO(), toBeDeleteKey, backup))
	}, time.Second*200, time.Second).Should(BeTrue())
}

func getBackupsTotal() (int, error) {
	backups, err := getBackups()
	if err != nil {
		return -1, err
	}
	return len(backups), nil
}

func getBackups() ([]v1alpha1.Backup, error) {
	backupList := &v1alpha1.BackupList{}
	err := k8sClient.List(context.TODO(), backupList)
	if err != nil {
		return nil, fmt.Errorf("unable to list backup: %v", err)
	}
	return backupList.Items, nil
}
