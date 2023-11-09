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

package standbydatamigrationtest

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/instancecontroller"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	// Enable GCP auth for k8s client
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

const testPassword = "testPASS-123"

// Global variable, to be accessible by AfterSuite
var primaryK8sEnv = testhelpers.K8sOperatorEnvironment{}
var standbyK8sEnv = testhelpers.K8sOperatorEnvironment{}
var log = logf.Log

// Initial setup before test suite.
var _ = BeforeSuite(func() {
	// Note that these GSM + WI setup steps are re-runnable.
	// If the env fulfills, no error should occur.
	testhelpers.EnableGsmApi()
	testhelpers.EnableIamApi()
})

// In case of Ctrl-C clean up the last valid k8sEnv.
var _ = AfterSuite(func() {
	primaryK8sEnv.Close()
	standbyK8sEnv.Close()
})

var _ = Describe("StandbyDataMigration", func() {
	var primaryInstanceName = "primary"
	var primaryPod = primaryInstanceName + "-sts-0"
	var secretName string

	var standbyInstanceName = "standby"
	var standbyPod = standbyInstanceName + "-sts-0"

	BeforeEach(func() {
		primaryNamespace := testhelpers.RandName("standbydatamigration-test-primary")
		secretName = fmt.Sprintf("%s-secret", primaryNamespace)
		primaryK8sEnv.Init(primaryNamespace, primaryNamespace)

		standbyNamespace := testhelpers.RandName("standbydatamigration-test-standby")
		standbyK8sEnv.Init(standbyNamespace, standbyNamespace)

		// Allow the k8s [namespace/default] service account access to GCS buckets
		testhelpers.SetupServiceAccountBindingBetweenGcpAndK8s(standbyK8sEnv)
	})

	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			testhelpers.PrintLogs(standbyK8sEnv.CPNamespace, standbyK8sEnv.DPNamespace, standbyK8sEnv.Env,
				[]string{"manager", "dbdaemon", "oracledb"}, []string{standbyInstanceName})
			testhelpers.PrintClusterObjects()
		}
		primaryK8sEnv.Close()
		standbyK8sEnv.Close()
	})

	testStandbyDataMigration := func(version string, edition string) {
		It("Should migrate primary instance data to standby instance", func() {
			By("Creating a primary instance and pdb")
			testhelpers.CreateSimpleInstance(primaryK8sEnv, primaryInstanceName, version, edition)

			primaryKey := client.ObjectKey{Namespace: primaryK8sEnv.DPNamespace, Name: primaryInstanceName}
			standbyKey := client.ObjectKey{Namespace: standbyK8sEnv.DPNamespace, Name: standbyInstanceName}
			testhelpers.WaitForInstanceConditionState(
				primaryK8sEnv,
				primaryKey,
				k8s.DatabaseInstanceReady,
				metav1.ConditionTrue,
				k8s.CreateComplete,
				20*time.Minute)

			testhelpers.CreateSimplePDB(primaryK8sEnv, primaryInstanceName)

			By("Inserting data1 into primary DB")
			testhelpers.InsertData(primaryPod, primaryK8sEnv.DPNamespace, "pdb1", "scott", "test_table1", "Hello World 1")

			By("Setting up primary Database")
			setUpPrimary(primaryPod, primaryK8sEnv.DPNamespace)

			By("Staging the password file to GCS")
			gcs := stagePasswordFile(primaryPod, primaryK8sEnv.DPNamespace)

			By("Creating a secret for sysdba credential")
			createSecret(secretName, testPassword)
			defer deleteSecret(secretName)
			grantSecretAccess(secretName)

			By("Creating a standby instance")
			primaryInstance := &v1alpha1.Instance{}
			testhelpers.K8sGetWithRetry(primaryK8sEnv.K8sClient,
				primaryK8sEnv.Ctx,
				primaryKey,
				primaryInstance)
			primaryHost := strings.SplitN(primaryInstance.Status.URL, ":", 2)[0]

			// Standbies can only be created from unseeded images.
			createStandbyInstance(standbyK8sEnv,
				primaryHost, "GCLOUD", secretName, gcs,
				standbyInstanceName, version, edition, "unseeded")

			testhelpers.WaitForObjectConditionState(standbyK8sEnv,
				standbyKey,
				&v1alpha1.Instance{},
				k8s.StandbyDRReady,
				metav1.ConditionFalse,
				k8s.StandbyDRDataGuardReplicationInProgress,
				time.Minute*25,
				func(conditions []metav1.Condition, name string) (bool, *metav1.Condition) {
					for i, c := range conditions {
						if c.Type == name {
							// For standby data migration, StandbyDRCreateFailed is the final error state.
							return c.Reason == k8s.StandbyDRCreateFailed, &conditions[i]
						}
					}
					return false, nil
				})

			By("Inserting data2 into primary DB")
			testhelpers.InsertData(primaryPod, primaryK8sEnv.DPNamespace, "pdb1", "scott", "test_table2", "Hello World 2")

			// wait to ensure the status is refreshed
			time.Sleep(instancecontroller.StandbyReconcileInterval * 2)
			waitStandbySync(standbyK8sEnv, standbyKey)

			By("Promoting the standby instance")
			promoteStandby(standbyK8sEnv, standbyKey)

			testhelpers.WaitForInstanceConditionState(standbyK8sEnv,
				standbyKey,
				k8s.StandbyDRReady,
				metav1.ConditionTrue,
				k8s.StandbyDRBootstrapCompleted,
				time.Minute*10)

			testhelpers.WaitForInstanceConditionState(
				standbyK8sEnv,
				standbyKey,
				k8s.DatabaseInstanceReady,
				metav1.ConditionTrue,
				k8s.CreateComplete,
				time.Minute*5)

			By("Verifying data is replicated to promoted standby")
			testhelpers.VerifyData(standbyPod, standbyK8sEnv.DPNamespace, "pdb1", "scott", "test_table1", "Hello World 1")
			testhelpers.VerifyData(standbyPod, standbyK8sEnv.DPNamespace, "pdb1", "scott", "test_table2", "Hello World 2")

			By("Verifying an object was created for the migrated PDB")
			migratedPDB := &v1alpha1.Database{}
			testhelpers.K8sGetWithRetry(standbyK8sEnv.K8sClient,
				standbyK8sEnv.Ctx,
				client.ObjectKey{Namespace: standbyK8sEnv.DPNamespace, Name: "pdb1"},
				migratedPDB)
		})
	}

	Context("Oracle 19c", func() {
		testStandbyDataMigration("19.3", "EE")
	})
})

func setUpPrimary(pod, ns string) {
	testhelpers.K8sExecuteSqlOrFail(
		pod,
		ns,
		fmt.Sprintf(`alter user sys identified by "%s";
alter database force logging;
alter system set dg_broker_start=true scope=both;
alter database add standby logfile thread 1 group 10 size 1024M;
alter database add standby logfile thread 1 group 11 size 1024M;
alter database add standby logfile thread 1 group 12 size 1024M;
alter database add standby logfile thread 1 group 13 size 1024M;
`, testPassword))
}

func stagePasswordFile(pod, ns string) string {
	tmpDir, err := ioutil.TempDir("", "standbydatamigration")
	defer os.RemoveAll(tmpDir)
	Expect(err).NotTo(HaveOccurred())
	passwordFile := "orapwGCLOUD"
	localPath := filepath.Join(tmpDir, passwordFile)
	testhelpers.K8sCopyFromPodOrFail(pod, ns, "oracledb", filepath.Join("/u02/app/oracle/oraconfig/GCLOUD", passwordFile), localPath)
	bucket := os.Getenv("PROW_PROJECT")
	object := fmt.Sprintf("%s/%s/%s", os.Getenv("PROW_CLUSTER"), ns, passwordFile)
	testhelpers.UploadFileOrFail(localPath, bucket, object)
	return fmt.Sprintf("gs://%s/%s", bucket, object)
}

func createSecret(name, val string) {
	tmpFile, err := ioutil.TempFile("", "standbydatamigration")
	Expect(err).NotTo(HaveOccurred())
	defer os.Remove(tmpFile.Name())
	_, err = tmpFile.WriteString(val)
	Expect(err).NotTo(HaveOccurred())
	// Create secret for primary sysdba password.
	cmd := exec.Command("gcloud", "secrets", "create", name, "--replication-policy=automatic", "--data-file="+tmpFile.Name(), "--project="+os.Getenv("PROW_PROJECT"))
	out, err := cmd.CombinedOutput()
	log.Info("gcloud create secret", "output", out)
	Expect(err).NotTo(HaveOccurred())
}

func deleteSecret(name string) {
	cmd := exec.Command("gcloud", "secrets", "delete", name, "--quiet", "--project="+os.Getenv("PROW_PROJECT"))
	out, err := cmd.CombinedOutput()
	log.Info("gcloud delete secret", "output", out)
	Expect(err).NotTo(HaveOccurred())
}

func grantSecretAccess(name string) {
	// Grant GSM secret access role to the our test service account.
	Expect(retry.OnError(retry.DefaultBackoff, func(error) bool { return true }, func() error {
		cmd := exec.Command("gcloud",
			"secrets", "add-iam-policy-binding", name, "--role=roles/secretmanager.secretAccessor",
			"--member="+"serviceAccount:"+testhelpers.GCloudServiceAccount(), "--project="+os.Getenv("PROW_PROJECT"))
		out, err := cmd.CombinedOutput()
		log.Info("gcloud secrets service-accounts add-iam-policy-binding", "output", string(out))
		return err
	})).To(Succeed())
}

func createStandbyInstance(k8sEnv testhelpers.K8sOperatorEnvironment,
	primaryHost, primaryService, primarySecret, passwordURI,
	instanceName, version, edition, extra string) {
	instance := &v1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName,
			Namespace: k8sEnv.DPNamespace,
		},
		Spec: v1alpha1.InstanceSpec{
			CDBName:      "GCLOUD",
			DBUniqueName: "GCLOUD_standby",
			ReplicationSettings: &v1alpha1.ReplicationSettings{
				PrimaryHost:        primaryHost,
				PrimaryPort:        consts.SecureListenerPort,
				PrimaryServiceName: primaryService,
				PrimaryUser: commonv1alpha1.UserSpec{
					Name: "sys",
					CredentialSpec: commonv1alpha1.CredentialSpec{
						GsmSecretRef: &commonv1alpha1.GsmSecretReference{
							ProjectId: os.Getenv("PROW_PROJECT"),
							SecretId:  primarySecret,
							Version:   "1",
						},
					},
				},
				PasswordFileURI: passwordURI,
			},
			InstanceSpec: commonv1alpha1.InstanceSpec{
				Version: version,
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
				DatabaseResources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("9Gi"),
					},
				},
				Images: map[string]string{
					"service": testhelpers.TestImageForVersion(version, edition, extra),
				},
				DBLoadBalancerOptions: &commonv1alpha1.DBLoadBalancerOptions{
					GCP: commonv1alpha1.DBLoadBalancerOptionsGCP{LoadBalancerType: "Internal"},
				},
			},
		},
	}

	testhelpers.K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, instance)
}

func waitStandbySync(k8sEnv testhelpers.K8sOperatorEnvironment, key client.ObjectKey) {
	re := regexp.MustCompile(`Apply\s+Lag:\s+0\s+seconds`)
	Eventually(func() bool {
		standbyInstance := &v1alpha1.Instance{}
		testhelpers.K8sGetWithRetry(k8sEnv.K8sClient,
			k8sEnv.Ctx,
			key,
			standbyInstance)
		if standbyInstance.Status.DataGuardOutput == nil {
			return false
		}
		log.Info("Data Guard status", "output", standbyInstance.Status.DataGuardOutput.StatusOutput)
		for _, output := range standbyInstance.Status.DataGuardOutput.StatusOutput {
			if re.MatchString(output) {
				return true
			}
		}
		// retry
		return false
	},
		time.Minute*10,
		time.Second*30).Should(Equal(true))
}

func promoteStandby(k8sEnv testhelpers.K8sOperatorEnvironment, key client.ObjectKey) {
	testhelpers.K8sUpdateWithRetry(k8sEnv.K8sClient,
		k8sEnv.Ctx,
		key,
		&v1alpha1.Instance{},
		func(obj *client.Object) {
			instToUpdate := (*obj).(*v1alpha1.Instance)
			instToUpdate.Spec.ReplicationSettings = nil
		},
	)
}

func TestStandbyDataMigration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecsWithDefaultAndCustomReporters(t,
		t.Name(),
		[]Reporter{printer.NewlineReporter{}})
}
