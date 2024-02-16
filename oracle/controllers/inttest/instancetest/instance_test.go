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

package instancetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"

	// Enable GCP auth for k8s client
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/instancecontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

// Made global to be accessible by AfterSuite
var k8sEnv = testhelpers.K8sOperatorEnvironment{}

// In case of Ctrl-C clean up the last valid k8sEnv.
var _ = AfterSuite(func() {
	k8sEnv.Close()
})

var _ = Describe("Instance and Database provisioning", func() {
	var namespace string
	var firstInstanceName string
	var secondInstanceName string
	var firstDatabaseName string
	var secondDatabaseName string
	var cdbName string
	podSpecLabel := "int-test-pod-spec"

	//Call Init with 'namespace' as both the namespace to install the operator, and the namespace for the operator to monitor.
	BeforeEach(func() {
		defer GinkgoRecover()
		namespace = testhelpers.RandName("instance-crd-test")
		firstInstanceName = "mydb-1"
		secondInstanceName = "mydb-2"
		firstDatabaseName = "pdb1"
		secondDatabaseName = "pdb2"
		cdbName = "MYDB"
		k8sEnv.Init(namespace, namespace)
	})

	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			testhelpers.PrintSimpleDebugInfo(k8sEnv, firstInstanceName, cdbName)
			testhelpers.PrintSimpleDebugInfo(k8sEnv, secondInstanceName, cdbName)
		}
		k8sEnv.Close()
	})

	TestInstanceCreationAndDatabaseProvisioning := func(version string, edition string, extra string, isImageSeeded bool) {
		It("Should create instance and provision database", func() {
			ctx := context.Background()
			k8sClient, err := client.New(k8sEnv.Env.Config, client.Options{})
			Expect(err).ToNot(HaveOccurred())

			instanceTimeout := instancecontroller.InstanceReadyTimeout
			dbTimeout := instancecontroller.DatabaseInstanceReadyTimeoutSeeded
			if !isImageSeeded {
				dbTimeout = instancecontroller.DatabaseInstanceReadyTimeoutUnseeded
			}
			dbTimeout += 5 * time.Minute // Add some buffer time given that this test runs in a different process space than the instance

			By("By creating two new Instances")
			createInstance(firstInstanceName, cdbName, namespace, version, edition, podSpecLabel, extra, true)
			instKey1 := client.ObjectKey{Namespace: namespace, Name: firstInstanceName}
			createInstance(secondInstanceName, cdbName, namespace, version, edition, podSpecLabel, extra, false)
			instKey2 := client.ObjectKey{Namespace: namespace, Name: secondInstanceName}

			By("By checking that Instance is created")
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey1, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, instanceTimeout)
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey2, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, instanceTimeout)

			By("By checking that Database (CDB) is provisioned")
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey1, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, dbTimeout)
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey2, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, dbTimeout)

			By("By creating a database (PDB) in the first instance")
			createDatabase(firstInstanceName, firstDatabaseName, namespace)

			By("By creating a database (PDB) in the second instance")
			createDatabase(secondInstanceName, secondDatabaseName, namespace)

			By("By checking that the Database (PDB) in the first Instance is Ready")
			db1Key := client.ObjectKey{Namespace: namespace, Name: firstDatabaseName}
			testhelpers.WaitForDatabaseConditionState(k8sEnv, db1Key, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 5*time.Minute)

			By("By checking that the Database (PDB) in the second Instance is Ready")
			db2Key := client.ObjectKey{Namespace: namespace, Name: secondDatabaseName}
			testhelpers.WaitForDatabaseConditionState(k8sEnv, db2Key, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 5*time.Minute)

			By("By checking that PVCs are created")
			var pvcList corev1.PersistentVolumeClaimList
			Expect(k8sClient.List(ctx, &pvcList, client.InNamespace(namespace))).Should(Succeed())
			Expect(len(pvcList.Items)).Should(Equal(4))

			By("By checking that statefulset/deployment/svc are created")
			var sts appsv1.StatefulSetList
			Expect(k8sClient.List(ctx, &sts, client.InNamespace(namespace))).Should(Succeed())
			Expect(len(sts.Items)).Should(Equal(2))

			var deployment appsv1.DeploymentList
			Expect(k8sClient.List(ctx, &deployment, client.InNamespace(namespace))).Should(Succeed())
			Expect(len(deployment.Items)).Should(Equal(3)) //1 deployment for the operator manager, 1 monitoring deployment for each instance

			var svc corev1.ServiceList
			Expect(k8sClient.List(ctx, &svc, client.InNamespace(namespace))).Should(Succeed())
			Expect(len(svc.Items)).Should(Equal(4)) // 2 services (LB, DBDaemon) per instance
			By("By Checking the PodSpec Field Is Propagated down to the Pod")
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey1, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 25*time.Minute)
			stsPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      instancecontroller.GetSTSName(firstInstanceName) + "-0",
					Namespace: namespace,
				},
			}
			testhelpers.K8sGetWithRetry(k8sEnv.K8sClient, ctx, client.ObjectKeyFromObject(stsPod), stsPod)
			podSpecLabelNames := []string{}
			for _, podAffinity := range stsPod.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
				val, ok := podAffinity.PodAffinityTerm.LabelSelector.MatchLabels["task-type"]
				if ok {
					podSpecLabelNames = append(podSpecLabelNames, val)
				}
			}
			Expect(podSpecLabelNames).Should(ContainElements(podSpecLabel))

			By("By checking that the Instance can be stopped")
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey1, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 25*time.Minute)
			createdInstance1 := &v1alpha1.Instance{}
			testhelpers.K8sUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
				instKey1,
				createdInstance1,
				func(obj *client.Object) {
					instanceToUpdate := (*obj).(*v1alpha1.Instance)
					instanceToUpdate.Spec.IsStopped = pointer.Bool(true)
				})
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey1, k8s.Ready, metav1.ConditionTrue, k8s.InstanceStopped, 5*time.Minute)
			By("Checking that the sts replicas were scaled down to 0")
			sts1 := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      instancecontroller.GetSTSName(firstInstanceName),
					Namespace: namespace,
				},
			}
			testhelpers.K8sGetWithLongRetry(k8sClient, ctx, client.ObjectKeyFromObject(sts1), sts1)
			Expect(*sts1.Spec.Replicas).Should(Equal(int32(controllers.StoppedReplicaCnt)))

			By("Checking that the monitoring deployment replicas were scaled down to 0")
			monitoringDep1 := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      instancecontroller.GetMonitoringDepName(firstInstanceName),
					Namespace: namespace,
				},
			}
			testhelpers.K8sGetWithLongRetry(k8sClient, ctx, client.ObjectKeyFromObject(monitoringDep1), monitoringDep1)
			Expect(*monitoringDep1.Spec.Replicas).Should(Equal(int32(controllers.StoppedReplicaCnt)))

			By("Checking that the instance can be started")
			createdInstance1 = &v1alpha1.Instance{}
			testhelpers.K8sUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
				instKey1,
				createdInstance1,
				func(obj *client.Object) {
					instanceToUpdate := (*obj).(*v1alpha1.Instance)
					instanceToUpdate.Spec.IsStopped = pointer.Bool(false)
				})

			By("Checking that the sts replicas were scaled up to the default replica count")
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey1, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 25*time.Minute)
			testhelpers.K8sGetWithLongRetry(k8sClient, ctx, client.ObjectKeyFromObject(sts1), sts1)
			Expect(*sts1.Spec.Replicas).Should(Equal(int32(controllers.DefaultReplicaCnt)))

			By("Checking that the monitoring deployment replicas were scaled up to the default replica count")
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey1, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 25*time.Minute)
			Eventually(func() bool {
				testhelpers.K8sGetWithRetry(k8sClient, ctx, client.ObjectKeyFromObject(monitoringDep1), monitoringDep1)
				return *monitoringDep1.Spec.Replicas == int32(controllers.DefaultReplicaCnt)
			}, time.Minute*25, time.Minute).Should(BeTrue())

			By("By checking that the datadisk and log disk get resized")
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey1, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 25*time.Minute)

			createdInstance1 = &v1alpha1.Instance{}
			testhelpers.K8sUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
				instKey1,
				createdInstance1,
				func(obj *client.Object) {
					instanceToUpdate := (*obj).(*v1alpha1.Instance)
					for i, disk := range instanceToUpdate.Spec.Disks {
						if disk.Name == "DataDisk" {
							instanceToUpdate.Spec.Disks[i].Size = resource.MustParse("45Gi")
						}
						if disk.Name == "LogDisk" {
							instanceToUpdate.Spec.Disks[i].Size = resource.MustParse("55Gi")
						}
					}
				})
			//wait for status propagation
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey1, k8s.Ready, metav1.ConditionFalse, k8s.ResizingInProgress, 5*time.Minute)
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey1, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 25*time.Minute)
			testhelpers.K8sGetWithLongRetry(k8sClient, ctx, client.ObjectKeyFromObject(sts1), sts1)
			By("By Checking if DataDisk and Log Disk PVC is changed")
			dataDiskPVCName := getPVCName(createdInstance1.Name, sts1.Name, "DataDisk")
			logDiskPVCName := getPVCName(createdInstance1.Name, sts1.Name, "LogDisk")
			for i := 0; i < int(*sts1.Spec.Replicas); i++ {
				dataPVC := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      dataDiskPVCName,
						Namespace: namespace,
					},
				}
				logPVC := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      logDiskPVCName,
						Namespace: namespace,
					},
				}
				Eventually(func() bool {
					testhelpers.K8sGetWithRetry(k8sClient, ctx, client.ObjectKeyFromObject(dataPVC), dataPVC)
					return *dataPVC.Spec.Resources.Requests.Storage() == resource.MustParse("45Gi")
				}, time.Minute*25, time.Minute).Should(BeTrue())
				Eventually(func() bool {
					testhelpers.K8sGetWithRetry(k8sClient, ctx, client.ObjectKeyFromObject(logPVC), logPVC)
					return *logPVC.Spec.Resources.Requests.Storage() == resource.MustParse("55Gi")
				}, time.Minute*25, time.Minute).Should(BeTrue())
			}
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey1, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 25*time.Minute)
			By("By checking that the cpu/mem get resized")
			testhelpers.K8sUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
				instKey1,
				createdInstance1,
				func(obj *client.Object) {
					instanceToUpdate := (*obj).(*v1alpha1.Instance)
					instanceToUpdate.Spec.DatabaseResources.Requests["memory"] = resource.MustParse("9Gi")
					instanceToUpdate.Spec.DatabaseResources.Requests["cpu"] = resource.MustParse("3m")
				})
			stsPod = &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sts1.Name + "-0",
					Namespace: namespace,
				},
			}
			Eventually(func() bool {
				testhelpers.K8sGetWithRetry(k8sEnv.K8sClient, ctx, client.ObjectKeyFromObject(stsPod), stsPod)
				for _, container := range stsPod.Spec.Containers {
					if container.Name == controllers.DatabaseContainerName {
						return container.Resources.Requests["memory"] == resource.MustParse("9Gi") && container.Resources.Requests["cpu"] == resource.MustParse("3m")
					}
				}
				return false
			}, 5*time.Minute, 5*time.Second).Should(Equal(true))
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey1, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 25*time.Minute)
			//call patch apply, get the pvcs, get the sts
			By("Deleting a database")
			deleteDatabase(ctx, firstDatabaseName, namespace)
			Eventually(func() ([]string, error) {
				updatedInstance := &v1alpha1.Instance{}
				Expect(k8sClient.Get(ctx, client.ObjectKey{Name: firstInstanceName, Namespace: namespace}, updatedInstance)).Should(Succeed())
				return updatedInstance.Status.DatabaseNames, nil
			}, 10*time.Minute, 5*time.Second).Should(BeEmpty())

			By("Checking that only PVCs for the first instance are retained")
			deleteInstance(ctx, firstInstanceName, namespace)
			deleteInstance(ctx, secondInstanceName, namespace) // Deleting the second Instance should lead to automatic deletion of the Databases attached to it
			Eventually(func() (int, error) {
				Expect(k8sClient.List(ctx, &pvcList, client.InNamespace(namespace))).Should(Succeed())
				return len(pvcList.Items), nil
			}, 5*time.Minute, 5*time.Second).Should(Equal(2)) // 2 PVCs kept for the first instance
			Expect(k8sClient.List(ctx, &pvcList, client.InNamespace(namespace))).Should(Succeed())
			pvcNames := make([]string, 2)
			for i := 0; i < len(pvcList.Items); i++ {
				pvcNames[i] = pvcList.Items[i].GetName()
			}
			Expect(pvcNames).Should(ContainElements(firstInstanceName+"-pvc-u02-"+firstInstanceName+"-sts-0",
				firstInstanceName+"-pvc-u03-"+firstInstanceName+"-sts-0"))

			By("Checking that Databases attached to the second instance are deleted along with the Instance")
			Eventually(func() bool {
				database := &v1alpha1.Database{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: secondDatabaseName, Namespace: namespace}, database)
				return apierrors.IsNotFound(err)
			}, 60*time.Second, 5*time.Second).Should(BeTrue())
		})
	}

	// Images built using El Carro scripts

	Context("Oracle 19.3 EE", func() {
		TestInstanceCreationAndDatabaseProvisioning("19.3", "EE", "", true)
	})

	Context("Oracle 18c XE", func() {
		TestInstanceCreationAndDatabaseProvisioning("18c", "XE", "", true)
	})

	Context("Oracle 23c FREE", func() {
		TestInstanceCreationAndDatabaseProvisioning("23c", "FREE", "", true)
	})

	// Slow tests, only run in Canary
	if testhelpers.IsCanaryJob() {
		Context("Oracle 19.3 EE unseeded", func() {
			TestInstanceCreationAndDatabaseProvisioning("19.3", "EE", "unseeded-32545013", false)
		})

		// Images from OCR
		Context("Oracle 19.3 EE unseeded from OCR", func() {
			TestInstanceCreationAndDatabaseProvisioning("19.3", "EE", "ocr", false)
		})
	}
})

func createInstance(instanceName, cdbName, namespace, version, edition, podSpecLabel, extra string, retainDisksOnDelete bool) {
	// Free edition only allows a CDB named 'FREE'
	if edition == "FREE" {
		cdbName = "FREE"
	}
	instance := &v1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName,
			Namespace: namespace,
		},
		Spec: v1alpha1.InstanceSpec{
			// Keep the CDBName in the spec different from the CDB name in the image (GCLOUD).
			// Doing this implicitly tests the CDB renaming feature.
			CDBName:      cdbName,
			DBUniqueName: cdbName,
			PodSpec: commonv1alpha1.PodSpec{
				Affinity: &corev1.Affinity{
					PodAntiAffinity: &corev1.PodAntiAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
							Weight: 1,
							PodAffinityTerm: corev1.PodAffinityTerm{
								LabelSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{"task-type": podSpecLabel},
								},
								TopologyKey: "kubernetes.io/hostname",
							},
						},
						},
					},
				},
			},
			InstanceSpec: commonv1alpha1.InstanceSpec{
				Version:                          version,
				RetainDisksAfterInstanceDeletion: retainDisksOnDelete,
				Disks: []commonv1alpha1.DiskSpec{
					{
						Name:         "DataDisk",
						Size:         resource.MustParse("40Gi"),
						StorageClass: "premium-rwo",
					},
					{
						Name:         "LogDisk",
						Size:         resource.MustParse("50Gi"),
						StorageClass: "premium-rwo",
					},
				},
				DatabaseResources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("8Gi"),
						corev1.ResourceCPU:    resource.MustParse("2m"),
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

func deleteInstance(ctx context.Context, instanceName, namespace string) {
	instance := &v1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      instanceName,
		},
	}
	testhelpers.K8sDeleteWithRetryNoWait(k8sEnv.K8sClient, ctx, client.ObjectKey{Name: instanceName, Namespace: namespace}, instance)
}

func deleteDatabase(ctx context.Context, databaseName, namespace string) {
	database := &v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      databaseName,
		},
	}
	testhelpers.K8sDeleteWithRetryNoWait(k8sEnv.K8sClient, ctx, client.ObjectKey{Name: databaseName, Namespace: namespace}, database)
}

func createDatabase(instanceName, databaseName, namespace string) {
	database := &v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      databaseName,
			Labels:    map[string]string{"instance": instanceName},
		},
		Spec: v1alpha1.DatabaseSpec{
			DatabaseSpec: commonv1alpha1.DatabaseSpec{
				Name:     databaseName,
				Instance: instanceName,
			},
			AdminPassword: "pwd123",
			Users: []v1alpha1.UserSpec{
				{UserSpec: commonv1alpha1.UserSpec{Name: "test", CredentialSpec: commonv1alpha1.CredentialSpec{Password: "pwd123"}}, Privileges: []v1alpha1.PrivilegeSpec{"connect"}},
			},
		},
	}

	testhelpers.K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, database)
}

func TestInstance(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecsWithDefaultAndCustomReporters(t,
		t.Name(),
		[]Reporter{printer.NewlineReporter{}})
}

func getPVCName(instName string, stsName string, diskName string) string {
	pvcName, _ := controllers.GetPVCNameAndMount(instName, diskName)
	pvcName = fmt.Sprintf("%s-%s-0", pvcName, stsName)
	return pvcName
}
