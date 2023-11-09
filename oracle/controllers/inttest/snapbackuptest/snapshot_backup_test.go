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

package snapbackuptest

import (
	"testing"
	"time"

	snapv1 "github.com/kubernetes-csi/external-snapshotter/client/v4/apis/volumesnapshot/v1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

// Made global to be accessible by AfterSuite
var k8sEnv = testhelpers.K8sOperatorEnvironment{}

// In case of Ctrl-C clean up the last valid k8sEnv.
var _ = AfterSuite(func() {
	k8sEnv.Close()
})

var _ = Describe("Backup through snapshot", func() {
	var namespace string
	var instanceName string

	BeforeEach(func() {
		defer GinkgoRecover()
		namespace = testhelpers.RandName("backup-snap-crd-test")
		instanceName = "mydb"
		k8sEnv.Init(namespace, namespace)
	})

	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			testhelpers.PrintSimpleDebugInfo(k8sEnv, instanceName, "GCLOUD")
		}
		k8sEnv.Close()
	})

	snapBackupTest := func(version string, edition string) {
		It("Should create snapshot based backup then restore to instance successfully", func() {
			log := logf.FromContext(nil)

			By("By creating a instance")
			testhelpers.CreateSimpleInstance(k8sEnv, instanceName, version, edition)

			createdInstance := &v1alpha1.Instance{}
			instKey := client.ObjectKey{Namespace: namespace, Name: instanceName}

			// Wait until the instance is "Ready" (requires 5+ minutes to download image)
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 20*time.Minute)

			// Wait until DatabaseInstanceReady = True
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, 10*time.Minute)

			// Add test data
			testhelpers.CreateSimplePDB(k8sEnv, instanceName)
			testhelpers.InsertSimpleData(k8sEnv)

			// Allow some time for the updates to reach the disk before creating a snapshot backup
			time.Sleep(5 * time.Second)

			By("By creating a snapshot based backup")
			backupName := "snap"
			backup := &v1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      backupName,
				},
				Spec: v1alpha1.BackupSpec{
					BackupSpec: commonv1alpha1.BackupSpec{
						Instance: instanceName,
						Type:     commonv1alpha1.BackupTypeSnapshot,
					},
				},
			}
			testhelpers.K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, backup)

			By("By checking volume snapshot is created and ready")
			backupKey := client.ObjectKey{Namespace: namespace, Name: backupName}

			// Wait for Ready or Error.
			var createdBackup v1alpha1.Backup
			var cond *metav1.Condition
			Eventually(func() bool {
				Expect(k8sEnv.K8sClient.Get(k8sEnv.Ctx, backupKey, &createdBackup)).Should(Succeed())
				cond = k8s.FindCondition(createdBackup.Status.Conditions, k8s.Ready)
				return k8s.ConditionStatusEquals(cond, metav1.ConditionTrue) ||
					k8s.ConditionReasonEquals(cond, k8s.BackupFailed)
			}, 10*time.Minute, 5*time.Second).Should(Equal(true))
			log.Info("Backup status", "status", cond.Status, "message", cond.Message)
			Expect(cond.Reason).Should(Equal(k8s.BackupReady))

			var snapshots snapv1.VolumeSnapshotList
			Expect(k8sEnv.K8sClient.List(k8sEnv.Ctx, &snapshots, client.InNamespace(namespace))).Should(Succeed())
			Expect(len(snapshots.Items)).Should(Equal(2))
			for _, snapshot := range snapshots.Items {
				Expect(*snapshot.Status.ReadyToUse).To(BeTrue())
			}

			By("By restoring snapshot backup to instance")
			restoreSpec := &v1alpha1.RestoreSpec{
				BackupType:       "Snapshot",
				BackupID:         createdBackup.Status.BackupID,
				Dop:              2,
				TimeLimitMinutes: 180,
				Force:            true,
				RequestTime:      metav1.Time{Time: time.Now()},
			}

			testhelpers.K8sUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
				instKey,
				createdInstance,
				func(obj *client.Object) {
					(*obj).(*v1alpha1.Instance).Spec.Restore = restoreSpec
				})

			// Wait until the instance is "Ready" (requires 5+ minutes to download image)
			By("By checking instance should be restored successfully")
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.RestoreComplete, 10*time.Minute)

			// Check databases are "Ready"
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, 10*time.Minute)

			//Verify if the restored instance contains the pre-backup data
			time.Sleep(30 * time.Second)
			testhelpers.VerifySimpleData(k8sEnv)
		})
	}

	Context("Oracle 19.3 EE", func() {
		snapBackupTest("19.3", "EE")
	})

	Context("Oracle 18c XE", func() {
		snapBackupTest("18c", "XE")
	})
})

func TestSnapshotBackup(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecsWithDefaultAndCustomReporters(t,
		t.Name(),
		[]Reporter{printer.NewlineReporter{}})
}
