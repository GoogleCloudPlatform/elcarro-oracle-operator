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

// BackupSpec defines the desired state of Backup.
type BackupSpec struct {
	// Backup specs that are common across all database engines.
	commonv1alpha1.BackupSpec `json:",inline"`

	// Backup sub-type, which is only relevant for a Physical backup type
	// (e.g. RMAN). If omitted, the default of Instance(Level) is assumed.
	// Supported options at this point are: Instance or Database level backups.
	// +kubebuilder:validation:Enum=Instance;Database;Tablespace;Datafile
	// +optional
	Subtype string `json:"subType,omitempty"`

	// VolumeSnapshotClass points to a particular CSI driver and is used
	// for taking a volume snapshot. If requested here at the Backup
	// level, this setting overrides the platform default as well
	// as the default set via the Config (global user preferences).
	VolumeSnapshotClass string `json:"volumeSnapshotClass,omitempty"`

	// For a Physical backup this slice can be used to indicate what
	// PDBs, schemas, tablespaces or tables to back up.
	// +optional
	BackupItems []string `json:"backupItems,omitempty"`

	// For a Physical backup the choices are Backupset and Image Copies.
	// Backupset is the default, but if Image Copies are required,
	// flip this flag to false.
	// +optional
	Backupset *bool `json:"backupset,omitempty"`

	// For a Physical backup, optionally turn on compression,
	// by flipping this flag to true. The default is false.
	Compressed bool `json:"compressed,omitempty"`

	// For a Physical backup, optionally turn on an additional "check
	// logical" option. The default is off.
	// +optional
	CheckLogical bool `json:"checkLogical,omitempty"`

	// For a Physical backup, optionally indicate a degree of parallelism
	// also known as DOP.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	Dop int32 `json:"dop,omitempty"`

	// For a Physical backup, optionally specify an incremental level.
	// The default is 0 (the whole database).
	// +optional
	Level int32 `json:"level,omitempty"`

	// For a Physical backup, optionally specify filesperset.
	// The default depends on a type of backup, generally 64.
	// +optional
	Filesperset int32 `json:"filesperset,omitempty"`

	// For a Physical backup, optionally specify a section size in MB.
	// Don't include the unit (MB), just the integer.
	// +optional
	SectionSize int32 `json:"sectionSize,omitempty"`

	// For a Physical backup, optionally specify the time threshold.
	// If a threshold is reached, the backup request would time out and
	// error out. The threshold is expressed in minutes.
	// Don't include the unit (minutes), just the integer.
	// +optional
	TimeLimitMinutes int32 `json:"timeLimitMinutes,omitempty"`

	// For a Physical backup, optionally specify a local backup dir.
	// If omitted, /u03/app/oracle/rman is assumed.
	// +optional
	LocalPath string `json:"localPath,omitempty"`

	// If set up ahead of time, the backup sets of a physical backup can be
	// optionally transferred to a GCS bucket.
	// A user is to ensure proper write access to the bucket from within the
	// Oracle Operator.
	// +optional
	GcsPath string `json:"gcsPath,omitempty"`
}

// BackupStatus defines the observed state of Backup.
type BackupStatus struct {
	// Backup status that is common across all database engines.
	commonv1alpha1.BackupStatus `json:",inline"`

	BackupID   string `json:"backupid,omitempty"`
	BackupTime string `json:"backuptime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:JSONPath=".spec.instance",name="Instance Name",type="string"
// +kubebuilder:printcolumn:JSONPath=".spec.type",name="Backup Type",type="string"
// +kubebuilder:printcolumn:JSONPath=".spec.subType",name="Backup SubType",type="string"
// +kubebuilder:printcolumn:JSONPath=".spec.dop",name="DOP",type="integer"
// +kubebuilder:printcolumn:JSONPath=".spec.backupset",name="BS/IC",type="boolean"
// +kubebuilder:printcolumn:JSONPath=".spec.gcsPath",name="GCS Path",type="string"
// +kubebuilder:printcolumn:JSONPath=".status.phase",name="Phase",type="string"
// +kubebuilder:printcolumn:JSONPath=".status.backupid",name="Backup ID",type="string"
// +kubebuilder:printcolumn:JSONPath=".status.backuptime",name="Backup Time",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].status`,name="ReadyStatus",type="string",priority=1
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,name="ReadyReason",type="string",priority=1
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].message`,name="ReadyMessage",type="string",priority=1

// Backup is the Schema for the backups API.
type Backup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupSpec   `json:"spec,omitempty"`
	Status BackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupList contains a list of Backup.
type BackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Backup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Backup{}, &BackupList{})
}
