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
	"k8s.io/apimachinery/pkg/runtime"
)

//+kubebuilder:object:generate=true

// BackupSpec defines the desired state of a backup.
type BackupSpec struct {

	// Instance is a name of an instance to take a backup for.
	// +required
	Instance string `json:"instance,omitempty"`

	// Type describes a type of a backup to take. Immutable.
	// Available options are:
	// - Snapshot: storage level disk snapshot.
	// - Physical: database engine specific backup that relies on a redo stream /
	//             continuous archiving (WAL) and may allow a PITR.
	//             Examples include pg_backup, pgBackRest, mysqlbackup.
	//             A Physical backup may be file based or database block based
	//	       (e.g. Oracle RMAN).
	// - Logical: database engine specific backup that relies on running SQL
	//            statements, e.g. mysqldump, pg_dump, expdp.
	// If not specified, the default of Snapshot is assumed.
	// +kubebuilder:validation:Enum=Snapshot;Physical;Logical
	// +optional
	Type BackupType `json:"type,omitempty"`

	// KeepDataOnDeletion defines whether to keep backup data
	// when backup resource is removed. The default value is false.
	// +optional
	KeepDataOnDeletion bool `json:"keepDataOnDeletion,omitempty"`
}

// BackupType is presently defined as a free formatted string.
type BackupType string

const (
	// See Backup.Spec.Type definition above for explanation
	// on what Snapshot, Physical and Logical backups are.
	BackupTypePhysical BackupType = "Physical"
	BackupTypeLogical  BackupType = "Logical"
	BackupTypeSnapshot BackupType = "Snapshot"
)

//+kubebuilder:object:generate=true

// BackupStatus defines the observed state of a backup.
type BackupStatus struct {
	// Phase is a summary of current state of the Backup.
	// +optional
	Phase BackupPhase `json:"phase,omitempty"`

	// Conditions represents the latest available observations
	// of the backup's current state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

type Backup interface {
	runtime.Object
	metav1.Object
	BackupSpec() *BackupSpec
	BackupStatus() *BackupStatus
}
