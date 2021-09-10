package backupcontroller

import (
	"context"
	"fmt"
	"testing"
	"time"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	capb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	testInstanceName = "test-instance"
	testBackupName   = "test-backup"
	testNamespace    = "test-ns"
	testBackupID     = "test-backupID"
	testGCSPath      = "gs://testbucket"
)

var (
	testTimeNow = metav1.NewTime(time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))
)

type mockBackupControl struct {
	validateBackupSpec func(backup *v1alpha1.Backup) bool
	getBackup          func(name, namespace string) (*v1alpha1.Backup, error)
	getInstance        func(name, namespace string) (*v1alpha1.Instance, error)
	loadConfig         func(namespace string) (*v1alpha1.Config, error)
	updateStatus       func(obj client.Object) error
}

func (c *mockBackupControl) ValidateBackupSpec(backup *v1alpha1.Backup) bool {
	return c.validateBackupSpec(backup)
}

func (c *mockBackupControl) GetBackup(name, namespace string) (*v1alpha1.Backup, error) {
	return c.getBackup(name, namespace)
}

func (c *mockBackupControl) GetInstance(name, namespace string) (*v1alpha1.Instance, error) {
	return c.getInstance(name, namespace)
}

func (c *mockBackupControl) LoadConfig(namespace string) (*v1alpha1.Config, error) {
	return c.loadConfig(namespace)
}

func (c *mockBackupControl) UpdateStatus(obj client.Object) error {
	return c.updateStatus(obj)
}

type mockOracleBackup struct {
	statusFunc      func(ctx context.Context) (done bool, err error)
	createCalledCnt int
	statusCalledCnt int
}

func (b *mockOracleBackup) create(ctx context.Context) error {
	b.createCalledCnt++
	return nil
}

func (b *mockOracleBackup) status(ctx context.Context) (done bool, err error) {
	b.statusCalledCnt++
	return b.statusFunc(ctx)
}

func (b *mockOracleBackup) generateID() string {
	return testBackupID
}

type mockOracleBackupFactory struct {
	mockBackup *mockOracleBackup
}

func (f *mockOracleBackupFactory) newOracleBackup(r *BackupReconciler, backup *v1alpha1.Backup, inst *v1alpha1.Instance, log logr.Logger) oracleBackup {
	return f.mockBackup
}

func TestReconcileBackupErrors(t *testing.T) {
	reconciler, _, backupCtrl, _, _ := newTestBackupReconciler()
	backup := v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testBackupName,
			Namespace: testNamespace,
		},
		Spec: v1alpha1.BackupSpec{
			BackupSpec: commonv1alpha1.BackupSpec{
				Instance: testInstanceName,
			},
		},
	}

	testCases := []struct {
		name               string
		isSpecValid        bool
		getBackupError     error
		updateStatusError  error
		wantNewStatus      v1alpha1.BackupStatus
		wantReconcileError bool
	}{
		{
			name:               "Error get backup fail",
			getBackupError:     fmt.Errorf("failed to get backup"),
			wantNewStatus:      v1alpha1.BackupStatus{},
			wantReconcileError: true,
		}, {
			name:               "Error backup not found",
			getBackupError:     apierrors.NewNotFound(schema.GroupResource{Group: "db.anthosapis.com", Resource: "Backup"}, testBackupName),
			wantNewStatus:      v1alpha1.BackupStatus{},
			wantReconcileError: false,
		}, {
			name:        "Error invalid spec",
			isSpecValid: false,
			wantNewStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Conditions: []metav1.Condition{
						{
							Type:    k8s.Ready,
							Status:  metav1.ConditionUnknown,
							Message: "backup spec is invalid",
						},
					},
				},
			},
			wantReconcileError: false,
		}, {
			name:              "Error update status fail",
			isSpecValid:       false,
			updateStatusError: fmt.Errorf("failed to update status"),
			wantNewStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Conditions: []metav1.Condition{
						{
							Type:    k8s.Ready,
							Status:  metav1.ConditionUnknown,
							Message: "backup spec is invalid",
						},
					},
				},
			},
			wantReconcileError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var gotNewStatus v1alpha1.BackupStatus
			backupCtrl.getBackup = func(name, namespace string) (*v1alpha1.Backup, error) {
				return &backup, tc.getBackupError
			}
			backupCtrl.validateBackupSpec = func(backup *v1alpha1.Backup) bool {
				if !tc.isSpecValid {
					backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, metav1.ConditionUnknown, "", "backup spec is invalid")
				}
				return tc.isSpecValid
			}
			backupCtrl.updateStatus = func(obj client.Object) error {
				gotNewStatus = obj.(*v1alpha1.Backup).Status
				return tc.updateStatusError
			}

			_, gotReconcileError := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      testBackupName,
					Namespace: testNamespace,
				},
			})

			if tc.wantReconcileError != (gotReconcileError != nil) {
				t.Errorf("reconciler.Reconcile got error %v, but want error %v", gotReconcileError, tc.wantReconcileError)
			}

			if diff := cmp.Diff(tc.wantNewStatus, gotNewStatus, cmpopts.IgnoreTypes(metav1.Time{})); diff != "" {
				t.Errorf("reconciler.Reconcile got unexpected status update: -want +got %v", diff)
			}
		})
	}
}

func TestReconcileBackupCreation(t *testing.T) {
	testCases := []struct {
		name                            string
		oldStatus                       v1alpha1.BackupStatus
		instNotReady                    bool
		createDone                      bool
		createError                     error
		wantNewStatus                   v1alpha1.BackupStatus
		wantReconcileResult             ctrl.Result
		wantOracleBackupCreateCalledCnt int
		wantOracleBackupStatusCalledCnt int
	}{
		{
			name:      "Status transtion from empty to pending",
			oldStatus: v1alpha1.BackupStatus{},
			wantNewStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupPending,
					Conditions: []metav1.Condition{
						{
							Type:   k8s.Ready,
							Status: metav1.ConditionFalse,
							Reason: k8s.BackupPending,
						},
					},
				},
			},
			wantReconcileResult: ctrl.Result{RequeueAfter: requeueInterval},
		}, {
			name:         "Status transition from pending to failed when instance is not ready",
			instNotReady: true,
			oldStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupPending,
					Conditions: []metav1.Condition{
						{
							Type:   k8s.Ready,
							Status: metav1.ConditionFalse,
							Reason: k8s.BackupPending,
						},
					},
				},
			},
			wantNewStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupFailed,
					Conditions: []metav1.Condition{
						{
							Type:   k8s.Ready,
							Status: metav1.ConditionFalse,
							Reason: k8s.BackupFailed,
						},
					},
				},
			},
			wantReconcileResult: ctrl.Result{},
		}, {
			name: "Status transition from pending to inprogress",
			oldStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupPending,
					Conditions: []metav1.Condition{
						{
							Type:   k8s.Ready,
							Status: metav1.ConditionFalse,
							Reason: k8s.BackupPending,
						},
					},
				},
				BackupID:   testBackupID,
				BackupTime: testTimeNow.Format("20060102150405"),
				StartTime:  &testTimeNow,
			},
			wantNewStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupInProgress,
					Conditions: []metav1.Condition{
						{
							Type:   k8s.Ready,
							Status: metav1.ConditionFalse,
							Reason: k8s.BackupInProgress,
						},
					},
				},
				BackupID:   testBackupID,
				BackupTime: testTimeNow.Format("20060102150405"),
				StartTime:  &testTimeNow,
			},
			wantOracleBackupCreateCalledCnt: 1,
			wantReconcileResult:             ctrl.Result{RequeueAfter: requeueInterval},
		}, {
			name:       "Status transition from inprogress to success when create is done",
			createDone: true,
			oldStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupInProgress,
					Conditions: []metav1.Condition{
						{
							Type:   k8s.Ready,
							Status: metav1.ConditionFalse,
							Reason: k8s.BackupInProgress,
						},
					},
				},
				BackupID:   testBackupID,
				BackupTime: testTimeNow.Format("20060102150405"),
				StartTime:  &testTimeNow,
			},
			wantNewStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupSucceeded,
					Conditions: []metav1.Condition{
						{
							Type:   k8s.Ready,
							Status: metav1.ConditionTrue,
							Reason: k8s.BackupReady,
						},
					},
				},
				BackupID:   testBackupID,
				BackupTime: testTimeNow.Format("20060102150405"),
				StartTime:  &testTimeNow,
			},
			wantOracleBackupStatusCalledCnt: 1,
			wantReconcileResult:             ctrl.Result{},
		}, {
			name:        "Status transition from inprogress to fail when create fails",
			createDone:  true,
			createError: fmt.Errorf("Backup creation failed."),
			oldStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupInProgress,
					Conditions: []metav1.Condition{
						{
							Type:   k8s.Ready,
							Status: metav1.ConditionFalse,
							Reason: k8s.BackupInProgress,
						},
					},
				},
				BackupTime: testTimeNow.Format("20060102150405"),
				StartTime:  &testTimeNow,
			},
			wantNewStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupFailed,
					Conditions: []metav1.Condition{
						{
							Type:   k8s.Ready,
							Status: metav1.ConditionFalse,
							Reason: k8s.BackupFailed,
						},
					},
				},
				BackupTime: testTimeNow.Format("20060102150405"),
				StartTime:  &testTimeNow,
			},
			wantOracleBackupStatusCalledCnt: 1,
			wantReconcileResult:             ctrl.Result{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reconciler, oracleBackup, backupCtrl, _, _ := newTestBackupReconciler()
			var gotNewStatus v1alpha1.BackupStatus
			backupCtrl.updateStatus = func(obj client.Object) error {
				if _, ok := obj.(*v1alpha1.Backup); ok {
					gotNewStatus = obj.(*v1alpha1.Backup).Status
				}
				return nil
			}
			backupCtrl.validateBackupSpec = func(backup *v1alpha1.Backup) bool {
				return true
			}
			backupCtrl.getInstance = func(name, namespace string) (*v1alpha1.Instance, error) {
				readyCond := metav1.Condition{Type: k8s.Ready, Status: metav1.ConditionTrue}
				if tc.instNotReady {
					readyCond.Status = metav1.ConditionFalse
				}
				inst := v1alpha1.Instance{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testInstanceName,
						Namespace: testNamespace,
					},
					Status: v1alpha1.InstanceStatus{
						InstanceStatus: commonv1alpha1.InstanceStatus{
							Conditions: []metav1.Condition{readyCond},
						},
					},
				}
				return &inst, nil
			}
			timeNow = func() time.Time {
				return testTimeNow.Time
			}
			oracleBackup.statusFunc = func(ctx context.Context) (done bool, err error) {
				return tc.createDone, tc.createError
			}
			gotReconcileResult, _ := reconciler.reconcileBackupCreation(context.Background(), newBackupWithStatus(tc.oldStatus), reconciler.Log)
			if diff := cmp.Diff(gotReconcileResult, tc.wantReconcileResult); diff != "" {
				t.Errorf("reconciler.reconcileBackupCreation got unexpected reconcile result: -want +got %v", diff)
			}
			statusCmpOptions := []cmp.Option{
				cmpopts.IgnoreFields(metav1.Condition{}, "Message"),
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.IgnoreFields(v1alpha1.BackupStatus{}, "Duration"),
			}
			if diff := cmp.Diff(gotNewStatus, tc.wantNewStatus, statusCmpOptions...); diff != "" {
				t.Errorf("reconciler.reconcileBackupCreation got unexpected backup status: -want +got %v", diff)
			}
			if oracleBackup.createCalledCnt != tc.wantOracleBackupCreateCalledCnt {
				t.Errorf("reconciler.reconcileBackupCreation got unexpected number of calls to oracleBackup.create()")
			}
			if oracleBackup.statusCalledCnt != tc.wantOracleBackupStatusCalledCnt {
				t.Errorf("reconciler.reconcileBackupCreation got unexpected number of calls to oracleBackup.status()")
			}
		})
	}
}

func TestReconcileVerifyExist(t *testing.T) {
	testCases := []struct {
		name                              string
		backupSpec                        v1alpha1.BackupSpec
		instNotReady                      bool
		verifyPhysicalBackupErrMsg        []string
		wantVerifyPhysicalBackupCalledCnt int
		wantNewStatus                     v1alpha1.BackupStatus
		wantReconcileResult               ctrl.Result
	}{
		{
			name: "Unsupported spec.type",
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     commonv1alpha1.BackupTypeSnapshot,
				},
			},
			wantNewStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupFailed,
					Conditions: []metav1.Condition{
						{
							Type:   k8s.Ready,
							Status: metav1.ConditionFalse,
							Reason: k8s.NotSupported,
						},
					},
				},
			},
			wantReconcileResult: ctrl.Result{},
		}, {
			name: "Unsupported spec.gcsPath",
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     commonv1alpha1.BackupTypePhysical,
				},
			},
			wantNewStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupFailed,
					Conditions: []metav1.Condition{
						{
							Type:   k8s.Ready,
							Status: metav1.ConditionFalse,
							Reason: k8s.NotSupported,
						},
					},
				},
			},
			wantReconcileResult: ctrl.Result{},
		}, {
			name: "Requeue when instance is not ready",
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     commonv1alpha1.BackupTypePhysical,
				},
				GcsPath: testGCSPath,
			},
			instNotReady:        true,
			wantNewStatus:       v1alpha1.BackupStatus{},
			wantReconcileResult: ctrl.Result{RequeueAfter: requeueInterval},
		}, {
			name: "Verify exists success",
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     commonv1alpha1.BackupTypePhysical,
				},
				GcsPath: testGCSPath,
			},
			verifyPhysicalBackupErrMsg: []string{},
			wantNewStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupSucceeded,
					Conditions: []metav1.Condition{{
						Type:   k8s.Ready,
						Status: metav1.ConditionTrue,
						Reason: k8s.BackupReady,
					}},
				},
			},
			wantReconcileResult:               ctrl.Result{RequeueAfter: verifyExistsInterval},
			wantVerifyPhysicalBackupCalledCnt: 1,
		}, {
			name: "Verify exists fail",
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     commonv1alpha1.BackupTypePhysical,
				},
				GcsPath: testGCSPath,
			},
			verifyPhysicalBackupErrMsg: []string{"Backup doesn't exist"},
			wantNewStatus: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupFailed,
					Conditions: []metav1.Condition{{
						Type:   k8s.Ready,
						Status: metav1.ConditionFalse,
						Reason: k8s.BackupFailed,
					}},
				},
			},
			wantReconcileResult:               ctrl.Result{RequeueAfter: verifyExistsInterval},
			wantVerifyPhysicalBackupCalledCnt: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reconciler, _, backupCtrl, caclient, _ := newTestBackupReconciler()
			var gotNewStatus v1alpha1.BackupStatus
			backupCtrl.updateStatus = func(obj client.Object) error {
				if _, ok := obj.(*v1alpha1.Backup); ok {
					gotNewStatus = obj.(*v1alpha1.Backup).Status
				}
				return nil
			}

			backupCtrl.getInstance = func(name, namespace string) (*v1alpha1.Instance, error) {
				readyCond := metav1.Condition{Type: k8s.Ready, Status: metav1.ConditionTrue}
				if tc.instNotReady {
					readyCond.Status = metav1.ConditionFalse
				}
				inst := v1alpha1.Instance{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testInstanceName,
						Namespace: testNamespace,
					},
					Status: v1alpha1.InstanceStatus{
						InstanceStatus: commonv1alpha1.InstanceStatus{
							Conditions: []metav1.Condition{readyCond},
						},
					},
				}
				return &inst, nil
			}

			caclient.SetMethodToResp("VerifyPhysicalBackup", &capb.VerifyPhysicalBackupResponse{ErrMsgs: tc.verifyPhysicalBackupErrMsg})

			gotReconcileResult, _ := reconciler.reconcileVerifyExists(context.Background(), newBackupWithSpec(tc.backupSpec), reconciler.Log)
			if diff := cmp.Diff(gotReconcileResult, tc.wantReconcileResult); diff != "" {
				t.Errorf("reconciler.reconcileBackupCreation got unexpected reconcile result: -want +got %v", diff)
			}

			statusCmpOptions := []cmp.Option{
				cmpopts.IgnoreFields(metav1.Condition{}, "Message"),
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.IgnoreFields(v1alpha1.BackupStatus{}, "Duration"),
			}
			if diff := cmp.Diff(gotNewStatus, tc.wantNewStatus, statusCmpOptions...); diff != "" {
				t.Errorf("reconciler.reconcileBackupCreation got unexpected backup status: -want +got %v", diff)
			}
		})
	}
}

func newBackupWithStatus(status v1alpha1.BackupStatus) *v1alpha1.Backup {
	backup := v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testBackupName,
			Namespace: testNamespace,
		},
		Spec: v1alpha1.BackupSpec{
			BackupSpec: commonv1alpha1.BackupSpec{
				Instance: testInstanceName,
			},
		},
		Status: status,
	}
	return &backup
}

func newTestBackupReconciler() (reconciler *BackupReconciler,
	b *mockOracleBackup,
	c *mockBackupControl,
	caclient *testhelpers.FakeConfigAgentClient,
	dbClient *testhelpers.FakeDatabaseClient) {
	b = &mockOracleBackup{}
	c = &mockBackupControl{}
	caclient = &testhelpers.FakeConfigAgentClient{}
	dbClient = &testhelpers.FakeDatabaseClient{}

	return &BackupReconciler{
		Log:                 ctrl.Log.WithName("controllers").WithName("Backup"),
		Scheme:              runtime.NewScheme(),
		ClientFactory:       &testhelpers.FakeClientFactory{Caclient: caclient},
		OracleBackupFactory: &mockOracleBackupFactory{mockBackup: b},
		Recorder:            record.NewFakeRecorder(10),
		BackupCtrl:          c,

		DatabaseClientFactory: &testhelpers.FakeDatabaseClientFactory{Dbclient: dbClient},
	}, b, c, caclient, dbClient
}
