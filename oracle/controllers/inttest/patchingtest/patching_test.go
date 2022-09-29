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

package patchingtest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	// Enable GCP auth for k8s client
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

func TestPatching(t *testing.T) {
	klog.SetOutput(GinkgoWriter)
	logf.SetLogger(klogr.NewWithOptions(klogr.WithFormat(klogr.FormatKlog)))
	RegisterFailHandler(Fail)
	RunSpecsWithDefaultAndCustomReporters(t,
		t.Name(),
		[]Reporter{printer.NewlineReporter{}})
}

// Global variable, to be accessible by AfterSuite
var k8sEnv = testhelpers.K8sOperatorEnvironment{}

// In case of Ctrl-C clean up the last valid k8sEnv.
var _ = AfterSuite(func() {
	k8sEnv.Close()
})

var _ = Describe("Patching", func() {
	klog.SetOutput(GinkgoWriter)
	logf.SetLogger(klogr.NewWithOptions(klogr.WithFormat(klogr.FormatKlog)))
	pod := "mydb-sts-0"
	instanceName := "mydb"

	BeforeEach(func() {
		defer GinkgoRecover()
		namespace := testhelpers.RandName("patching-test")
		k8sEnv.Init(namespace, namespace)
	})

	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			testhelpers.PrintLogs(k8sEnv.CPNamespace, k8sEnv.DPNamespace, k8sEnv.Env, []string{"manager", "dbdaemon", "oracledb"}, []string{instanceName})
			testhelpers.PrintClusterObjects()
		}
		k8sEnv.Close()
	})

	TestNormalPatchingFlow := func(version, edition, startingPatchNumber, targetPatchNumber string) {
		It("Test Normal Patching Flow", func() {

			createInstanceAndVerifyPrePatchVersion(instanceName, pod, version, edition, startingPatchNumber)

			By("DB is ready, starting normal patching")
			// Start applying patching image
			createdInstance := &v1alpha1.Instance{}
			instKey := client.ObjectKey{Namespace: k8sEnv.DPNamespace, Name: instanceName}

			testhelpers.K8sUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
				instKey,
				createdInstance,
				func(obj *client.Object) {
					instanceToUpdate := (*obj).(*v1alpha1.Instance)
					oneHourBefore := metav1.NewTime(time.Now().Add(-1 * time.Hour))
					twoHours := metav1.Duration{Duration: 2 * time.Hour}
					instanceToUpdate.Spec.MaintenanceWindow = &commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{{Start: &oneHourBefore, Duration: &twoHours}}}
					instanceToUpdate.Spec.Images["service"] = testhelpers.TestImageForVersion(version, edition, "unseeded-"+targetPatchNumber)
					instanceToUpdate.Spec.Services = map[commonv1alpha1.Service]bool{"Patching": true}
				})

			// Give controller some time to process
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionFalse, k8s.PatchingBackupStarted, 45*time.Second)

			// Wait until the instance is "Ready" again
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 25*time.Minute)

			// Check post-patch oracle binary version
			out, err := testhelpers.K8sExec(pod, k8sEnv.DPNamespace, "oracledb", "source ~/GCLOUD.env; $ORACLE_HOME/OPatch/opatch lspatches")
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(Equal(getExpectedOPatchMessage(targetPatchNumber)))
			// Check post-patch oracle DB version
			out = testhelpers.K8sExecuteSqlOrFail(pod, k8sEnv.DPNamespace, "SELECT patch_id FROM sys.dba_registry_sqlpatch ORDER by action_time;")
			Expect(out).To(Equal(fmt.Sprintf("%s\n  %s", startingPatchNumber, targetPatchNumber)))

			// Check that test data is still there
			testhelpers.VerifySimpleData(k8sEnv)
		})
	}

	TestFaultyPatchingFlow := func(version, edition, startingPatchNumber, targetPatchNumber string) {
		It("Test Faulty Datapatch", func() {

			createInstanceAndVerifyPrePatchVersion(instanceName, pod, version, edition, startingPatchNumber)

			By("DB is ready, starting patching with datapatch removed")
			// Start applying patching image
			createdInstance := &v1alpha1.Instance{}
			instKey := client.ObjectKey{Namespace: k8sEnv.DPNamespace, Name: instanceName}

			testhelpers.K8sUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
				instKey,
				createdInstance,
				func(obj *client.Object) {
					instanceToUpdate := (*obj).(*v1alpha1.Instance)
					oneHourBefore := metav1.NewTime(time.Now().Add(-1 * time.Hour))
					twoHours := metav1.Duration{Duration: 2 * time.Hour}
					instanceToUpdate.Spec.MaintenanceWindow = &commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{{Start: &oneHourBefore, Duration: &twoHours}}}
					instanceToUpdate.Spec.Images["service"] = testhelpers.TestImageForVersion(version, edition, "seeded-buggy")
					instanceToUpdate.Spec.Services = map[commonv1alpha1.Service]bool{commonv1alpha1.Patching: true}
				})

			// Give controller some time to process
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionFalse, k8s.PatchingBackupStarted, 45*time.Second)

			// Wait until the instance is "Ready" again
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.PatchingRecoveryCompleted, 25*time.Minute)

			// Check post-patch oracle binary version
			// Need to wait until Oracle starts up
			Eventually(func() string {
				out, _ := testhelpers.K8sExec(pod, k8sEnv.DPNamespace, "oracledb", "source ~/GCLOUD.env; $ORACLE_HOME/OPatch/opatch lspatches")
				return strings.TrimSpace(out)
			}, 5*time.Minute, 5*time.Second).Should(Equal(getExpectedOPatchMessage(startingPatchNumber)))

			// Check post-patch oracle DB version
			out := testhelpers.K8sExecuteSqlOrFail(pod, k8sEnv.DPNamespace, "SELECT patch_id FROM sys.dba_registry_sqlpatch ORDER by action_time;")
			Expect(out).To(Equal(startingPatchNumber))

			// Check that test data is still there
			testhelpers.VerifySimpleData(k8sEnv)
		})
	}

	//Patching is not supported for Oracle 18c XE
	Context("Normal Patching - Oracle 19.3 EE", func() {
		TestNormalPatchingFlow("19.3", "EE", "32218454", "32545013")
	})

	Context("Faulty Patching - Oracle 19.3 EE", func() {
		TestFaultyPatchingFlow("19.3", "EE", "32218454", "32545013")
	})
})

func createInstanceAndVerifyPrePatchVersion(instanceName, pod, version, edition, startingPatchNumber string) {
	testhelpers.CreateSimpleInstance(k8sEnv, instanceName, version, edition)

	// Wait until DatabaseInstanceReady = True
	instKey := client.ObjectKey{Namespace: k8sEnv.DPNamespace, Name: instanceName}
	testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, 15*time.Minute)

	// Create PDB
	testhelpers.CreateSimplePDB(k8sEnv, instanceName)

	// Check pre-patch oracle binary version
	out, err := testhelpers.K8sExec(pod, k8sEnv.DPNamespace, "oracledb", "source ~/GCLOUD.env; $ORACLE_HOME/OPatch/opatch lspatches")
	Expect(err).NotTo(HaveOccurred())
	Expect(out).To(Equal(getExpectedOPatchMessage(startingPatchNumber)))
	// Check pre-patch oracle SQL patch version
	out = testhelpers.K8sExecuteSqlOrFail(pod, k8sEnv.DPNamespace, "SELECT patch_id FROM sys.dba_registry_sqlpatch ORDER by action_time;")
	Expect(out).To(Equal(startingPatchNumber))
	// Insert test data
	testhelpers.InsertSimpleData(k8sEnv)
}

// Returns the output that we expect to receive after executing $ORACLE_HOME/OPatch/opatch lspatches
func getExpectedOPatchMessage(patchNumber string) string {
	switch patchNumber {
	case "31312468":
		return "31312468;Database Jul 2020 Release Update : 12.2.0.1.200714 (31312468)\n\nOPatch succeeded."
	case "31741641":
		return "31741641;Database Oct 2020 Release Update : 12.2.0.1.201020 (31741641)\n\nOPatch succeeded."
	case "32218454":
		return "32218454;Database Release Update : 19.10.0.0.210119 (32218454)\n29585399;OCW RELEASE UPDATE 19.3.0.0.0 (29585399)\n\nOPatch succeeded."
	case "32545013":
		return "32545013;Database Release Update : 19.11.0.0.210420 (32545013)\n29585399;OCW RELEASE UPDATE 19.3.0.0.0 (29585399)\n\nOPatch succeeded."
	default:
		return "INVALID_PATCH_NUMBER"
	}
}
