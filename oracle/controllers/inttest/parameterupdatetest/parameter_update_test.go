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

package parameterupdatetest

import (
	"fmt"
	"math/rand"
	"strconv"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	// Enable GCP auth for k8s client
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

func TestParameterUpdate(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ParameterUpdate")
}

// Global variable, to be accessible by AfterSuite
var k8sEnv = testhelpers.K8sOperatorEnvironment{}

// In case of Ctrl-C clean up the last valid k8sEnv.
var _ = AfterSuite(func() {
	k8sEnv.Close()
})

var _ = Describe("ParameterUpdate", func() {
	log := logf.FromContext(nil)
	pod := "mydb-sts-0"
	instanceName := "mydb"

	BeforeEach(func() {
		defer GinkgoRecover()
		nameSpace := testhelpers.RandName("parameter-update-test")
		k8sEnv.Init(nameSpace)
	})

	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			testhelpers.PrintSimpleDebugInfo(k8sEnv, instanceName, "GCLOUD")
		}
		k8sEnv.Close()
	})

	TestParameterUpdateForCorrectParameters := func(version string, edition string) {
		It("Test Successful Parameter update", func() {
			testhelpers.CreateSimpleInstance(k8sEnv, instanceName, version, edition)

			// Wait until DatabaseInstanceReady = True
			instKey := client.ObjectKey{Namespace: k8sEnv.Namespace, Name: instanceName}
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, 20*time.Minute)

			// Create PDB
			testhelpers.CreateSimplePDB(k8sEnv, instanceName)

			By("DB is ready, initiating parameter update")

			// Generate a random value for the parameter whose max is 100
			randVal := strconv.Itoa(1 + (rand.New(rand.NewSource(time.Now().UnixNano() / 1000)).Intn(100)))

			createdInstance := &v1alpha1.Instance{}
			testhelpers.K8sGetAndUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
				instKey,
				createdInstance,
				func(obj *client.Object) {
					instanceToUpdate := (*obj).(*v1alpha1.Instance)
					// Add the required parameters spec to the spec file
					oneHourBefore := metav1.NewTime(time.Now().Add(-1 * time.Hour))
					twoHours := metav1.Duration{Duration: 2 * time.Hour}
					instanceToUpdate.Spec.MaintenanceWindow = &commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{{Start: &oneHourBefore, Duration: &twoHours}}}

					instanceToUpdate.Spec.Parameters = map[string]string{
						"parallel_threads_per_cpu": randVal, // dynamic parameter
						"disk_asynch_io":           "true",  // static parameter
					}
				})

			// Verify the controller is getting into ParameterUpdateInProgress state
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionFalse, k8s.ParameterUpdateInProgress, 60*time.Second)

			// Wait until the instance settles into "Ready" again
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 20*time.Minute)

			Expect(fetchParameterValue(pod, "parallel_threads_per_cpu")).To(Equal(randVal))
			Expect(fetchParameterValue(pod, "disk_asynch_io")).To(Equal("TRUE"))
		})
	}

	TestParameterUpdateFailureAndRollback := func(version string, edition string) {
		It("Test parameter update failure and rollback", func() {
			testhelpers.CreateSimpleInstance(k8sEnv, instanceName, version, edition)

			// Wait until DatabaseInstanceReady = True
			instKey := client.ObjectKey{Namespace: k8sEnv.Namespace, Name: instanceName}
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, 15*time.Minute)

			// Create PDB
			testhelpers.CreateSimplePDB(k8sEnv, instanceName)
			By("DB is ready, initiating parameter update")

			createdInstance := &v1alpha1.Instance{}
			parallelThreadCountPreUpdate := fetchParameterValue(pod, "parallel_threads_per_cpu")
			diskAsyncValuePreUpdate := fetchParameterValue(pod, "disk_asynch_io")
			memMaxTargetPreUpdate := fetchParameterValue(pod, "memory_max_target")

			testhelpers.K8sGetAndUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
				instKey,
				createdInstance,
				func(obj *client.Object) {
					instanceToUpdate := (*obj).(*v1alpha1.Instance)
					// Add the required parameters spec to the spec file
					oneHourBefore := metav1.NewTime(time.Now().Add(-1 * time.Hour))
					twoHours := metav1.Duration{Duration: 2 * time.Hour}
					instanceToUpdate.Spec.MaintenanceWindow = &commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{{Start: &oneHourBefore, Duration: &twoHours}}}
					// Generate a random value for the parameter whose max is 100
					randVal := strconv.Itoa(rand.New(rand.NewSource(time.Now().UnixNano() / 1000)).Intn(100))
					log.Info("The generated random value is ", "rand val", randVal)

					instanceToUpdate.Spec.Parameters = map[string]string{
						"parallel_threads_per_cpu": randVal, // dynamic parameter
						"disk_asynch_io":           "true",  // static parameter
						"memory_max_target":        "1",     // bad static parameter value.
					}
				})

			// Verify the controller is getting into ParameterUpdateInProgress state
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionFalse, k8s.ParameterUpdateInProgress, 10*time.Second)

			// Verify the instance transitions to ParameterUpdateRollback state due to database unable to restart.
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionFalse, k8s.ParameterUpdateRollback, 5*time.Minute)
			// Wait until the instance settles into "Ready" again
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 20*time.Minute)

			// Verify rollback by checking current parameter value is equivalent to pre-update value.
			Expect(fetchParameterValue(pod, "parallel_threads_per_cpu")).To(Equal(parallelThreadCountPreUpdate))
			Expect(fetchParameterValue(pod, "disk_asynch_io")).To(Equal(diskAsyncValuePreUpdate))
			Expect(fetchParameterValue(pod, "memory_max_target")).To(Equal(memMaxTargetPreUpdate))
		})
	}

	Context("Oracle 12.2 EE", func() {
		TestParameterUpdateForCorrectParameters("12.2", "EE")
		TestParameterUpdateFailureAndRollback("12.2", "EE")
	})

	Context("Oracle 19.3 EE", func() {
		TestParameterUpdateForCorrectParameters("19.3", "EE")
		TestParameterUpdateFailureAndRollback("19.3", "EE")
	})

	Context("Oracle 18c XE", func() {
		TestParameterUpdateForCorrectParameters("18c", "XE")
		TestParameterUpdateFailureAndRollback("18c", "XE")
	})
})

func fetchParameterValue(pod string, parameter string) string {
	out := testhelpers.K8sExecuteSqlOrFail(pod, k8sEnv.Namespace, fmt.Sprintf("SHOW PARAMETERS %s;", parameter))
	// The output of the above query is
	// parallel_threads_per_cpu\t     integer\t 1000
	// The following command extract the required last column
	// The alternate approach to query system parameter(shown below) doesn't seem to work with bash due the dollar symbol
	// SELECT value from v$parameter where name='parallel_servers_target'
	shellCmd := "echo '%s' | sed 's/ //g' | tr -s '\\t' | tr '\\t' '|' |  cut -d '|' -f3"
	out, _ = testhelpers.K8sExec(pod, k8sEnv.Namespace, "oracledb", fmt.Sprintf(shellCmd, out))
	return out
}
