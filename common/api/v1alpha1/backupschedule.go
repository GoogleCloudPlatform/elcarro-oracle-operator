package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

//+kubebuilder:object:generate=true

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

//+kubebuilder:object:generate=true

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
	Phase BackupPhase `json:"phase,omitempty"`
}

//+kubebuilder:object:generate=true

// BackupScheduleSpec defines the desired state of BackupSchedule.
type BackupScheduleSpec struct {
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

//+kubebuilder:object:generate=true

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

// BackupSchedule represent the contract for the Anthos DB Operator compliant
// database operator providers to abide by.
type BackupSchedule interface {
	runtime.Object
	metav1.Object
	BackupScheduleSpec() *BackupScheduleSpec
	BackupScheduleStatus() *BackupScheduleStatus
}
