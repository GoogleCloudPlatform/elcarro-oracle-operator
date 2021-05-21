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

package instancecontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

var (
	k8sClient  client.Client
	k8sManager ctrl.Manager
	images     = map[string]string{
		"dbinit":          "dbInitImage",
		"service":         "serviceImage",
		"config":          "configAgentImage",
		"logging_sidecar": "loggingSidecarImage",
	}
	reconciler        *InstanceReconciler
	fakeClientFactory *testhelpers.FakeClientFactory
)

func TestInstanceController(t *testing.T) {

	// Mock functions
	CheckStatusInstanceFunc = func(ctx context.Context, instName, cdbName, clusterIP, DBDomain string, log logr.Logger) (string, error) {
		return "Ready", nil
	}

	fakeClientFactory = &testhelpers.FakeClientFactory{}

	testhelpers.RunReconcilerTestSuite(t, &k8sClient, &k8sManager, "Instance controller", func() []testhelpers.Reconciler {
		reconciler = &InstanceReconciler{
			Client:        k8sManager.GetClient(),
			Log:           ctrl.Log.WithName("controllers").WithName("Instance"),
			Scheme:        k8sManager.GetScheme(),
			Images:        images,
			ClientFactory: fakeClientFactory,
			Recorder:      k8sManager.GetEventRecorderFor("instance-controller"),
		}

		return []testhelpers.Reconciler{reconciler}
	})
}

var _ = Describe("Instance controller", func() {

	// Define utility constants for object names and testing timeouts and intervals.
	const (
		Namespace    = "default"
		InstanceName = "test-instance"

		timeout  = time.Second * 15
		interval = time.Millisecond * 15
	)

	Context("New instance", func() {
		It("Should create statefulset/deployment/svc", func() {
			By("creating a new Instance")
			ctx := context.Background()
			instance := &v1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      InstanceName,
					Namespace: Namespace,
				},
				Spec: v1alpha1.InstanceSpec{
					CDBName: "GCLOUD",
					InstanceSpec: commonv1alpha1.InstanceSpec{
						Images: images,
					},
				},
			}
			Expect(k8sClient.Create(ctx, instance)).Should(Succeed())

			objKey := client.ObjectKey{Namespace: Namespace, Name: InstanceName}

			By("checking that statefulset/deployment/svc are created")
			Eventually(
				func() error {
					var createdInst v1alpha1.Instance
					if err := k8sClient.Get(ctx, objKey, &createdInst); err != nil {
						return err
					}
					if cond := k8s.FindCondition(createdInst.Status.Conditions, k8s.Ready); !k8s.ConditionReasonEquals(cond, k8s.CreateInProgress) {
						return errors.New("expected update has not happened yet")
					}
					return nil
				}, timeout, interval).Should(Succeed())

			var sts appsv1.StatefulSetList
			Expect(k8sClient.List(ctx, &sts, client.InNamespace(Namespace))).Should(Succeed())
			Expect(len(sts.Items) == 1)

			var deployment appsv1.DeploymentList
			Expect(k8sClient.List(ctx, &deployment, client.InNamespace(Namespace))).Should(Succeed())
			Expect(len(deployment.Items) == 1)

			var svc corev1.ServiceList
			Expect(k8sClient.List(ctx, &svc, client.InNamespace(Namespace))).Should(Succeed())
			Expect(len(svc.Items) == 4)

			Expect(k8sClient.Delete(ctx, instance)).Should(Succeed())
		})
	})

	Context("instance status observedGeneration and isChangeApplied fields", func() {

		It("should update observedGeneration", func() {
			fakeClientFactory.Reset()
			objKey := client.ObjectKey{Namespace: "default", Name: "generation-test-inst"}
			By("creating a new Instance")
			ctx := context.Background()
			instance := v1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      objKey.Name,
					Namespace: objKey.Namespace,
				},
				Spec: v1alpha1.InstanceSpec{
					CDBName: "GCLOUD",
					InstanceSpec: commonv1alpha1.InstanceSpec{
						Images: images,
					},
				},
			}
			Expect(k8sClient.Create(ctx, &instance)).Should(Succeed())

			By("checking observed generation matches generation in meta data")
			Eventually(func() bool {
				if k8sClient.Get(ctx, objKey, &instance) != nil {
					return false
				}
				return instance.ObjectMeta.Generation == instance.Status.ObservedGeneration
			}, timeout, interval).Should(Equal(true))

			By("updating instance parameters in spec")
			oldObservedGeneration := instance.Status.ObservedGeneration
			parameterMap := map[string]string{"parallel_servers_target": "15"}
			Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				if err := k8sClient.Get(ctx, objKey, &instance); err != nil {
					return err
				}
				instance.Spec.Parameters = parameterMap
				oneHourAfter := metav1.NewTime(time.Now().Add(1 * time.Hour))
				oneHour := metav1.Duration{Duration: time.Hour}
				instance.Spec.MaintenanceWindow = &commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{{Start: &oneHourAfter, Duration: &oneHour}}}
				return k8sClient.Update(ctx, &instance)
			})).Should(Succeed())

			By("checking generation in meta data is increased after spec changes")
			Eventually(func() bool {
				if k8sClient.Get(ctx, objKey, &instance) != nil {
					return false
				}
				return instance.ObjectMeta.Generation > oldObservedGeneration
			}, timeout, interval).Should(Equal(true))

			By("checking observed generation matches generation in meta data after reconciliation")
			Eventually(func() bool {
				if k8sClient.Get(ctx, objKey, &instance) != nil {
					return false
				}
				return instance.ObjectMeta.Generation == instance.Status.ObservedGeneration
			}, timeout, interval).Should(Equal(true))

			By("checking isChangeApplied is false before parameterUpdates is completed")
			Expect(instance.Status.IsChangeApplied).Should(Equal(metav1.ConditionFalse))

			By("updating currentParameters in status to match the parameterMap in spec")
			Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				if err := k8sClient.Get(ctx, objKey, &instance); err != nil {
					return err
				}
				instance.Status.CurrentParameters = parameterMap
				return k8sClient.Status().Update(ctx, &instance)
			})).Should(Succeed())

			By("checking isChangeApplied is true after parameterUpdates is completed")
			Eventually(func() bool {
				if k8sClient.Get(ctx, objKey, &instance) != nil {
					return false
				}
				return instance.Status.IsChangeApplied == metav1.ConditionTrue
			}, timeout*2, interval).Should(Equal(true))

			Expect(k8sClient.Delete(ctx, &instance)).Should(Succeed())
		})
	})

	Context("Existing instance restore from RMAN backup", func() {
		var fakeConfigAgentClient *testhelpers.FakeConfigAgentClient
		oldPreflightFunc := restorePhysicalPreflightCheck

		BeforeEach(func() {
			fakeClientFactory.Reset()
			fakeConfigAgentClient = fakeClientFactory.Caclient

			fakeConfigAgentClient.SetAsyncPhysicalRestore(true)
			restorePhysicalPreflightCheck = func(ctx context.Context, r *InstanceReconciler, namespace, instName string) error {
				return nil
			}
		})

		AfterEach(func() {
			restorePhysicalPreflightCheck = oldPreflightFunc
		})

		backupName := "test-backup"
		backupID := "test-backup-id"
		objKey := client.ObjectKey{Namespace: Namespace, Name: InstanceName}
		ctx := context.Background()
		restoreRequestTime := metav1.Now()

		createInstanceAndStartRestore := func(mode testhelpers.FakeOperationStatus) (*v1alpha1.Instance, *v1alpha1.Backup) {
			By("creating a new Instance")
			instance := &v1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      InstanceName,
					Namespace: Namespace,
				},
				Spec: v1alpha1.InstanceSpec{
					CDBName: "GCLOUD",
					InstanceSpec: commonv1alpha1.InstanceSpec{
						Images: images,
					},
				},
			}

			Expect(k8sClient.Create(ctx, instance)).Should(Succeed())

			By("checking that statefulset/deployment/svc are created")

			Eventually(
				func() error {
					var createdInst v1alpha1.Instance
					if err := k8sClient.Get(ctx, objKey, &createdInst); err != nil {
						return err
					}
					if cond := k8s.FindCondition(createdInst.Status.Conditions, k8s.Ready); !k8s.ConditionReasonEquals(cond, k8s.CreateInProgress) {
						return errors.New("expected update has not happened yet")
					}
					return nil
				}, timeout, interval).Should(Succeed())

			By("setting Instance as Ready")
			Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				if err := k8sClient.Get(ctx, objKey, instance); err != nil {
					return err
				}
				instance.Status = v1alpha1.InstanceStatus{
					InstanceStatus: commonv1alpha1.InstanceStatus{
						Conditions: []metav1.Condition{
							{
								Type:               k8s.Ready,
								Status:             metav1.ConditionTrue,
								Reason:             k8s.CreateComplete,
								LastTransitionTime: metav1.Now().Rfc3339Copy(),
							},
							{
								Type:               k8s.DatabaseInstanceReady,
								Status:             metav1.ConditionTrue,
								Reason:             k8s.CreateComplete,
								LastTransitionTime: metav1.Now().Rfc3339Copy(),
							},
						},
					},
				}
				return k8sClient.Status().Update(ctx, instance)
			})).Should(Succeed())

			trueVar := true
			By("creating a new RMAN backup")
			backup := &v1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: Namespace,
					Name:      backupName,
				},
				Spec: v1alpha1.BackupSpec{
					BackupSpec: commonv1alpha1.BackupSpec{
						Instance: InstanceName,
						Type:     commonv1alpha1.BackupTypePhysical,
					},
					Subtype:   "Instance",
					Backupset: &trueVar,
				},
			}

			backupObjKey := client.ObjectKey{Namespace: Namespace, Name: backupName}
			Expect(k8sClient.Create(ctx, backup)).Should(Succeed())
			Eventually(
				func() error {
					return k8sClient.Get(ctx, backupObjKey, backup)
				}, timeout, interval).Should(Succeed())

			backup.Status = v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Conditions: []metav1.Condition{
						{
							Type:               k8s.Ready,
							Status:             metav1.ConditionTrue,
							Reason:             k8s.BackupReady,
							LastTransitionTime: metav1.Now().Rfc3339Copy(),
						},
					},
					Phase: commonv1alpha1.BackupSucceeded,
				},
				BackupID: backupID,
			}
			Expect(k8sClient.Status().Update(ctx, backup)).Should(Succeed())

			By("invoking RMAN restore for the an Instance")

			// configure fake ConfigAgent to be in requested mode
			fakeConfigAgentClient.SetNextGetOperationStatus(mode)
			Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				if err := k8sClient.Get(ctx, objKey, instance); err != nil {
					return err
				}
				instance.Spec.Restore = &v1alpha1.RestoreSpec{
					BackupID:    backupID,
					BackupType:  "Physical",
					Force:       true,
					RequestTime: restoreRequestTime,
				}
				return k8sClient.Update(ctx, instance)
			})).Should(Succeed())

			return instance, backup
		}

		// extract happy path restore test part into a function for reuse
		// in multiple tests
		testCaseHappyPathLRORestore := func() (*v1alpha1.Instance, *v1alpha1.Backup) {
			instance, backup := createInstanceAndStartRestore(testhelpers.StatusRunning)

			By("verifying restore LRO was started")
			Eventually(func() (string, error) {
				return getConditionReason(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.RestoreInProgress))

			Expect(k8sClient.Get(ctx, objKey, instance)).Should(Succeed())
			Expect(instance.Status.LastRestoreTime).ShouldNot(BeNil())
			Expect(instance.Status.LastRestoreTime.UnixNano()).Should(Equal(restoreRequestTime.Rfc3339Copy().UnixNano()))

			Eventually(func() int {
				return fakeConfigAgentClient.PhysicalRestoreCalledCnt()
			}, timeout, interval).Should(Equal(1))

			By("checking that instance is Ready on restore LRO completion")
			fakeConfigAgentClient.SetNextGetOperationStatus(testhelpers.StatusDone)
			Expect(triggerReconcile(ctx, objKey)).Should(Succeed())

			Eventually(func() (metav1.ConditionStatus, error) {
				return getConditionStatus(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(metav1.ConditionTrue))
			Eventually(fakeConfigAgentClient.DeleteOperationCalledCnt).Should(Equal(1))

			By("checking that instance Restore section is deleted")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, objKey, instance); err != nil {
					return err
				}
				if instance.Spec.Restore != nil {
					return fmt.Errorf("expected update has not yet happened")
				}
				return nil
			}, timeout, interval).Should(Succeed())

			By("checking that instance Status.Description is updated")
			Expect(instance.Status.Description).Should(HavePrefix("Restored on"))
			Expect(instance.Status.Description).Should(ContainSubstring(backupID))

			return instance, backup
		}

		It("it should restore successfully in LRO mode", func() {
			instance, backup := testCaseHappyPathLRORestore()

			Expect(k8sClient.Delete(ctx, instance)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, backup)).Should(Succeed())
		})

		It("it should NOT attempt to restore with the same RequestTime", func() {
			instance, backup := testCaseHappyPathLRORestore()

			oldPhysicalRestoreCalledCnt := fakeConfigAgentClient.PhysicalRestoreCalledCnt()
			fakeConfigAgentClient.SetNextGetOperationStatus(testhelpers.StatusRunning)

			By("restoring from same backup with same RequestTime")
			Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				if err := k8sClient.Get(ctx, objKey, instance); err != nil {
					return err
				}
				instance.Spec.Restore = &v1alpha1.RestoreSpec{
					BackupID:    backupID,
					BackupType:  "Physical",
					Force:       true,
					RequestTime: restoreRequestTime,
				}
				return k8sClient.Update(ctx, instance)
			})).Should(Succeed())

			By("verifying restore was not run")
			Eventually(func() (metav1.ConditionStatus, error) {
				return getConditionStatus(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(metav1.ConditionTrue))
			Expect(fakeConfigAgentClient.PhysicalRestoreCalledCnt()).Should(Equal(oldPhysicalRestoreCalledCnt))

			Expect(k8sClient.Delete(ctx, instance)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, backup)).Should(Succeed())
		})

		It("it should run new restore with a later RequestTime", func() {

			instance, backup := testCaseHappyPathLRORestore()

			// reset method call counters used later
			fakeConfigAgentClient.Reset()
			fakeConfigAgentClient.SetAsyncPhysicalRestore(true)

			By("restoring from same backup with later RequestTime")
			fakeConfigAgentClient.SetNextGetOperationStatus(testhelpers.StatusRunning)
			secondRestoreRequestTime := metav1.NewTime(restoreRequestTime.Rfc3339Copy().Add(time.Second))

			Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				if err := k8sClient.Get(ctx, objKey, instance); err != nil {
					return err
				}
				instance.Spec.Restore = &v1alpha1.RestoreSpec{
					BackupID:    backupID,
					BackupType:  "Physical",
					Force:       true,
					RequestTime: secondRestoreRequestTime,
				}
				return k8sClient.Update(ctx, instance)
			})).Should(Succeed())

			By("verifying restore was started")
			Eventually(func() (string, error) {
				return getConditionReason(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.RestoreInProgress))

			By("checking that instance is Ready on restore LRO completion")
			fakeConfigAgentClient.SetNextGetOperationStatus(testhelpers.StatusDone)
			Expect(triggerReconcile(ctx, objKey)).Should(Succeed())
			Eventually(func() (metav1.ConditionStatus, error) {
				return getConditionStatus(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(metav1.ConditionTrue))
			Eventually(fakeConfigAgentClient.DeleteOperationCalledCnt).Should(Equal(1))
			Expect(fakeConfigAgentClient.PhysicalRestoreCalledCnt()).Should(Equal(1))

			By("checking Status.LastRestoreTime was updated")
			Expect(k8sClient.Get(ctx, objKey, instance)).Should(Succeed())
			Expect(instance.Status.LastRestoreTime.UnixNano()).Should(Equal(secondRestoreRequestTime.UnixNano()))

			Expect(k8sClient.Delete(ctx, instance)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, backup)).Should(Succeed())
		})

		It("it should handle failure in LRO operation", func() {

			instance, backup := createInstanceAndStartRestore(testhelpers.StatusDoneWithError)

			By("checking that instance has RestoreFailed status")
			Expect(triggerReconcile(ctx, objKey)).Should(Succeed())
			Eventually(func() (string, error) {
				return getConditionReason(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(k8s.RestoreFailed))
			Eventually(fakeConfigAgentClient.DeleteOperationCalledCnt, timeout, interval).Should(Equal(1))

			By("checking that instance Restore section is deleted")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, objKey, instance); err != nil {
					return err
				}
				if instance.Spec.Restore != nil {
					return fmt.Errorf("expected update has not yet happened")
				}
				return nil
			}, timeout, interval).Should(Succeed())

			By("checking that instance Status.Description is updated")
			cond := k8s.FindCondition(instance.Status.Conditions, k8s.Ready)
			Expect(cond.Message).Should(HavePrefix("Failed to restore on"))
			Expect(cond.Message).Should(ContainSubstring(backupID))

			Expect(k8sClient.Delete(ctx, instance)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, backup)).Should(Succeed())
		})

		It("it should restore successfully in sync mode", func() {

			fakeConfigAgentClient.SetAsyncPhysicalRestore(false)
			instance, backup := createInstanceAndStartRestore(testhelpers.StatusNotFound)

			By("checking that instance status is Ready")
			Eventually(func() (metav1.ConditionStatus, error) {
				return getConditionStatus(ctx, objKey, k8s.Ready)
			}, timeout, interval).Should(Equal(metav1.ConditionTrue))

			Expect(fakeConfigAgentClient.DeleteOperationCalledCnt()).Should(Equal(0))

			By("checking that instance Restore section is deleted")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, objKey, instance); err != nil {
					return err
				}
				if instance.Spec.Restore != nil {
					return fmt.Errorf("expected update has not yet happened")
				}
				return nil
			}, timeout, interval).Should(Succeed())

			By("checking that instance Status.Description is updated")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, objKey, instance); err != nil {
					return err
				}
				if !strings.HasPrefix(instance.Status.Description, "Restored on") {
					return fmt.Errorf("%q does not have expected prefix", instance.Status.Description)
				}
				if !strings.Contains(instance.Status.Description, backupID) {
					return fmt.Errorf("%q does not contain %q", instance.Status.Description, backupID)
				}
				return nil
			}, timeout, interval).Should(Succeed())

			Expect(k8sClient.Delete(ctx, instance)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, backup)).Should(Succeed())
		})
	})
})

func TestSanityCheckForReservedParameters(t *testing.T) {
	twoHourBefore := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	oneHourBefore := metav1.NewTime(time.Now().Add(-time.Hour))
	oneHourAfter := metav1.NewTime(time.Now().Add(time.Hour))
	oneHour := metav1.Duration{Duration: time.Hour}
	twoHours := metav1.Duration{Duration: 2 * time.Hour}

	tests := []struct {
		name              string
		parameterKey      string
		parameterVal      string
		expectedError     bool
		maintenanceWindow commonv1alpha1.MaintenanceWindowSpec
	}{
		{
			name:              "should return error for reserved parameters",
			parameterKey:      "audit_trail",
			parameterVal:      "/some/directory",
			expectedError:     true,
			maintenanceWindow: commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{{Start: &oneHourBefore, Duration: &twoHours}}},
		},
		{
			name:              "should not return error for valid parameters",
			parameterKey:      "parallel_servers_target",
			parameterVal:      "15",
			expectedError:     false,
			maintenanceWindow: commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{{Start: &oneHourBefore, Duration: &twoHours}}},
		},
		{
			name:              "should return error for elapsed past time range",
			parameterKey:      "parallel_servers_target",
			parameterVal:      "15",
			expectedError:     true,
			maintenanceWindow: commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{{Start: &twoHourBefore, Duration: &oneHour}}},
		},
		{
			name:              "should return error for current time not within time range",
			parameterKey:      "parallel_servers_target",
			parameterVal:      "15",
			expectedError:     true,
			maintenanceWindow: commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{{Start: &oneHourAfter, Duration: &oneHour}}},
		},
	}
	ctx := context.Background()
	fakeClientFactory := &testhelpers.FakeClientFactory{}
	fakeClientFactory.Reset()
	fakeConfigAgentClient := fakeClientFactory.Caclient

	for _, tc := range tests {
		instanceSpec := v1alpha1.InstanceSpec{
			InstanceSpec: commonv1alpha1.InstanceSpec{
				Parameters:        map[string]string{tc.parameterKey: tc.parameterVal},
				MaintenanceWindow: &tc.maintenanceWindow,
			},
		}
		_, _, err := fetchCurrentParameterState(ctx, fakeConfigAgentClient, instanceSpec)
		if tc.expectedError && err == nil {
			t.Fatalf("TestSanityCheckParameters(ctx) expected error for test case:%v", tc.name)
		} else if !tc.expectedError && err != nil {
			t.Fatalf("TestSanityCheckParameters(ctx) didn't expect error for test case: %v", tc.name)
		}
	}
}

func getConditionReason(ctx context.Context, objKey client.ObjectKey, cType string) (string, error) {
	var instance v1alpha1.Instance
	if err := k8sClient.Get(ctx, objKey, &instance); err != nil {
		return "", err
	}

	if cond := k8s.FindCondition(instance.Status.Conditions, cType); cond != nil {
		return cond.Reason, nil
	}
	return "", nil
}

func getConditionStatus(ctx context.Context, objKey client.ObjectKey, cType string) (metav1.ConditionStatus, error) {
	var instance v1alpha1.Instance
	if err := k8sClient.Get(ctx, objKey, &instance); err != nil {
		return "", err
	}
	if cond := k8s.FindCondition(instance.Status.Conditions, cType); cond != nil {
		return cond.Status, nil
	}
	return metav1.ConditionUnknown, nil
}

// triggerReconcile invokes k8s reconcile action by updating
// an irrelevant field.
func triggerReconcile(ctx context.Context, objKey client.ObjectKey) error {
	var instance v1alpha1.Instance
	if err := k8sClient.Get(ctx, objKey, &instance); err != nil {
		return err
	}
	instance.Spec.MemoryPercent = (instance.Spec.MemoryPercent + 1) % 100

	err := k8sClient.Update(ctx, &instance)
	if k8serrors.IsConflict(err) {
		return nil
	}
	return err
}

func createSimpleInstance(ctx context.Context, instanceName string, namespace string, timeout time.Duration, interval time.Duration) *v1alpha1.Instance {
	By("Creating a new Instance")
	images := map[string]string{"service": "image"}
	instance := &v1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName,
			Namespace: namespace,
		},
		Spec: v1alpha1.InstanceSpec{
			CDBName: "GCLOUD",
			InstanceSpec: commonv1alpha1.InstanceSpec{
				Images: images,
			},
		},
	}

	Expect(k8sClient.Create(ctx, instance)).Should(Succeed())
	createdInstance := &v1alpha1.Instance{}
	// We'll need to retry getting this newly created Instance, given that creation may not immediately happen.
	Eventually(
		func() error {
			return k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: instance.Name}, createdInstance)
		}, timeout, interval).Should(Succeed())

	Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: instance.Name}, instance); client.IgnoreNotFound(err) != nil {
			return err
		}
		instance.Status.Conditions = k8s.Upsert(instance.Status.Conditions, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, "")
		instance.Status.Conditions = k8s.Upsert(instance.Status.Conditions, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, "")
		return k8sClient.Status().Update(ctx, instance)
	})).Should(Succeed())

	By("By creating an agent service")
	agentSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance-agent-svc",
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9999}}},
	}
	// Might fail if the resource already exists
	k8sClient.Create(ctx, agentSvc)

	Eventually(
		func() error {
			return k8sClient.Get(ctx, client.ObjectKey{Name: "test-instance-agent-svc", Namespace: namespace}, agentSvc)
		}, timeout, interval).Should(Succeed())

	fakeClientFactory.Reset()

	return instance
}
