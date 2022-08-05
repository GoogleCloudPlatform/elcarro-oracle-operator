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

package pitrcontroller

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	pb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/pitr/proto"
)

const (
	testImage          = "gcr.io/test-project/oracle.db.anthosapis.com/pitragent:latest"
	testPITR           = "test-pitr"
	testNamespace      = "test-ns"
	testInstance       = "test-inst"
	testGCSURI         = "gs://testbucket"
	testBackupSchedule = "0 */4 * * *"
)

type mockBackupControl struct {
	list func(ctx context.Context, opts ...client.ListOption) ([]v1alpha1.Backup, error)
}

func (m *mockBackupControl) List(ctx context.Context, opts ...client.ListOption) ([]v1alpha1.Backup, error) {
	return m.list(ctx, opts...)
}

type mockPITRControl struct {
	availableRecoveryWindow func(ctx context.Context, p *v1alpha1.PITR) ([]*pb.Range, error)
	updateStatus            func(ctx context.Context, p *v1alpha1.PITR) error
}

func (m *mockPITRControl) AvailableRecoveryWindows(ctx context.Context, p *v1alpha1.PITR) ([]*pb.Range, error) {
	return m.availableRecoveryWindow(ctx, p)
}

func (m *mockPITRControl) UpdateStatus(ctx context.Context, p *v1alpha1.PITR) error {
	return m.updateStatus(ctx, p)
}

func TestUpdateStatus(t *testing.T) {
	ctx := context.TODO()
	reconciler, backupCtrl, pitrCtrl := newTestPITRReconciler()
	testCases := []struct {
		name           string
		backups        []v1alpha1.Backup
		ranges         []*pb.Range
		incarnation    string
		wantTimeWindow []v1alpha1.TimeWindow
		wantSCNWindow  []v1alpha1.SCNWindow
	}{
		{
			name: "0 available window, backup is earlier than a window begin time",
			backups: []v1alpha1.Backup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							controllers.IncarnationLabel: "3",
						},
						Annotations: map[string]string{
							controllers.SCNAnnotation: "200",
							// date -d @1631203200 -u '+%Y-%m-%dT%H:%M:%SZ'
							// 2021-09-09T16:00:00Z
							controllers.TimestampAnnotation: time.Unix(1631203200, 0).Format(time.RFC3339),
						},
					},
					Status: v1alpha1.BackupStatus{
						BackupStatus: commonv1alpha1.BackupStatus{
							Phase: commonv1alpha1.BackupSucceeded,
						},
					},
				},
			},
			ranges: []*pb.Range{
				{
					Start: &pb.Instant{
						Time:        timestamppb.New(time.Unix(1631203300, 0)),
						Scn:         "300",
						Incarnation: "3",
					},
					End: &pb.Instant{
						Time:        timestamppb.New(time.Unix(1631203400, 0)),
						Scn:         "400",
						Incarnation: "3",
					},
				},
			},
		},

		{
			name: "0 available window, backup is later than a window end time",
			backups: []v1alpha1.Backup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							controllers.IncarnationLabel: "3",
						},
						Annotations: map[string]string{
							controllers.SCNAnnotation:       "500",
							controllers.TimestampAnnotation: time.Unix(1631203500, 0).Format(time.RFC3339),
						},
					},
					Status: v1alpha1.BackupStatus{
						BackupStatus: commonv1alpha1.BackupStatus{
							Phase: commonv1alpha1.BackupSucceeded,
						},
					},
				},
			},
			ranges: []*pb.Range{
				{
					Start: &pb.Instant{
						Time:        timestamppb.New(time.Unix(1631203300, 0)),
						Scn:         "300",
						Incarnation: "3",
					},
					End: &pb.Instant{
						Time:        timestamppb.New(time.Unix(1631203400, 0)),
						Scn:         "400",
						Incarnation: "3",
					},
				},
			},
		},

		{
			name: "1 available window",
			backups: []v1alpha1.Backup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							controllers.IncarnationLabel: "3",
						},
						Annotations: map[string]string{
							controllers.SCNAnnotation: "200",
							// date -d @1631203200 -u '+%Y-%m-%dT%H:%M:%SZ'
							// 2021-09-09T16:00:00Z
							controllers.TimestampAnnotation: time.Unix(1631203200, 0).Format(time.RFC3339),
						},
					},
					Status: v1alpha1.BackupStatus{
						BackupStatus: commonv1alpha1.BackupStatus{
							Phase: commonv1alpha1.BackupSucceeded,
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							controllers.IncarnationLabel: "3",
						},
						Annotations: map[string]string{
							controllers.SCNAnnotation:       "250",
							controllers.TimestampAnnotation: time.Unix(1631203250, 0).Format(time.RFC3339),
						},
					},
					Status: v1alpha1.BackupStatus{
						BackupStatus: commonv1alpha1.BackupStatus{
							Phase: commonv1alpha1.BackupSucceeded,
						},
					},
				},
			},
			ranges: []*pb.Range{
				{
					Start: &pb.Instant{
						Time:        timestamppb.New(time.Unix(1631203100, 0)),
						Scn:         "100",
						Incarnation: "3",
					},
					End: &pb.Instant{
						Time:        timestamppb.New(time.Unix(1631203300, 0)),
						Scn:         "300",
						Incarnation: "3",
					},
				},
			},
			incarnation: "3",
			wantTimeWindow: []v1alpha1.TimeWindow{
				{
					Begin: metav1.NewTime(time.Unix(1631203200, 0)),
					End:   metav1.NewTime(time.Unix(1631203300, 0)),
				},
			},
			wantSCNWindow: []v1alpha1.SCNWindow{
				{
					Begin: "200",
					End:   "300",
				},
			},
		},

		{
			name: "2 available windows",
			backups: []v1alpha1.Backup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							controllers.IncarnationLabel: "3",
						},
						Annotations: map[string]string{
							controllers.SCNAnnotation: "200",
							// date -d @1631203200 -u '+%Y-%m-%dT%H:%M:%SZ'
							// 2021-09-09T16:00:00Z
							controllers.TimestampAnnotation: time.Unix(1631203200, 0).Format(time.RFC3339),
						},
					},
					Status: v1alpha1.BackupStatus{
						BackupStatus: commonv1alpha1.BackupStatus{
							Phase: commonv1alpha1.BackupSucceeded,
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							controllers.IncarnationLabel: "3",
						},
						Annotations: map[string]string{
							controllers.SCNAnnotation:       "400",
							controllers.TimestampAnnotation: time.Unix(1631203400, 0).Format(time.RFC3339),
						},
					},
					Status: v1alpha1.BackupStatus{
						BackupStatus: commonv1alpha1.BackupStatus{
							Phase: commonv1alpha1.BackupSucceeded,
						},
					},
				},
			},
			ranges: []*pb.Range{
				{
					Start: &pb.Instant{
						Time:        timestamppb.New(time.Unix(1631203100, 0)),
						Scn:         "100",
						Incarnation: "3",
					},
					End: &pb.Instant{
						Time:        timestamppb.New(time.Unix(1631203300, 0)),
						Scn:         "300",
						Incarnation: "3",
					},
				},
				{
					Start: &pb.Instant{
						Time:        timestamppb.New(time.Unix(1631203400, 0)),
						Scn:         "400",
						Incarnation: "3",
					},
					End: &pb.Instant{
						Time:        timestamppb.New(time.Unix(1631203500, 0)),
						Scn:         "500",
						Incarnation: "3",
					},
				},
			},
			incarnation: "3",
			wantTimeWindow: []v1alpha1.TimeWindow{
				{
					Begin: metav1.NewTime(time.Unix(1631203200, 0)),
					End:   metav1.NewTime(time.Unix(1631203300, 0)),
				},
				{
					Begin: metav1.NewTime(time.Unix(1631203400, 0)),
					End:   metav1.NewTime(time.Unix(1631203500, 0)),
				},
			},
			wantSCNWindow: []v1alpha1.SCNWindow{
				{
					Begin: "200",
					End:   "300",
				},
				{
					Begin: "400",
					End:   "500",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPITRStatus v1alpha1.PITRStatus
			pitrCtrl.updateStatus = func(ctx context.Context, p *v1alpha1.PITR) error {
				gotPITRStatus = p.Status
				return nil
			}
			pitrCtrl.availableRecoveryWindow = func(ctx context.Context, p *v1alpha1.PITR) ([]*pb.Range, error) {
				return tc.ranges, nil
			}
			backupCtrl.list = func(context.Context, ...client.ListOption) ([]v1alpha1.Backup, error) {
				return tc.backups, nil
			}
			if err := reconciler.updateStatus(ctx, &v1alpha1.PITR{}, &v1alpha1.Instance{
				Status: v1alpha1.InstanceStatus{CurrentDatabaseIncarnation: tc.incarnation},
			}, reconciler.Log); err != nil {
				t.Fatalf("reconciler.updateStatus want nil, got %v", err)
			}
			if diff := cmp.Diff(tc.wantTimeWindow, gotPITRStatus.AvailableRecoveryWindowTime); diff != "" {
				t.Errorf("reconciler.updateStatus got unexpected AvailableRecoveryWindowTime deleted: -want +got %v", diff)
			}
			if diff := cmp.Diff(tc.wantSCNWindow, gotPITRStatus.AvailableRecoveryWindowSCN); diff != "" {
				t.Errorf("reconciler.updateStatus got unexpected AvailableRecoveryWindowSCN deleted: -want +got %v", diff)
			}
			if gotPITRStatus.CurrentDatabaseIncarnation != tc.incarnation {
				t.Errorf("reconciler.updateStatus got incarnation %s, want %s", gotPITRStatus.CurrentDatabaseIncarnation, tc.incarnation)
			}
		})
	}
}

func TestValidatePITRSpec(t *testing.T) {
	testCases := []struct {
		name          string
		inputSpec     v1alpha1.PITRSpec
		wantErrMsgCnt int
	}{
		{
			name: "Valid PITR spec",
			inputSpec: v1alpha1.PITRSpec{
				Images:         nil,
				InstanceRef:    &v1alpha1.InstanceReference{Name: testInstance},
				StorageURI:     testGCSURI,
				BackupSchedule: testBackupSchedule,
			},
			wantErrMsgCnt: 0,
		}, {
			name: "Invalid storageURI",
			inputSpec: v1alpha1.PITRSpec{
				Images:         nil,
				InstanceRef:    &v1alpha1.InstanceReference{Name: testInstance},
				StorageURI:     "invalidGCSURI",
				BackupSchedule: testBackupSchedule,
			},
			wantErrMsgCnt: 1,
		}, {
			name: "Invalid backupSchedule",
			inputSpec: v1alpha1.PITRSpec{
				Images:         nil,
				InstanceRef:    &v1alpha1.InstanceReference{Name: testInstance},
				StorageURI:     testGCSURI,
				BackupSchedule: "invalidCronExpression",
			},
			wantErrMsgCnt: 1,
		}, {
			name: "Invalid storageURI and backupSchedule",
			inputSpec: v1alpha1.PITRSpec{
				Images:         nil,
				InstanceRef:    &v1alpha1.InstanceReference{Name: testInstance},
				StorageURI:     "invalidGCSURI",
				BackupSchedule: "invalidCronExpression",
			},
			wantErrMsgCnt: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotErrMsg := validatePITRSpec(tc.inputSpec)
			if len(gotErrMsg) != tc.wantErrMsgCnt {
				t.Errorf("validatePITRSpec returns unexpected number of error msg. want:%v got:%v", tc.wantErrMsgCnt, len(gotErrMsg))
			}
		})
	}
}

func TestCalculateBackupRetentionCnt(t *testing.T) {
	testCases := []struct {
		name                   string
		backupSchedule         string
		recoverWindow          time.Duration
		wantBackupRetentionCnt int32
	}{
		{
			name:                   "Backup every 4 hours, 7 days recover window",
			backupSchedule:         "0 */4 * * *",
			recoverWindow:          time.Hour * 24 * 7,
			wantBackupRetentionCnt: 43,
		},
		{
			name:                   "Backup every 5 hours, 7 days recover window",
			backupSchedule:         "0 */5 * * *",
			recoverWindow:          time.Hour * 24 * 7,
			wantBackupRetentionCnt: 36,
		},
		{
			name:                   "Backup every day, 10 days recover window",
			backupSchedule:         "0 0 * * *",
			recoverWindow:          time.Hour * 24 * 10,
			wantBackupRetentionCnt: 11,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotBackupRetentionCnt := calculateBackupRetentionCnt(tc.backupSchedule, tc.recoverWindow)
			if gotBackupRetentionCnt != tc.wantBackupRetentionCnt {
				t.Errorf("calculateBackupRetentionCnt returns unexpected rentention cnt. want:%v got:%v", tc.wantBackupRetentionCnt, gotBackupRetentionCnt)
			}
		})
	}
}

func newTestPITRReconciler() (reconciler *PITRReconciler,
	backupCtrl *mockBackupControl,
	pitrCtrl *mockPITRControl) {

	backupCtrl = &mockBackupControl{}
	pitrCtrl = &mockPITRControl{}
	scheme := runtime.NewScheme()

	return &PITRReconciler{
		Log:        ctrl.Log.WithName("controllers").WithName("PITR"),
		Scheme:     scheme,
		BackupCtrl: backupCtrl,
		PITRCtrl:   pitrCtrl,
	}, backupCtrl, pitrCtrl
}
