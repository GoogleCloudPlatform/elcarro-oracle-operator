// Copyright 2022 Google LLC
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

package pitrtest

import (
	"fmt"
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	log "k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

var k8sEnv = testhelpers.K8sOperatorEnvironment{}

// In case of Ctrl-C clean up the last valid k8sEnv.
var _ = AfterSuite(func() {
	k8sEnv.Close()
})

var _ = Describe("PITR Test", func() {
	BeforeEach(func() {
		defer GinkgoRecover()
		namespace := testhelpers.RandName("pitr-test")
		k8sEnv.Init(namespace, namespace)

		// Allow the k8s [namespace/default] service account access to GCS buckets
		testhelpers.SetupServiceAccountBindingBetweenGcpAndK8s(k8sEnv)
	})

	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			testhelpers.PrintSimpleDebugInfo(k8sEnv, "mydb", "GCLOUD")
		}
		k8sEnv.Close()
	})

	instanceName := "mydb"
	pitrName := "mypitr"
	testPitr := func(version string, edition string) {
		It("Should perform PITR restore successfully", func() {
			By("By creating an instance")
			testhelpers.CreateSimpleInstance(k8sEnv, instanceName, version, edition)

			// Wait until the instance is "Ready" (requires 5+ minutes to download image)
			instKey := client.ObjectKey{Namespace: k8sEnv.DPNamespace, Name: instanceName}
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 20*time.Minute)

			By("By letting instance DB initialize")
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, 15*time.Minute)
			testhelpers.CreateSimplePDB(k8sEnv, instanceName)

			By("By creating a PITR object")
			pitr := &v1alpha1.PITR{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: k8sEnv.DPNamespace,
					Name:      pitrName,
					Labels: map[string]string{
						"instance": instanceName,
					},
				},
				Spec: v1alpha1.PITRSpec{
					InstanceRef: &v1alpha1.InstanceReference{Name: instanceName},
					Images: map[string]string{
						"agent": testhelpers.PitrAgentImage,
					},
					StorageURI: fmt.Sprintf("gs://%s/%s/%s",
						os.Getenv("PROW_PROJECT"), os.Getenv("PROW_CLUSTER"), k8sEnv.DPNamespace),
				},
			}
			testhelpers.K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, pitr)

			By("By checking pitr object is created and ready")
			pitrKey := client.ObjectKey{Namespace: k8sEnv.DPNamespace, Name: pitrName}

			var createdPITR v1alpha1.PITR
			var cond *metav1.Condition
			// Wait until the pitr is ready.
			Eventually(func() bool {
				Expect(k8sEnv.K8sClient.Get(k8sEnv.Ctx, pitrKey, &createdPITR)).Should(Succeed())
				cond = k8s.FindCondition(createdPITR.Status.Conditions, k8s.Ready)
				return k8s.ConditionStatusEquals(cond, metav1.ConditionTrue)
			}, 10*time.Minute, 5*time.Second).Should(Equal(true))
			log.Info("PITR status", "status", cond.Status, "message", cond.Message)
			Expect(cond.Reason).Should(Equal(k8s.CreateComplete))

			testhelpers.InsertSimpleData(k8sEnv)

			// Fetch current timestamp
			time.Sleep(10 * time.Second)
			out := testhelpers.K8sExecuteSqlOrFail("mydb-sts-0", k8sEnv.DPNamespace, `SELECT TO_CHAR (SYSDATE, 'YYYY-MM-DD"T"HH24:MI:SS"Z"') as now from dual;`)
			timestamp, err := time.Parse(time.RFC3339, out)
			Expect(err).NotTo(HaveOccurred())

			// Manually switch log
			time.Sleep(20 * time.Second)
			Expect(testhelpers.K8sExecuteSqlOrFail("mydb-sts-0", k8sEnv.DPNamespace, "ALTER SYSTEM ARCHIVE LOG CURRENT;")).To(Equal(""))

			text := fmt.Sprintf("By waiting until timestamp %v is included in pitr recover window", timestamp.Unix())
			By(text)
			Eventually(func() bool {
				Expect(k8sEnv.K8sClient.Get(k8sEnv.Ctx, pitrKey, &createdPITR)).Should(Succeed())
				return isTimestampRecoverable(timestamp, createdPITR)
			}, 10*time.Minute, 5*time.Second).Should(Equal(true))

			By("By restoring through PITR")
			createdInstance := &v1alpha1.Instance{}
			instKey = client.ObjectKey{Namespace: k8sEnv.DPNamespace, Name: instanceName}
			timev1 := metav1.NewTime(timestamp)
			restoreSpec := &v1alpha1.RestoreSpec{
				BackupType: commonv1alpha1.BackupTypePhysical,
				PITRRestore: &v1alpha1.PITRRestoreSpec{
					Timestamp: &timev1,
				},
				Force:       true,
				RequestTime: metav1.NewTime(time.Now()),
			}

			testhelpers.K8sUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
				instKey,
				createdInstance,
				func(obj *client.Object) {
					(*obj).(*v1alpha1.Instance).Spec.Restore = restoreSpec
				})

			// Wait until the instance is "Ready"
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.RestoreComplete, 20*time.Minute)

			// Check databases are "Ready"
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, 10*time.Minute)

			testhelpers.VerifySimpleData(k8sEnv)
		})
	}

	Context("Oracle 19c", func() {
		testPitr("19.3", "EE")
	})
})

func TestPITR(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecsWithDefaultAndCustomReporters(t,
		t.Name(),
		[]Reporter{printer.NewlineReporter{}})
}

func isTimestampRecoverable(timestamp time.Time, pitr v1alpha1.PITR) bool {
	if len(pitr.Status.AvailableRecoveryWindowTime) == 0 {
		return false
	}
	for _, window := range pitr.Status.AvailableRecoveryWindowTime {
		if timestamp.After(window.Begin.Time) && timestamp.Before(window.End.Time) {
			return true
		}
	}
	return false
}
