package backupcontroller

import (
	"context"
	"fmt"
	"testing"

	"github.com/golang/protobuf/proto"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
)

func TestPhysicalBackupCreate(t *testing.T) {
	testCases := []struct {
		name                        string
		backupSpec                  v1alpha1.BackupSpec
		physicalBackupFailure       bool
		wantPhysicalBackupCalledCnt int
		wantBackupDop               int32
		wantBackupSet               bool
		wantBackupLevel             int32
		wantGcsPath                 string
		wantError                   bool
	}{
		{
			name: "Create physical backup with DOP=5",
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     commonv1alpha1.BackupTypePhysical,
				},
				Dop: 5,
			},
			wantPhysicalBackupCalledCnt: 1,
			wantBackupDop:               5,
			wantBackupSet:               true,
		}, {
			name: "Create physical backup with backupset=false",
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     commonv1alpha1.BackupTypePhysical,
				},
				Backupset: proto.Bool(false),
			},
			wantPhysicalBackupCalledCnt: 1,
			wantBackupDop:               1,
			wantBackupSet:               false,
		}, {
			name: "Create physical backup with GcsPath set",
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     commonv1alpha1.BackupTypePhysical,
				},
				GcsPath: testGCSPath,
			},
			wantPhysicalBackupCalledCnt: 1,
			wantBackupDop:               1,
			wantBackupSet:               true,
			wantGcsPath:                 testGCSPath,
		}, {
			name: "Create physical backup with level=1",
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     commonv1alpha1.BackupTypePhysical,
				},
				Level: 1,
			},
			wantPhysicalBackupCalledCnt: 1,
			wantBackupDop:               1,
			wantBackupSet:               true,
			wantBackupLevel:             1,
		}, {
			name: "Create physical backup fails",
			backupSpec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     commonv1alpha1.BackupTypePhysical,
				},
			},
			physicalBackupFailure:       true,
			wantPhysicalBackupCalledCnt: 1,
			wantBackupDop:               1,
			wantBackupSet:               true,
			wantError:                   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _, dbClient := newTestBackupReconciler()
			if tc.physicalBackupFailure {
				dbClient.SetMethodToError("RunRMANAsync", fmt.Errorf("PhysicalBackup fail."))
			}
			backup := &physicalBackup{
				r:      r,
				backup: newBackupWithSpec(tc.backupSpec),
			}
			gotErr := backup.create(context.Background())

			if tc.wantError != (gotErr != nil) {
				t.Fatalf("physicalBackup.create() returns unexpected error: wantErr:%v gotErr:%v", tc.wantError, gotErr)
			}
			wantRMANAsyncCalledCnt := tc.wantPhysicalBackupCalledCnt
			if dbClient.RunRMANAsyncCalledCnt() != wantRMANAsyncCalledCnt {
				t.Errorf("physicalBackup.create() make unexpected number of calls to dbClient.RunRMANAsync(): want:%v got:%v", wantRMANAsyncCalledCnt, dbClient.RunRMANAsyncCalledCnt())
			}
			if dbClient.GotRMANAsyncRequest.SyncRequest.GetGcsPath() != tc.wantGcsPath {
				t.Errorf("Unexpected PhysicalBackupRequest.GcsPath: want:%v got:%v", tc.wantGcsPath, dbClient.GotRMANAsyncRequest.SyncRequest.GetGcsPath())
			}
		})
	}
}

func TestPhysicalBackupStatus(t *testing.T) {
	testCases := []struct {
		name            string
		operationStatus testhelpers.FakeOperationStatus
		wantDone        bool
		wantError       bool
	}{
		{
			name:            "return false when lro operation is in progress",
			operationStatus: testhelpers.StatusRunning,
			wantDone:        false,
		}, {
			name:            "return true when lro operation is done",
			operationStatus: testhelpers.StatusDone,
			wantDone:        true,
		}, {
			name:            "return error when lro operation is done with error",
			operationStatus: testhelpers.StatusDoneWithError,
			wantDone:        true,
			wantError:       true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _, dbClient := newTestBackupReconciler()

			dbClient.SetNextGetOperationStatus(tc.operationStatus)
			backup := &physicalBackup{
				r:      r,
				backup: newBackupWithSpec(v1alpha1.BackupSpec{}),
				log:    r.Log,
			}
			gotDone, gotErr := backup.status(context.Background())
			if tc.wantError != (gotErr != nil) {
				t.Fatalf("physicalBackup.status() returns unexpected error: wantErr:%v gotErr:%v", tc.wantError, gotErr)
			}
			if tc.wantDone != gotDone {
				t.Fatalf("physicalBackup.status() returns unexpected done: wantDone:%v gotDone:%v", tc.wantDone, gotDone)
			}
		})
	}
}
