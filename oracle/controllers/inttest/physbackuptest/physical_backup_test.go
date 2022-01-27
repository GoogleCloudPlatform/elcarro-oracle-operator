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

package physbackuptest

import (
	"fmt"
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

type backupTestCase struct {
	name         string
	contextTitle string
	instanceName string
	backupName   string
	instanceSpec v1alpha1.InstanceSpec
	backupSpec   v1alpha1.BackupSpec
}

// Made global to be accessible by AfterSuite
var k8sEnv = testhelpers.K8sOperatorEnvironment{}

// In case of Ctrl-C clean up the last valid k8sEnv.
var _ = AfterSuite(func() {
	k8sEnv.Close()
})

var _ = Describe("Instance and Database provisioning", func() {
	var namespace string
	dbResource := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("7Gi"),
		},
	}

	BeforeEach(func() {
		defer GinkgoRecover()
		namespace = testhelpers.RandName("physical-backup-test")
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

	BackupTest := func(tc backupTestCase) {
		Context(tc.contextTitle, func() {
			It("Should create rman based backup successfully", func() {
				log := logf.FromContext(nil)

				By("By creating an instance")
				instance := &v1alpha1.Instance{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tc.instanceName,
						Namespace: namespace,
					},
					Spec: tc.instanceSpec,
				}
				testhelpers.K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, instance)
				instKey := client.ObjectKey{Namespace: namespace, Name: tc.instanceName}

				// Wait until the instance is "Ready" (requires 5+ minutes to download image)
				testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 20*time.Minute)

				By("By letting instance DB initialize")
				testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, 15*time.Minute)

				testhelpers.CreateSimplePDB(k8sEnv, tc.instanceName)
				testhelpers.InsertSimpleData(k8sEnv)

				By("By creating a physical based backup")
				backupName := tc.backupName
				backup := &v1alpha1.Backup{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Name:      backupName,
					},
					Spec: tc.backupSpec,
				}
				testhelpers.K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, backup)

				By("By checking backup object is created and ready")
				backupKey := client.ObjectKey{Namespace: namespace, Name: backupName}

				var createdBackup v1alpha1.Backup
				var cond *metav1.Condition
				// Wait until the backup is ready or failed.
				Eventually(func() bool {
					Expect(k8sEnv.K8sClient.Get(k8sEnv.Ctx, backupKey, &createdBackup)).Should(Succeed())
					cond = k8s.FindCondition(createdBackup.Status.Conditions, k8s.Ready)
					return k8s.ConditionStatusEquals(cond, metav1.ConditionTrue) ||
						k8s.ConditionReasonEquals(cond, k8s.BackupFailed)
				}, 7*time.Minute, 5*time.Second).Should(Equal(true))
				log.Info("Backup status", "status", cond.Status, "message", cond.Message)
				Expect(cond.Reason).Should(Equal(k8s.BackupReady))

				By("By restoring an instance from backup")
				instKey = client.ObjectKey{Namespace: namespace, Name: tc.instanceName}
				testhelpers.K8sUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
					instKey,
					instance,
					func(obj *client.Object) {
						instanceToUpdate := (*obj).(*v1alpha1.Instance)
						instanceToUpdate.Spec.Restore = &v1alpha1.RestoreSpec{
							BackupType:  createdBackup.Spec.Type,
							BackupID:    createdBackup.Status.BackupID,
							Force:       true,
							RequestTime: metav1.NewTime(time.Now()),
						}
					},
				)

				// Wait until the instance is "Ready"
				testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.RestoreComplete, 20*time.Minute)

				// Check databases are "Ready"
				testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, 10*time.Minute)

				testhelpers.VerifySimpleData(k8sEnv)
			})
		})
	}

	Context("New backup through physical", func() {
		testCase := backupTestCase{
			name:         "default rman backup",
			instanceName: "mydb",
			backupName:   "phys",
			instanceSpec: v1alpha1.InstanceSpec{
				CDBName: "GCLOUD",
				InstanceSpec: commonv1alpha1.InstanceSpec{
					Disks: []commonv1alpha1.DiskSpec{
						{
							Name: "DataDisk",
							Size: resource.MustParse("45Gi"),
						},
						{
							Name: "LogDisk",
							Size: resource.MustParse("55Gi"),
						},
					},
					Images:            map[string]string{},
					DatabaseResources: dbResource,
				},
			},
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: "mydb",
					Type:     commonv1alpha1.BackupTypePhysical,
				},
			},
		}

		Context("Oracle 18c XE", func() {
			testCase.instanceSpec.Version = "18c"
			testCase.instanceSpec.Images = map[string]string{
				"service": testhelpers.TestImageForVersion("18c", "XE", ""),
			}
			BackupTest(testCase)
		})
	})

	Context("RMAN backup on datadisk", func() {
		testCase := backupTestCase{
			name:         "rman backup on datadisk",
			contextTitle: "rman backup on datadisk",
			instanceName: "mydb",
			backupName:   "phys",
			instanceSpec: v1alpha1.InstanceSpec{
				CDBName: "GCLOUD",
				InstanceSpec: commonv1alpha1.InstanceSpec{
					Disks: []commonv1alpha1.DiskSpec{
						{
							Name: "DataDisk",
							Size: resource.MustParse("45Gi"),
						},
						{
							Name: "LogDisk",
							Size: resource.MustParse("55Gi"),
						},
						{
							Name: "BackupDisk",
							Size: resource.MustParse("100Gi"),
						},
					},
					Images:            map[string]string{},
					DatabaseResources: dbResource,
				},
			},
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: "mydb",
					Type:     commonv1alpha1.BackupTypePhysical,
				},
				LocalPath: "/u04/app/oracle/rman",
			},
		}
		Context("Oracle 18c XE", func() {
			testCase.instanceSpec.Version = "18c"
			testCase.instanceSpec.Images = map[string]string{
				"service": testhelpers.TestImageForVersion("18c", "XE", ""),
			}
			BackupTest(testCase)
		})
	})

	Context("New GCS backup", func() {
		testCase := backupTestCase{
			name:         "GCS rman backup",
			instanceName: "mydb",
			backupName:   "phys",
			instanceSpec: v1alpha1.InstanceSpec{
				CDBName: "GCLOUD",
				InstanceSpec: commonv1alpha1.InstanceSpec{
					Disks: []commonv1alpha1.DiskSpec{
						{
							Name: "DataDisk",
							Size: resource.MustParse("45Gi"),
						},
						{
							Name: "LogDisk",
							Size: resource.MustParse("55Gi"),
						},
					},
					Images:            map[string]string{},
					DatabaseResources: dbResource,
				},
			},
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: "mydb",
					Type:     commonv1alpha1.BackupTypePhysical,
				},
				GcsPath: fmt.Sprintf("gs://%s/%s/%s/",
					os.Getenv("PROW_PROJECT"), os.Getenv("PROW_CLUSTER"), testhelpers.RandName("backup")),
			},
		}

		Context("Oracle 18c XE", func() {
			testCase.instanceSpec.Version = "18c"
			testCase.instanceSpec.Images = map[string]string{
				"service": testhelpers.TestImageForVersion("18c", "XE", ""),
			}
			BackupTest(testCase)
		})
		Context("Oracle 19.3 EE", func() {
			testCase.instanceSpec.Version = "19.3"
			testCase.instanceSpec.Images = map[string]string{
				"service": testhelpers.TestImageForVersion("19.3", "EE", ""),
			}
			BackupTest(testCase)
		})
	})

	Context("New backup with section size", func() {
		sectionSize, _ := resource.ParseQuantity("100M")
		testCase := backupTestCase{
			name:         "rman backup with section size",
			instanceName: "mydb",
			backupName:   "secsiz",
			instanceSpec: v1alpha1.InstanceSpec{
				CDBName: "GCLOUD",
				InstanceSpec: commonv1alpha1.InstanceSpec{
					Disks: []commonv1alpha1.DiskSpec{
						{
							Name: "DataDisk",
							Size: resource.MustParse("45Gi"),
						},
						{
							Name: "LogDisk",
							Size: resource.MustParse("55Gi"),
						},
					},
					Images:            map[string]string{},
					DatabaseResources: dbResource,
				},
			},
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: "mydb",
					Type:     commonv1alpha1.BackupTypePhysical,
				},
				SectionSize: sectionSize,
			},
		}

		Context("Oracle 18c XE", func() {
			testCase.instanceSpec.Version = "18c"
			testCase.instanceSpec.Images = map[string]string{
				"service": testhelpers.TestImageForVersion("18c", "XE", ""),
			}
			BackupTest(testCase)
		})
	})
})

func TestPhysicalBackup(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecsWithDefaultAndCustomReporters(t,
		t.Name(),
		[]Reporter{printer.NewlineReporter{}})
}
