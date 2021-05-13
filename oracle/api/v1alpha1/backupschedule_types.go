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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
)

// BackupRetentionPolicy is a policy used to trigger automatic deletion of
// backups produced by a particular schedule. Deletion will be triggered by
// count (keeping a maximum number of backups around).
type BackupRetentionPolicy struct {
	// BackupRetention is the number of successful backups to keep around.
	// The default is 7.
	// A value of 0 means "do not delete backups based on count". Max of 512
	// allows for ~21 days of hourly backups or ~1.4 years of daily backups.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=512
	// +optional
	BackupRetention *int32 `json:"backupRetention,omitempty"`
}

// BackupHistoryRecord is a historical record of a Backup.
type BackupHistoryRecord struct {
	// BackupName is the name of the Backup that gets created.
	// +nullable
	BackupName string `json:"backupName"`

	// CreationTime is the time that the Backup gets created.
	// +nullable
	CreationTime metav1.Time `json:"creationTime"`

	// Phase tells the state of the Backup.
	// +optional
	Phase commonv1alpha1.BackupPhase `json:"phase,omitempty"`
}

// BackupScheduleSpec defines the desired state of BackupSchedule.
type BackupScheduleSpec struct {
	// BackupSpec defines the Backup that will be created on the provided schedule.
	BackupSpec BackupSpec `json:"backupSpec"`

	// Schedule is a cron-style expression of the schedule on which Backup will
	// be created. For allowed syntax, see en.wikipedia.org/wiki/Cron and
	// godoc.org/github.com/robfig/cron.
	Schedule string `json:"schedule"`

	// Suspend tells the controller to suspend operations - both creation of new
	// Backup and retention actions. This will not have any effect on backups
	// currently in progress. Default is false.
	// +optional
	Suspend *bool `json:"suspend,omitempty"`

	// StartingDeadlineSeconds is an optional deadline in seconds for starting the
	// backup creation if it misses scheduled time for any reason.
	// The default is 30 seconds.
	// +optional
	StartingDeadlineSeconds *int64 `json:"startingDeadlineSeconds,omitempty"`

	// BackupRetentionPolicy is the policy used to trigger automatic deletion of
	// backups produced from this BackupSchedule.
	// +optional
	BackupRetentionPolicy *BackupRetentionPolicy `json:"backupRetentionPolicy,omitempty"`
}

// BackupScheduleStatus defines the observed state of BackupSchedule.
type BackupScheduleStatus struct {
	// LastBackupTime is the time the last Backup was created for this
	// BackupSchedule.
	// +optional
	// +nullable
	LastBackupTime *metav1.Time `json:"lastBackupTime,omitempty"`

	// Conditions of the BackupSchedule.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// BackupTotal stores the total number of current existing backups created
	// by this backupSchedule.
	BackupTotal *int32 `json:"backupTotal,omitempty"`

	// BackupHistory stores the records for up to 7 of the latest backups.
	// +optional
	BackupHistory []BackupHistoryRecord `json:"backupHistory,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// BackupSchedule is the Schema for the backupschedules API.
type BackupSchedule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupScheduleSpec   `json:"spec,omitempty"`
	Status BackupScheduleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupScheduleList contains a list of BackupSchedule.
type BackupScheduleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackupSchedule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BackupSchedule{}, &BackupScheduleList{})
}
