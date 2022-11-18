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

package controllers

import (
	"context"
	"encoding/json"
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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
)

const (
	testBackupScheduleName = "test-backup-schedule"
	testNamespace          = "db"
	testSchedule           = "* * * * *" // At every minute
)

func TestReconcileWithNoBackupSchedule(t *testing.T) {
	reconciler, backupScheduleCtrl, _, _ := newTestBackupScheduleReconciler()
	backupScheduleCtrl.get = func(name, _ string) (v1alpha1.BackupSchedule, error) {
		return nil, errors.NewNotFound(schema.GroupResource{Group: "db.anthosapis.com", Resource: "BackupSchedule"}, name)
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
		backupScheduleSpec *mockBackupScheduleSpec
		wantCronStr        string
	}{
		{
			name: "minimum spec",
			backupScheduleSpec: &mockBackupScheduleSpec{
				BackupScheduleSpec: v1alpha1.BackupScheduleSpec{
					Schedule: testSchedule,
				},
				BackupSpec: v1alpha1.BackupSpec{
					Instance: "mydb",
					Type:     v1alpha1.BackupTypeSnapshot,
				},
			},
			wantCronStr: strings.TrimSpace(`
Object: null
creationTimestamp: null
name: test-backup-schedule-cron
namespace: db
spec:
  concurrencyPolicy: Forbid
  finishableStrategy:
    stringField:
      fieldPath: '{.status.phase}'
      finishedValues:
      - Succeeded
      - Failed
    type: StringField
  resourceBaseName: test-backup-schedule-cron
  resourceTimestampFormat: 20060102-150405
  schedule: '* * * * *'
  template:
    apiVersion: mock.db.anthosapis.com/v1alpha1
    kind: Backup
    spec:
      instance: mydb
      type: Snapshot
  triggerDeadlineSeconds: 30
status: {}`),
		},
		{
			name: "spec trigger deadlines",
			backupScheduleSpec: &mockBackupScheduleSpec{
				BackupSpec: v1alpha1.BackupSpec{
					Instance: "mydb1",
					Type:     v1alpha1.BackupTypePhysical,
				},
				BackupScheduleSpec: v1alpha1.BackupScheduleSpec{
					Schedule:                "*/5 * * * *",
					StartingDeadlineSeconds: pointer.Int64Ptr(60),
				},
			},
			wantCronStr: strings.TrimSpace(`
Object: null
creationTimestamp: null
name: test-backup-schedule-cron
namespace: db
spec:
  concurrencyPolicy: Forbid
  finishableStrategy:
    stringField:
      fieldPath: '{.status.phase}'
      finishedValues:
      - Succeeded
      - Failed
    type: StringField
  resourceBaseName: test-backup-schedule-cron
  resourceTimestampFormat: 20060102-150405
  schedule: '*/5 * * * *'
  template:
    apiVersion: mock.db.anthosapis.com/v1alpha1
    kind: Backup
    spec:
      instance: mydb1
      type: Physical
  triggerDeadlineSeconds: 60
status: {}`),
		},
	}
	backupSchedule := &mockBackupSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testBackupScheduleName,
			Namespace: testNamespace,
		},
	}
	backupScheduleCtrl.get = func(_, _ string) (v1alpha1.BackupSchedule, error) {
		return backupSchedule, nil
	}

	backupScheduleCtrl.updateStatus = func(backupSchedule v1alpha1.BackupSchedule) error {
		return nil
	}

	backupScheduleCtrl.getBackupBytes = getBackupBytes

	cronAnythingCtrl.get = func(key types.NamespacedName) (v1alpha1.CronAnything, error) {
		return nil, errors.NewNotFound(schema.GroupResource{Group: "db.anthosapis.com", Resource: "CronAnything"}, name)
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var gotCronStr string
			cronAnythingCtrl.create = func(ns string, name string, cas v1alpha1.CronAnythingSpec, owner v1alpha1.BackupSchedule) error {
				ca := &fakeCronAnything{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: ns,
						Name:      name,
					},
					Spec: cas,
				}
				b, err := yaml.Marshal(ca)
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
			diffSpecs(t, gotCronStr, tc.wantCronStr)
		})
	}
}

func TestReconcileWithCronUpdate(t *testing.T) {
	reconciler, backupScheduleCtrl, cronAnythingCtrl, backupCtrl := newTestBackupScheduleReconciler()
	schedule := mockBackupSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testBackupScheduleName,
			Namespace: testNamespace,
		},
		Spec: mockBackupScheduleSpec{
			BackupSpec: v1alpha1.BackupSpec{
				Instance: "mydb1",
				Type:     v1alpha1.BackupTypePhysical,
			},
			BackupScheduleSpec: v1alpha1.BackupScheduleSpec{
				Schedule: testSchedule,
			},
		},
	}
	backup, err := getBackupBytes(&schedule)
	if err != nil {
		t.Fatalf("failed to parse backup bytes: %v", err)
	}

	schedule1 := schedule
	schedule1.Spec.BackupSpec = v1alpha1.BackupSpec{
		Instance: "mydb",
		Type:     v1alpha1.BackupTypeSnapshot,
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
  apiVersion: mock.db.anthosapis.com/v1alpha1
  kind: Backup
  spec:
    instance: mydb1
    type: Physical`)

	backupScheduleCtrl.get = func(_, _ string) (v1alpha1.BackupSchedule, error) {
		return &schedule, nil
	}

	backupScheduleCtrl.updateStatus = func(backupSchedule v1alpha1.BackupSchedule) error {
		return nil
	}

	backupScheduleCtrl.getBackupBytes = getBackupBytes

	backupCtrl.list = func(cronAnythingName string) ([]v1alpha1.Backup, error) {
		var a []v1alpha1.Backup
		return a, nil
	}

	testCases := []struct {
		name            string
		oldCronSpec     *v1alpha1.CronAnythingSpec
		wantCronSpecStr string
	}{
		{
			name: "schedule changed",
			oldCronSpec: &v1alpha1.CronAnythingSpec{
				Schedule:                "*/10 * * * *",
				Template:                runtime.RawExtension{Raw: backup},
				ResourceBaseName:        pointer.StringPtr("test-backup-schedule-cron"),
				ResourceTimestampFormat: pointer.StringPtr("20060102-150405"),
			},
			wantCronSpecStr: wantCronStr,
		},
		{
			name: "backup spec changed",
			oldCronSpec: &v1alpha1.CronAnythingSpec{
				Schedule:                testSchedule,
				Template:                runtime.RawExtension{Raw: changedBackup},
				ResourceBaseName:        pointer.StringPtr("test-backup-schedule-cron"),
				ResourceTimestampFormat: pointer.StringPtr("20060102-150405"),
			},
			wantCronSpecStr: wantCronStr,
		},
		{
			name: "StartingDeadlineSeconds changed",
			oldCronSpec: &v1alpha1.CronAnythingSpec{
				Schedule:                testSchedule,
				Template:                runtime.RawExtension{Raw: backup},
				ResourceBaseName:        pointer.StringPtr("test-backup-schedule-cron"),
				ResourceTimestampFormat: pointer.StringPtr("20060102-150405"),
				TriggerDeadlineSeconds:  pointer.Int64Ptr(60),
			},
			wantCronSpecStr: wantCronStr,
		},
		{
			name: "unchanged",
			oldCronSpec: &v1alpha1.CronAnythingSpec{
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
			cronAnythingCtrl.get = func(key types.NamespacedName) (v1alpha1.CronAnything, error) {
				return &fakeCronAnything{
					Spec: *tc.oldCronSpec,
				}, nil
			}
			var gotCronSpecStr string
			cronAnythingCtrl.update = func(cron v1alpha1.CronAnything) error {
				b, err := yaml.Marshal(cron.(*fakeCronAnything).Spec)
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
			diffSpecs(t, gotCronSpecStr, tc.wantCronSpecStr)
		})
	}
}

func TestReconcileWithBackupPrune(t *testing.T) {
	reconciler, backupScheduleCtrl, cronAnythingCtrl, backupCtrl := newTestBackupScheduleReconciler()
	schedule := mockBackupSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testBackupScheduleName,
			Namespace: testNamespace,
		},
		Spec: mockBackupScheduleSpec{
			BackupScheduleSpec: v1alpha1.BackupScheduleSpec{
				Schedule:              testSchedule,
				BackupRetentionPolicy: &v1alpha1.BackupRetentionPolicy{BackupRetention: pointer.Int32Ptr(2)},
			},
			BackupSpec: v1alpha1.BackupSpec{
				Instance: "mydb1",
				Type:     v1alpha1.BackupTypePhysical,
			},
		},
	}
	backupBytes, err := getBackupBytes(&schedule)
	if err != nil {
		t.Fatalf("failed to parse backup byptes: %v", err)
	}

	backupScheduleCtrl.get = func(_, _ string) (v1alpha1.BackupSchedule, error) {
		return &schedule, nil
	}
	backupScheduleCtrl.updateStatus = func(backupSchedule v1alpha1.BackupSchedule) error {
		return nil
	}
	backupScheduleCtrl.getBackupBytes = getBackupBytes

	cronAnythingCtrl.get = func(key types.NamespacedName) (v1alpha1.CronAnything, error) {
		return &fakeCronAnything{
			Spec: v1alpha1.CronAnythingSpec{
				Template: runtime.RawExtension{Raw: backupBytes},
			},
		}, nil
	}
	cronAnythingCtrl.update = func(cron v1alpha1.CronAnything) error {
		return nil
	}

	backups := makeSortedBackups(t, 4)

	testCases := []struct {
		name        string
		backups     []*mockBackup
		wantDeleted []*mockBackup
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
			backupCtrl.list = func(cronAnythingName string) ([]v1alpha1.Backup, error) {
				var backups []v1alpha1.Backup
				for _, b := range tc.backups {
					backups = append(backups, b)
				}
				return backups, nil
			}
			var gotDeleted []*mockBackup
			backupCtrl.delete = func(backup v1alpha1.Backup) error {
				gotDeleted = append(gotDeleted, backup.(*mockBackup))
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
			sortedBackups := makeSortedBackups(t, tc.backupTotal)
			schedule := &mockBackupSchedule{
				Spec: mockBackupScheduleSpec{
					BackupScheduleSpec: v1alpha1.BackupScheduleSpec{
						BackupRetentionPolicy: &v1alpha1.BackupRetentionPolicy{BackupRetention: pointer.Int32Ptr(100)},
					},
				},
			}
			var gotSchedule *mockBackupSchedule
			backupScheduleCtrl.updateStatus = func(backupSchedule v1alpha1.BackupSchedule) error {
				gotSchedule = backupSchedule.(*mockBackupSchedule)
				return nil
			}
			var backups []v1alpha1.Backup
			for _, b := range sortedBackups {
				backups = append(backups, b)
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
				if gotSchedule.Status.BackupHistory[i].CreationTime != backups[i].GetCreationTimestamp() {
					t.Errorf("reconciler.UpdateHistory BackupHistory[%d] got %v, want %v", i, gotSchedule.Status.BackupHistory[i], backups[i])
				}
			}
		})
	}
}

func newTestBackupScheduleReconciler() (reconciler *BackupScheduleReconciler,
	backupScheduleCtrl *mockBackupScheduleControl,
	cronAnythingCtrl *mockCronAnythingControl,
	backupCtrl *mockBackupControl) {

	backupScheduleCtrl = &mockBackupScheduleControl{}
	cronAnythingCtrl = &mockCronAnythingControl{}
	backupCtrl = &mockBackupControl{}
	scheme := runtime.NewScheme()

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

func makeSortedBackups(t *testing.T, total int) (backups []*mockBackup) {
	timestamp := timeFromStr(t, "2020-12-21T04:00:00Z")
	for i := 0; i < total; i++ {
		backups = append(backups, &mockBackup{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(timestamp),
			},
			Status: v1alpha1.BackupStatus{
				Phase: v1alpha1.BackupSucceeded,
			},
		})
		timestamp = timestamp.Add(-1 * time.Hour)
	}
	return backups
}

var _ v1alpha1.BackupSchedule = &mockBackupSchedule{}

type mockBackupSchedule struct {
	runtime.Object
	metav1.TypeMeta
	metav1.ObjectMeta
	Spec   mockBackupScheduleSpec
	Status v1alpha1.BackupScheduleStatus
}

type mockBackupScheduleSpec struct {
	v1alpha1.BackupScheduleSpec
	BackupSpec v1alpha1.BackupSpec
}

func (in *mockBackupSchedule) BackupScheduleSpec() *v1alpha1.BackupScheduleSpec {
	return &in.Spec.BackupScheduleSpec
}

func (in *mockBackupSchedule) BackupScheduleStatus() *v1alpha1.BackupScheduleStatus {
	return &in.Status
}

func (in *mockBackupSchedule) GetObjectKind() schema.ObjectKind {
	return schema.EmptyObjectKind
}

var _ v1alpha1.Backup = &mockBackup{}

type mockBackup struct {
	runtime.Object
	metav1.TypeMeta
	metav1.ObjectMeta
	Spec   v1alpha1.BackupSpec
	Status v1alpha1.BackupStatus
}

func (in *mockBackup) BackupSpec() *v1alpha1.BackupSpec {
	return &in.Spec
}

func (in *mockBackup) BackupStatus() *v1alpha1.BackupStatus {
	return &in.Status
}

func (in *mockBackup) GetObjectKind() schema.ObjectKind {
	return schema.EmptyObjectKind
}

type mockBackupScheduleControl struct {
	get            func(name, namespace string) (v1alpha1.BackupSchedule, error)
	updateStatus   func(backupSchedule v1alpha1.BackupSchedule) error
	getBackupBytes func(backupSchedule v1alpha1.BackupSchedule) ([]byte, error)
}

func (f *mockBackupScheduleControl) Get(name, namespace string) (v1alpha1.BackupSchedule, error) {
	return f.get(name, namespace)
}
func (f *mockBackupScheduleControl) UpdateStatus(backupSchedule v1alpha1.BackupSchedule) error {
	return f.updateStatus(backupSchedule)
}
func (f *mockBackupScheduleControl) GetBackupBytes(backupSchedule v1alpha1.BackupSchedule) ([]byte, error) {
	return f.getBackupBytes(backupSchedule)
}

type mockCronAnythingControl struct {
	create func(ns string, name string, cas v1alpha1.CronAnythingSpec, owner v1alpha1.BackupSchedule) error
	get    func(key client.ObjectKey) (v1alpha1.CronAnything, error)
	update func(cron v1alpha1.CronAnything) error
}

func (f *mockCronAnythingControl) Create(ns string, name string, cas v1alpha1.CronAnythingSpec, owner v1alpha1.BackupSchedule) error {
	return f.create(ns, name, cas, owner)
}
func (f *mockCronAnythingControl) Get(key client.ObjectKey) (v1alpha1.CronAnything, error) {
	return f.get(key)
}
func (f *mockCronAnythingControl) UpdateStatus(cron v1alpha1.CronAnything) error {
	return f.update(cron)
}

type mockBackupControl struct {
	list   func(cronAnythingName string) ([]v1alpha1.Backup, error)
	delete func(backup v1alpha1.Backup) error
}

func (f *mockBackupControl) List(cronAnythingName string) ([]v1alpha1.Backup, error) {
	return f.list(cronAnythingName)
}
func (f *mockBackupControl) Delete(backup v1alpha1.Backup) error {
	return f.delete(backup)
}

func diffSpecs(t *testing.T, got, want string) {
	if diff := cmp.Diff(got, want); diff != "" {
		t.Errorf("\n===== GOT SPEC =====\n%s\n===== WANT SPEC =====\n%s\n\n====== Diff ======\n%s\n", got, want, diff)
	}
}

func getBackupBytes(backupSchedule v1alpha1.BackupSchedule) ([]byte, error) {
	specBytes, err := json.Marshal(backupSchedule.(*mockBackupSchedule).Spec.BackupSpec)
	if err != nil {
		return nil, err
	}

	var specMap map[string]interface{}
	err = json.Unmarshal(specBytes, &specMap)
	if err != nil {
		return nil, err
	}

	backupMap := make(map[string]interface{})
	backupMap["apiVersion"] = "mock.db.anthosapis.com/v1alpha1"
	backupMap["kind"] = "Backup"
	backupMap["spec"] = specMap
	return json.Marshal(backupMap)
}
