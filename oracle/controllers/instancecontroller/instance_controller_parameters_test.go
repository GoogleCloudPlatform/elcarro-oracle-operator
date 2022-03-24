package instancecontroller

import (
	"context"
	"testing"
	"time"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

	fakeDatabaseClientFactory := &testhelpers.FakeDatabaseClientFactory{}
	fakeDatabaseClientFactory.Reset()
	fakeDatabaseClient := fakeDatabaseClientFactory.Dbclient
	fakeDatabaseClient.SetMethodToResp("FetchServiceImageMetaData", &dbdpb.FetchServiceImageMetaDataResponse{
		Version:    "19.3",
		CdbName:    "GCLOUD",
		OracleHome: "/u01/app/oracle/product/19.3/db",
	})

	const (
		Namespace    = "default"
		InstanceName = "test-instance-parameter"
	)
	objKey := client.ObjectKey{Namespace: Namespace, Name: InstanceName}
	for _, tc := range tests {
		instance := v1alpha1.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      objKey.Name,
				Namespace: objKey.Namespace,
			},
			Spec: v1alpha1.InstanceSpec{
				InstanceSpec: commonv1alpha1.InstanceSpec{
					Parameters:        map[string]string{tc.parameterKey: tc.parameterVal},
					MaintenanceWindow: &tc.maintenanceWindow,
				},
			},
		}
		_, _, err := fetchCurrentParameterState(ctx, reconciler, fakeDatabaseClientFactory, instance)
		if tc.expectedError && err == nil {
			t.Fatalf("TestSanityCheckParameters(ctx) expected error for test case:%v", tc.name)
		} else if !tc.expectedError && err != nil {
			t.Fatalf("TestSanityCheckParameters(ctx) didn't expect error for test case: %v", tc.name)
		}
	}
}

func testInstanceParameterUpdate() {
	const (
		Namespace    = "default"
		InstanceName = "test-instance-parameter"
		timeout      = time.Second * 25
		interval     = time.Millisecond * 15
	)
	It("should update observedGeneration", func() {
		objKey := client.ObjectKey{Namespace: Namespace, Name: InstanceName}
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

		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, objKey, &instance)
	})
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
