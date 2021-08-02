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

package backupschedulecontroller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ghodss/yaml"
	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
)

const (
	testBackupScheduleName = "test-backup-schedule"
	testNamespace          = "db"
	testSchedule           = "* * * * *" // At every minute
)

func TestReconcileWithNoBackupSchedule(t *testing.T) {
	reconciler, backupScheduleCtrl, _, _ := newTestBackupScheduleReconciler()
	backupScheduleCtrl.get = func(name, _ string) (*v1alpha1.BackupSchedule, error) {
		return nil, errors.NewNotFound(schema.GroupResource{Group: "oracle.db.anthosapis.com", Resource: "BackupSchedule"}, name)
	}
	_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testBackupScheduleName,
			Namespace: testNamespace,
		},
	})
	if err != nil {
		t.Errorf("reconciler.Reconcile got %v, want nil", err)
	}
}

func TestReconcileWithCronCreation(t *testing.T) {
	reconciler, backupScheduleCtrl, cronAnythingCtrl, _ := newTestBackupScheduleReconciler()
	testCases := []struct {
		name               string
		backupScheduleSpec *v1alpha1.BackupScheduleSpec
		wantCronStr        string
	}{
		{
			name: "minimum spec",
			backupScheduleSpec: &v1alpha1.BackupScheduleSpec{
				BackupSpec: v1alpha1.BackupSpec{
					BackupSpec: commonv1alpha1.BackupSpec{
						Instance: "mydb",
						Type:     commonv1alpha1.BackupTypeSnapshot,
					},
				},
				Schedule: testSchedule,
			},
			wantCronStr: strings.TrimSpace(`
metadata:
  creationTimestamp: null
  name: test-backup-schedule-cron
  namespace: db
  ownerReferences:
  - apiVersion: oracle.db.anthosapis.com/v1alpha1
    blockOwnerDeletion: true
    controller: true
    kind: BackupSchedule
    name: test-backup-schedule
    uid: ""
spec:
  concurrencyPolicy: Forbid
  finishableStrategy:
    stringField:
      fieldPath: '{.status.conditions[?(@.type=="Ready")].reason}'
      finishedValues:
      - BackupReady
      - BackupFailed
    type: StringField
  resourceBaseName: test-backup-schedule-cron
  resourceTimestampFormat: 20060102-150405
  schedule: '* * * * *'
  template:
    apiVersion: oracle.db.anthosapis.com/v1alpha1
    kind: Backup
    spec:
      instance: mydb
      type: Snapshot
  triggerDeadlineSeconds: 30
status: {}`),
		},
		{
			name: "spec trigger deadlines",
			backupScheduleSpec: &v1alpha1.BackupScheduleSpec{
				BackupSpec: v1alpha1.BackupSpec{
					BackupSpec: commonv1alpha1.BackupSpec{
						Instance: "mydb1",
						Type:     commonv1alpha1.BackupTypePhysical,
					},
					Subtype: "Instance",
					GcsPath: "gs://bucket/rman",
				},
				Schedule:                "*/5 * * * *",
				StartingDeadlineSeconds: pointer.Int64Ptr(60),
			},
			wantCronStr: strings.TrimSpace(`
metadata:
  creationTimestamp: null
  name: test-backup-schedule-cron
  namespace: db
  ownerReferences:
  - apiVersion: oracle.db.anthosapis.com/v1alpha1
    blockOwnerDeletion: true
    controller: true
    kind: BackupSchedule
    name: test-backup-schedule
    uid: ""
spec:
  concurrencyPolicy: Forbid
  finishableStrategy:
    stringField:
      fieldPath: '{.status.conditions[?(@.type=="Ready")].reason}'
      finishedValues:
      - BackupReady
      - BackupFailed
    type: StringField
  resourceBaseName: test-backup-schedule-cron
  resourceTimestampFormat: 20060102-150405
  schedule: '*/5 * * * *'
  template:
    apiVersion: oracle.db.anthosapis.com/v1alpha1
    kind: Backup
    spec:
      gcsPath: gs://bucket/rman
      instance: mydb1
      subType: Instance
      type: Physical
  triggerDeadlineSeconds: 60
status: {}`),
		},
	}
	backupSchedule := &v1alpha1.BackupSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testBackupScheduleName,
			Namespace: testNamespace,
		},
	}
	backupScheduleCtrl.get = func(_, _ string) (*v1alpha1.BackupSchedule, error) {
		return backupSchedule, nil
	}

	backupScheduleCtrl.updateStatus = func(backupSchedule *v1alpha1.BackupSchedule) error {
		return nil
	}

	cronAnythingCtrl.get = func(name, namespace string) (*v1alpha1.CronAnything, error) {
		return nil, errors.NewNotFound(schema.GroupResource{Group: "oracle.db.anthosapis.com", Resource: "CronAnything"}, name)
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var gotCronStr string
			cronAnythingCtrl.create = func(cron *v1alpha1.CronAnything) error {
				b, err := yaml.Marshal(cron)
				gotCronStr = strings.TrimSpace(string(b))
				return err
			}
			backupSchedule.Spec = *tc.backupScheduleSpec

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      testBackupScheduleName,
					Namespace: testNamespace,
				},
			})
			if err != nil {
				t.Fatalf("reconciler.Reconcile want nil, got %v", err)
			}
			if gotCronStr != tc.wantCronStr {
				t.Errorf("reconciler.Reconcile create CronAnything got spec \n%s\n want \n%s\n", gotCronStr, tc.wantCronStr)
			}
		})
	}
}

func TestReconcileWithCronUpdate(t *testing.T) {
	reconciler, backupScheduleCtrl, cronAnythingCtrl, backupCtrl := newTestBackupScheduleReconciler()
	schedule := v1alpha1.BackupSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testBackupScheduleName,
			Namespace: testNamespace,
		},
		Spec: v1alpha1.BackupScheduleSpec{
			BackupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: "mydb1",
					Type:     commonv1alpha1.BackupTypePhysical,
				},
				Subtype: "Instance",
				GcsPath: "gs://bucket/rman",
			},
			Schedule: testSchedule,
		},
	}
	backup, err := getBackupBytes(&schedule)
	if err != nil {
		t.Fatalf("failed to parse backup bytes: %v", err)
	}

	schedule1 := schedule
	schedule1.Spec.BackupSpec = v1alpha1.BackupSpec{
		BackupSpec: commonv1alpha1.BackupSpec{
			Instance: "mydb",
			Type:     commonv1alpha1.BackupTypeSnapshot,
		},
	}
	changedBackup, err := getBackupBytes(&schedule1)
	if err != nil {
		t.Fatalf("failed to parse changedBackup bytes: %v", err)
	}
	wantCronStr := strings.TrimSpace(`
resourceBaseName: test-backup-schedule-cron
resourceTimestampFormat: 20060102-150405
schedule: '* * * * *'
template:
  apiVersion: oracle.db.anthosapis.com/v1alpha1
  kind: Backup
  spec:
    gcsPath: gs://bucket/rman
    instance: mydb1
    subType: Instance
    type: Physical`)

	backupScheduleCtrl.get = func(_, _ string) (*v1alpha1.BackupSchedule, error) {
		return &schedule, nil
	}

	backupScheduleCtrl.updateStatus = func(backupSchedule *v1alpha1.BackupSchedule) error {
		return nil
	}

	backupCtrl.list = func(cronAnythingName string) ([]*v1alpha1.Backup, error) {
		return []*v1alpha1.Backup{}, nil
	}

	testCases := []struct {
		name            string
		backupSchedule  *v1alpha1.BackupSchedule
		oldCronSpec     *commonv1alpha1.CronAnythingSpec
		wantCronSpecStr string
	}{
		{
			name:           "schedule changed",
			backupSchedule: &schedule,
			oldCronSpec: &commonv1alpha1.CronAnythingSpec{
				Schedule:                "*/10 * * * *",
				Template:                runtime.RawExtension{Raw: backup},
				ResourceBaseName:        pointer.StringPtr("test-backup-schedule-cron"),
				ResourceTimestampFormat: pointer.StringPtr("20060102-150405"),
			},
			wantCronSpecStr: wantCronStr,
		},
		{
			name:           "backup spec changed",
			backupSchedule: &schedule,
			oldCronSpec: &commonv1alpha1.CronAnythingSpec{
				Schedule:                testSchedule,
				Template:                runtime.RawExtension{Raw: changedBackup},
				ResourceBaseName:        pointer.StringPtr("test-backup-schedule-cron"),
				ResourceTimestampFormat: pointer.StringPtr("20060102-150405"),
			},
			wantCronSpecStr: wantCronStr,
		},
		{
			name:           "StartingDeadlineSeconds changed",
			backupSchedule: &schedule,
			oldCronSpec: &commonv1alpha1.CronAnythingSpec{
				Schedule:                testSchedule,
				Template:                runtime.RawExtension{Raw: backup},
				ResourceBaseName:        pointer.StringPtr("test-backup-schedule-cron"),
				ResourceTimestampFormat: pointer.StringPtr("20060102-150405"),
				TriggerDeadlineSeconds:  pointer.Int64Ptr(60),
			},
			wantCronSpecStr: wantCronStr,
		},
		{
			name:           "unchanged",
			backupSchedule: &schedule,
			oldCronSpec: &commonv1alpha1.CronAnythingSpec{
				Schedule:                testSchedule,
				Template:                runtime.RawExtension{Raw: backup},
				ResourceBaseName:        pointer.StringPtr("test-backup-schedule-cron"),
				ResourceTimestampFormat: pointer.StringPtr("20060102-150405"),
			},
			wantCronSpecStr: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cronAnythingCtrl.get = func(name, namespace string) (*v1alpha1.CronAnything, error) {
				return &v1alpha1.CronAnything{
					Spec: v1alpha1.CronAnythingSpec{
						CronAnythingSpec: *tc.oldCronSpec,
					},
				}, nil
			}
			var gotCronSpecStr string
			cronAnythingCtrl.update = func(cron *v1alpha1.CronAnything) error {
				b, err := yaml.Marshal(cron.Spec)
				gotCronSpecStr = strings.TrimSpace(string(b))
				return err
			}

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      testBackupScheduleName,
					Namespace: testNamespace,
				},
			})
			if err != nil {
				t.Fatalf("reconciler.Reconcile want nil, got %v", err)
			}
			if gotCronSpecStr != tc.wantCronSpecStr {
				t.Errorf("reconciler.Reconcile create CronAnything got spec \n%s\n want \n%s\n", gotCronSpecStr, tc.wantCronSpecStr)
			}
		})
	}
}

func TestReconcileWithBackupPrune(t *testing.T) {
	reconciler, backupScheduleCtrl, cronAnythingCtrl, backupCtrl := newTestBackupScheduleReconciler()
	schedule := v1alpha1.BackupSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testBackupScheduleName,
			Namespace: testNamespace,
		},
		Spec: v1alpha1.BackupScheduleSpec{
			BackupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: "mydb1",
					Type:     commonv1alpha1.BackupTypePhysical,
				},
				Subtype: "Instance",
				GcsPath: "gs://bucket/rman",
			},
			Schedule:              testSchedule,
			BackupRetentionPolicy: &v1alpha1.BackupRetentionPolicy{BackupRetention: pointer.Int32Ptr(2)},
		},
	}
	backupBytes, err := getBackupBytes(&schedule)
	if err != nil {
		t.Fatalf("failed to parse backup byptes: %v", err)
	}

	backupScheduleCtrl.get = func(_, _ string) (*v1alpha1.BackupSchedule, error) {
		return &schedule, nil
	}
	backupScheduleCtrl.updateStatus = func(backupSchedule *v1alpha1.BackupSchedule) error {
		return nil
	}

	cronAnythingCtrl.get = func(name, namespace string) (*v1alpha1.CronAnything, error) {
		return &v1alpha1.CronAnything{
			Spec: v1alpha1.CronAnythingSpec{
				CronAnythingSpec: commonv1alpha1.CronAnythingSpec{
					Template: runtime.RawExtension{Raw: backupBytes},
				},
			},
		}, nil
	}
	cronAnythingCtrl.update = func(cron *v1alpha1.CronAnything) error {
		return nil
	}

	backups := makeSortedBackups(t, 4)

	testCases := []struct {
		name        string
		backups     []*v1alpha1.Backup
		wantDeleted []*v1alpha1.Backup
	}{
		{
			name:        "delete 1 backup",
			backups:     backups[0:3],
			wantDeleted: backups[2:3],
		},
		{
			name:        "delete 2 backups",
			backups:     backups,
			wantDeleted: backups[2:4],
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			backupCtrl.list = func(cronAnythingName string) ([]*v1alpha1.Backup, error) {
				return tc.backups, nil
			}
			var gotDeleted []*v1alpha1.Backup
			backupCtrl.delete = func(backup *v1alpha1.Backup) error {
				gotDeleted = append(gotDeleted, backup)
				return nil
			}

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      testBackupScheduleName,
					Namespace: testNamespace,
				},
			})
			if err != nil {
				t.Fatalf("reconciler.Reconcile want nil, got %v", err)
			}
			if diff := cmp.Diff(tc.wantDeleted, gotDeleted); diff != "" {
				t.Errorf("reconciler.Reconcile got unexpected backups deleted: -want +got %v", diff)
			}
		})
	}
}

func TestUpdateBackupHistory(t *testing.T) {
	reconciler, backupScheduleCtrl, _, _ := newTestBackupScheduleReconciler()
	testCases := []struct {
		name            string
		backupTotal     int
		wantTotal       int32
		wantRecordTotal int
	}{
		{
			name:            "less than max backup records limitation",
			backupTotal:     5,
			wantTotal:       5,
			wantRecordTotal: 5,
		},
		{
			name:            "more than backup records limitation",
			backupTotal:     15,
			wantTotal:       15,
			wantRecordTotal: 7,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			backups := makeSortedBackups(t, tc.backupTotal)
			schedule := &v1alpha1.BackupSchedule{
				Spec: v1alpha1.BackupScheduleSpec{
					BackupRetentionPolicy: &v1alpha1.BackupRetentionPolicy{BackupRetention: pointer.Int32Ptr(100)},
				},
			}
			var gotSchedule *v1alpha1.BackupSchedule
			backupScheduleCtrl.updateStatus = func(backupSchedule *v1alpha1.BackupSchedule) error {
				gotSchedule = backupSchedule
				return nil
			}
			if err := reconciler.updateHistory(schedule, backups); err != nil {
				t.Fatalf("reconciler.UpdateHistory want nil, got %v", err)
			}
			if *gotSchedule.Status.BackupTotal != tc.wantTotal {
				t.Errorf("reconciler.UpdateHistory got BackupTotal %d, want %d", *schedule.Status.BackupTotal, tc.wantTotal)
			}
			if len(gotSchedule.Status.BackupHistory) != tc.wantRecordTotal {
				t.Fatalf("reconciler.UpdateHistory len(BackupHistory) got %d, want %d", len(schedule.Status.BackupHistory), tc.wantRecordTotal)
			}
			for i := 0; i < tc.wantRecordTotal; i++ {
				if gotSchedule.Status.BackupHistory[i].CreationTime != backups[i].CreationTimestamp {
					t.Errorf("reconciler.UpdateHistory BackupHistory[%d] got %v, want %v", i, gotSchedule.Status.BackupHistory[i], backups[i])
				}
			}
		})
	}
}

func newTestBackupScheduleReconciler() (reconciler *BackupScheduleReconciler,
	backupScheduleCtrl *fakeBackupScheduleControl,
	cronAnythingCtrl *fakeCronAnythingControl,
	backupCtrl *fakeBackupControl) {

	backupScheduleCtrl = &fakeBackupScheduleControl{}
	cronAnythingCtrl = &fakeCronAnythingControl{}
	backupCtrl = &fakeBackupControl{}
	scheme := runtime.NewScheme()
	v1alpha1.AddToScheme(scheme)

	return &BackupScheduleReconciler{
		Log:                ctrl.Log.WithName("controllers").WithName("BackupSchedule"),
		scheme:             scheme,
		backupScheduleCtrl: backupScheduleCtrl,
		cronAnythingCtrl:   cronAnythingCtrl,
		backupCtrl:         backupCtrl,
	}, backupScheduleCtrl, cronAnythingCtrl, backupCtrl
}

func timeFromStr(t *testing.T, dateStr string) time.Time {
	date, err := time.Parse("2006-01-02T15:04:05Z", dateStr)
	if err != nil {
		t.Fatalf("failed to parse %s: %v", dateStr, err)
	}
	return date
}

func makeSortedBackups(t *testing.T, total int) (backups []*v1alpha1.Backup) {
	timestamp := timeFromStr(t, "2020-12-21T01:00:00Z")
	for i := 0; i < total; i++ {
		backups = append(backups, &v1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(timestamp),
			},
			Status: v1alpha1.BackupStatus{
				BackupStatus: commonv1alpha1.BackupStatus{
					Phase: commonv1alpha1.BackupSucceeded,
				},
			},
		})
		timestamp = timestamp.Add(time.Hour)
	}
	return backups
}

type fakeBackupScheduleControl struct {
	get          func(name, namespace string) (*v1alpha1.BackupSchedule, error)
	updateStatus func(backupSchedule *v1alpha1.BackupSchedule) error
}

func (f *fakeBackupScheduleControl) Get(name, namespace string) (*v1alpha1.BackupSchedule, error) {
	return f.get(name, namespace)
}
func (f *fakeBackupScheduleControl) UpdateStatus(backupSchedule *v1alpha1.BackupSchedule) error {
	return f.updateStatus(backupSchedule)
}

type fakeCronAnythingControl struct {
	create func(cron *v1alpha1.CronAnything) error
	get    func(name, namespace string) (*v1alpha1.CronAnything, error)
	update func(cron *v1alpha1.CronAnything) error
	delete func(cron *v1alpha1.CronAnything) error
}

func (f *fakeCronAnythingControl) Create(cron *v1alpha1.CronAnything) error {
	return f.create(cron)
}
func (f *fakeCronAnythingControl) Get(name, namespace string) (*v1alpha1.CronAnything, error) {
	return f.get(name, namespace)
}
func (f *fakeCronAnythingControl) Update(cron *v1alpha1.CronAnything) error {
	return f.update(cron)
}

type fakeBackupControl struct {
	list   func(cronAnythingName string) ([]*v1alpha1.Backup, error)
	delete func(backup *v1alpha1.Backup) error
}

func (f *fakeBackupControl) List(cronAnythingName string) ([]*v1alpha1.Backup, error) {
	return f.list(cronAnythingName)
}
func (f *fakeBackupControl) Delete(backup *v1alpha1.Backup) error {
	return f.delete(backup)
}
