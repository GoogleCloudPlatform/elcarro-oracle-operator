package backupcontroller

import (
	"testing"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateBackupSpec(t *testing.T) {
	backupCtrl := &RealBackupControl{}
	testCases := []struct {
		name    string
		spec    v1alpha1.BackupSpec
		wantRes bool
	}{
		{
			name: "Valid physical backup spec",
			spec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     commonv1alpha1.BackupTypePhysical,
				},
				Subtype: "Instance",
			},
			wantRes: true,
		}, {
			name: "Valid snapshot backup spec",
			spec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     commonv1alpha1.BackupTypeSnapshot,
				},
				Subtype: "Instance",
			},
			wantRes: true,
		}, {
			name: "Invalid backup type", spec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     "InvalidType",
				},
				Subtype: "Instance",
			},
			wantRes: false,
		}, {
			name: "Invalid subtype for snapshot backup",
			spec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Instance: testInstanceName,
					Type:     commonv1alpha1.BackupTypeSnapshot,
				},
				Subtype: "Database",
			},
			wantRes: false,
		}, {
			name: "Invalid missing spec.instance",
			spec: v1alpha1.BackupSpec{
				BackupSpec: commonv1alpha1.BackupSpec{
					Type: commonv1alpha1.BackupTypePhysical,
				},
				Subtype: "Instance",
			},
			wantRes: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotRes := backupCtrl.ValidateBackupSpec(newBackupWithSpec(tc.spec))
			if gotRes != tc.wantRes {
				t.Errorf("backupControl.validateBackupSpec got unexpected result, got:%v, want:%v", gotRes, tc.wantRes)
			}
		})
	}
}

func newBackupWithSpec(spec v1alpha1.BackupSpec) *v1alpha1.Backup {
	b := v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testBackupName,
			Namespace: testNamespace,
		},
		Spec: spec,
	}
	return &b
}
