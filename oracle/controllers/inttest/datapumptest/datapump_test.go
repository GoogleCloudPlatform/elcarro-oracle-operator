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

package datapumptest

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"

	// Enable GCP auth for k8s client
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

// Global variable, to be accessible by AfterSuite
var k8sEnv = testhelpers.K8sOperatorEnvironment{}

// In case of Ctrl-C clean up the last valid k8sEnv.
var _ = AfterSuite(func() {
	k8sEnv.Close()
})

var _ = Describe("Datapump", func() {
	var namespace string
	var instanceName = "mydb"
	var pod = instanceName + "-sts-0"

	BeforeEach(func() {
		defer GinkgoRecover()
		namespace = testhelpers.RandName("datapump-test")
		k8sEnv.Init(namespace, namespace)

		// Allow the k8s [namespace/default] service account access to GCS buckets
		testhelpers.SetupServiceAccountBindingBetweenGcpAndK8s(k8sEnv)
	})

	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			testhelpers.PrintSimpleDebugInfo(k8sEnv, instanceName, "GCLOUD")
		}
		k8sEnv.Close()
	})

	testDataPump := func(version string, edition string) {
		It("Should create instance and export data", func() {
			testhelpers.CreateSimpleInstance(k8sEnv, instanceName, version, edition)

			instKey := client.ObjectKey{Namespace: k8sEnv.DPNamespace, Name: instanceName}
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, 20*time.Minute)

			testhelpers.CreateSimplePDB(k8sEnv, instanceName)
			testhelpers.InsertSimpleData(k8sEnv)

			By("Creating a new Schema export")
			schemaExport := &v1alpha1.Export{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      "export-schemas",
				},
				Spec: v1alpha1.ExportSpec{
					Instance:         instanceName,
					DatabaseName:     "pdb1",
					Type:             "DataPump",
					ExportObjectType: "Schemas",
					ExportObjects:    []string{"scott"},
					FlashbackTime:    &metav1.Time{Time: time.Now()},
					GcsPath: fmt.Sprintf("gs://%s/%s/%s/exportSchema.dmp",
						os.Getenv("PROW_PROJECT"), os.Getenv("PROW_CLUSTER"), namespace),
				},
			}
			testhelpers.K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, schemaExport)

			By("Waiting for export to complete")
			{
				createdExport := &v1alpha1.Export{}
				objKey := client.ObjectKey{Namespace: k8sEnv.CPNamespace, Name: schemaExport.Name}
				testhelpers.WaitForObjectConditionState(k8sEnv,
					objKey, createdExport, k8s.Ready, metav1.ConditionTrue, k8s.ExportComplete, 5*time.Minute)
			}

			By("Creating a new Table export")
			tableExport := &v1alpha1.Export{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      "export-tables",
				},
				Spec: v1alpha1.ExportSpec{
					Instance:         instanceName,
					DatabaseName:     "pdb1",
					Type:             "DataPump",
					ExportObjectType: "Tables",
					ExportObjects:    []string{"scott.test_table"},
					FlashbackTime:    &metav1.Time{Time: time.Now()},
					GcsPath: fmt.Sprintf("gs://%s/%s/%s/exportTables.dmp",
						os.Getenv("PROW_PROJECT"), os.Getenv("PROW_CLUSTER"), namespace),
				},
			}
			testhelpers.K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, tableExport)

			By("Waiting for export to complete")
			{
				createdExport := &v1alpha1.Export{}
				objKey := client.ObjectKey{Namespace: k8sEnv.CPNamespace, Name: tableExport.Name}
				testhelpers.WaitForObjectConditionState(k8sEnv,
					objKey, createdExport, k8s.Ready, metav1.ConditionTrue, k8s.ExportComplete, 5*time.Minute)
			}

			By("Erasing scott user")
			sql := `alter session set container=pdb1;
drop user scott cascade;`
			testhelpers.K8sExecuteSqlOrFail(pod, k8sEnv.CPNamespace, sql)

			By("Importing Schemas")
			schemaImport := &v1alpha1.Import{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      "import-schemas",
				},
				Spec: v1alpha1.ImportSpec{
					Instance:     instanceName,
					DatabaseName: "pdb1",
					GcsPath:      schemaExport.Spec.GcsPath,
				},
			}
			testhelpers.K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, schemaImport)

			By("Waiting for schema import to complete")
			{
				createdImport := &v1alpha1.Import{}
				objKey := client.ObjectKey{Namespace: k8sEnv.CPNamespace, Name: schemaImport.Name}
				testhelpers.WaitForObjectConditionState(k8sEnv,
					objKey, createdImport, k8s.Ready, metav1.ConditionTrue, k8s.ImportComplete, 5*time.Minute)
			}

			By("Granting unlimited tablespace to scott")
			sql = `alter session set container=pdb1;
grant unlimited tablespace to scott;
alter session set current_schema=scott;
drop table test_table;`
			testhelpers.K8sExecuteSqlOrFail(pod, k8sEnv.CPNamespace, sql)

			By("Importing Tables")
			tableImport := &v1alpha1.Import{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      "import-tables",
				},
				Spec: v1alpha1.ImportSpec{
					Instance:     instanceName,
					DatabaseName: "pdb1",
					GcsPath:      tableExport.Spec.GcsPath,
				},
			}
			testhelpers.K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, tableImport)
			By("Waiting for table import to complete")
			{
				createdImport := &v1alpha1.Import{}
				objKey := client.ObjectKey{Namespace: k8sEnv.CPNamespace, Name: tableImport.Name}
				testhelpers.WaitForObjectConditionState(k8sEnv,
					objKey, createdImport, k8s.Ready, metav1.ConditionTrue, k8s.ImportComplete, 5*time.Minute)
			}

			testhelpers.VerifySimpleData(k8sEnv)
		})
	}

	Context("Oracle 19c", func() {
		testDataPump("19.3", "EE")
	})

	Context("Oracle 18c XE", func() {
		testDataPump("18c", "XE")
	})
})

func TestDataPump(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecsWithDefaultAndCustomReporters(t,
		t.Name(),
		[]Reporter{printer.NewlineReporter{}})
}
