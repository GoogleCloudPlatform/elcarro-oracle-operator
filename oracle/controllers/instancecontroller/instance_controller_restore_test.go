package instancecontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func testInstanceRestore() {
	const (
		Namespace    = "default"
		InstanceName = "test-instance-restore"

		timeout  = time.Second * 25
		interval = time.Millisecond * 15
	)

	var fakeConfigAgentClient *testhelpers.FakeConfigAgentClient
	var fakeDatabaseClient *testhelpers.FakeDatabaseClient
	oldPreflightFunc := restorePhysicalPreflightCheck

	BeforeEach(func() {
		fakeClientFactory.Reset()
		fakeConfigAgentClient = fakeClientFactory.Caclient
		fakeDatabaseClientFactory.Reset()
		fakeDatabaseClient = fakeDatabaseClientFactory.Dbclient

		fakeConfigAgentClient.SetAsyncPhysicalRestore(true)

		fakeDatabaseClient.SetMethodToResp(
			"FetchServiceImageMetaData", &dbdpb.FetchServiceImageMetaDataResponse{
				Version:    "19.3",
				CdbName:    "",
				OracleHome: "/u01/app/oracle/product/19.3/db",
			})
		restorePhysicalPreflightCheck = func(ctx context.Context, r *InstanceReconciler, namespace, instName string, log logr.Logger) error {
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
		instance := createSimpleInstance(ctx, InstanceName, Namespace, timeout, interval)
		backup := createSimpleRMANBackup(ctx, InstanceName, backupName, backupID, Namespace)

		By("invoking RMAN restore for the Instance")

		// configure fake ConfigAgent to be in requested mode
		fakeDatabaseClient.SetNextGetOperationStatus(mode)
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
		fakeDatabaseClient.SetNextGetOperationStatus(testhelpers.StatusDone)

		Eventually(func() (metav1.ConditionStatus, error) {
			return getConditionStatus(ctx, objKey, k8s.Ready)
		}, timeout, interval).Should(Equal(metav1.ConditionTrue))

		// There might be more than one call to DeleteOperation
		// from the reconciler loop with the same LRO id.
		// This should be expected and not harmful.
		Eventually(fakeDatabaseClient.DeleteOperationCalledCnt).Should(BeNumerically(">=", 1))

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

		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, objKey, instance)
		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, client.ObjectKey{Namespace: Namespace, Name: backupName}, backup)
	})

	It("it should NOT attempt to restore with the same RequestTime", func() {
		instance, backup := testCaseHappyPathLRORestore()

		oldPhysicalRestoreCalledCnt := fakeConfigAgentClient.PhysicalRestoreCalledCnt()
		fakeDatabaseClient.SetNextGetOperationStatus(testhelpers.StatusRunning)

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

		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, objKey, instance)
		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, client.ObjectKey{Namespace: Namespace, Name: backupName}, backup)
	})

	It("it should run new restore with a later RequestTime", func() {

		instance, backup := testCaseHappyPathLRORestore()

		// reset method call counters used later
		fakeConfigAgentClient.Reset()

		fakeDatabaseClient.SetMethodToResp("FetchServiceImageMetaData", &dbdpb.FetchServiceImageMetaDataResponse{
			Version:    "12.2",
			CdbName:    "GCLOUD",
			OracleHome: "/u01/app/oracle/product/12.2/db",
		})
		fakeConfigAgentClient.SetAsyncPhysicalRestore(true)

		By("restoring from same backup with later RequestTime")
		fakeDatabaseClient.SetNextGetOperationStatus(testhelpers.StatusRunning)
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

		By("checking that instance maintenance lock is acquired")
		Eventually(func() string {
			if err := k8sClient.Get(ctx, objKey, instance); err != nil {
				return ""
			}
			return instance.Status.LockedByController
		}, timeout, interval).Should(Equal("instancecontroller"))

		By("checking that instance is Ready on restore LRO completion")
		fakeDatabaseClient.SetNextGetOperationStatus(testhelpers.StatusDone)
		Eventually(func() (metav1.ConditionStatus, error) {
			return getConditionStatus(ctx, objKey, k8s.Ready)
		}, timeout, interval).Should(Equal(metav1.ConditionTrue))
		// There might be more than one call to DeleteOperation
		// from the reconciler loop with the same LRO id.
		// This should be expected and not harmful.
		Eventually(fakeDatabaseClient.DeleteOperationCalledCnt()).Should(BeNumerically(">=", 1))
		Expect(fakeConfigAgentClient.PhysicalRestoreCalledCnt()).Should(Equal(1))

		By("checking Status.LastRestoreTime was updated")
		Expect(k8sClient.Get(ctx, objKey, instance)).Should(Succeed())
		Expect(instance.Status.LastRestoreTime.UnixNano()).Should(Equal(secondRestoreRequestTime.UnixNano()))

		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, objKey, instance)
		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, client.ObjectKey{Namespace: Namespace, Name: backupName}, backup)
	})

	It("it should handle failure in LRO operation", func() {

		instance, backup := createInstanceAndStartRestore(testhelpers.StatusDoneWithError)

		By("checking that instance has RestoreFailed status")
		Expect(triggerReconcile(ctx, objKey)).Should(Succeed())
		Eventually(func() (string, error) {
			return getConditionReason(ctx, objKey, k8s.Ready)
		}, timeout, interval).Should(Equal(k8s.RestoreFailed))
		// There might be more than one call to DeleteOperation
		// from the reconciler loop with the same LRO id.
		// This should be expected and not harmful.
		Eventually(fakeDatabaseClient.DeleteOperationCalledCnt()).Should(BeNumerically(">=", 1))

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

		By("checking that instance maintenance lock is released")
		// Instance object should be fresh at this point, no need to retry
		Expect(instance.Status.LockedByController).Should(Equal(""))

		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, objKey, instance)
		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, client.ObjectKey{Namespace: Namespace, Name: backupName}, backup)
	})

	It("it should be able to restore from RestoreFailed state", func() {
		fakeConfigAgentClient.SetAsyncPhysicalRestore(false)

		instance := createSimpleInstance(ctx, InstanceName, Namespace, timeout, interval)
		backup := createSimpleRMANBackup(ctx, InstanceName, backupName, backupID, Namespace)

		By("setting Instance as False:RestoreFailed")
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			if err := k8sClient.Get(ctx, objKey, instance); err != nil {
				return err
			}
			instance.Status = v1alpha1.InstanceStatus{
				InstanceStatus: commonv1alpha1.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:               k8s.Ready,
							Status:             metav1.ConditionFalse,
							Reason:             k8s.RestoreFailed,
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

		// configure fake ConfigAgent to be in requested mode
		fakeDatabaseClient.SetNextGetOperationStatus(testhelpers.StatusNotFound)
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

		By("checking that instance status is Ready")
		Eventually(func() (metav1.ConditionStatus, error) {
			return getConditionStatus(ctx, objKey, k8s.Ready)
		}, timeout, interval).Should(Equal(metav1.ConditionTrue))

		Expect(fakeDatabaseClient.DeleteOperationCalledCnt()).Should(Equal(0))

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

		By("checking that instance maintenance lock is released")
		// Instance object should be fresh at this point, no need to retry
		Expect(instance.Status.LockedByController).Should(Equal(""))

		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, objKey, instance)
		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, client.ObjectKey{Namespace: Namespace, Name: backupName}, backup)
	})

	It("it should restore successfully in sync mode", func() {

		fakeConfigAgentClient.SetAsyncPhysicalRestore(false)
		instance, backup := createInstanceAndStartRestore(testhelpers.StatusNotFound)

		By("checking that instance status is Ready")
		Eventually(func() (metav1.ConditionStatus, error) {
			return getConditionStatus(ctx, objKey, k8s.Ready)
		}, timeout, interval).Should(Equal(metav1.ConditionTrue))

		Expect(fakeDatabaseClient.DeleteOperationCalledCnt()).Should(Equal(0))

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

		By("checking that instance maintenance lock is released")
		// Instance object should be fresh at this point, no need to retry
		Expect(instance.Status.LockedByController).Should(Equal(""))

		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, objKey, instance)
		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, client.ObjectKey{Namespace: Namespace, Name: backupName}, backup)
	})
}

func createSimpleRMANBackup(ctx context.Context, instanceName string, backupName string, backupID string, namespace string) *v1alpha1.Backup {
	trueVar := true
	By("creating a new RMAN backup")
	backup := &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      backupName,
		},
		Spec: v1alpha1.BackupSpec{
			BackupSpec: commonv1alpha1.BackupSpec{
				Instance: instanceName,
				Type:     commonv1alpha1.BackupTypePhysical,
			},
			Subtype:   "Instance",
			Backupset: &trueVar,
		},
	}

	backupObjKey := client.ObjectKey{Namespace: namespace, Name: backupName}
	createdBackup := &v1alpha1.Backup{}
	testhelpers.K8sCreateAndGet(k8sClient, ctx, backupObjKey, backup, createdBackup)

	createdBackup = &v1alpha1.Backup{}
	testhelpers.K8sUpdateStatusWithRetry(k8sClient, ctx, backupObjKey, createdBackup, func(obj *client.Object) {
		(*obj).(*v1alpha1.Backup).Status = v1alpha1.BackupStatus{
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
	})

	return createdBackup
}

func createSimpleInstance(ctx context.Context, instanceName string, namespace string, timeout time.Duration, interval time.Duration) *v1alpha1.Instance {
	objKey := client.ObjectKey{Namespace: namespace, Name: instanceName}

	By("creating a new Instance")
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
	testhelpers.K8sCreateWithRetry(k8sClient, ctx, instance)

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
	createdInstance := &v1alpha1.Instance{}
	testhelpers.K8sUpdateStatusWithRetry(k8sClient, ctx, objKey, createdInstance, func(obj *client.Object) {
		(*obj).(*v1alpha1.Instance).Status = v1alpha1.InstanceStatus{
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
	})

	return createdInstance
}
