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
)

// PITRSpec defines the desired state of PITR
type PITRSpec struct {
	// Images defines PITR service agent images.
	// This is a required map that allows a customer to specify GCR images.
	// +required
	Images map[string]string `json:"images"`

	// InstanceRef references to the instance that the PITR applies to.
	// +required
	InstanceRef *InstanceReference `json:"instanceRef"`

	// StorageURI is the URI to store PITR backups and redo logs.
	// Currently only gs:// (GCS) schemes are supported.
	// +required
	StorageURI string `json:"storageURI,omitempty"`

	// Schedule is a cron-style expression of the schedule on which Backup will
	// be created for PITR. For allowed syntax, see en.wikipedia.org/wiki/Cron and
	// godoc.org/github.com/robfig/cron. Default to backup every 4 hours.
	// +optional
	BackupSchedule string `json:"backupSchedule,omitempty"`
}

// InstanceReference represents a database instance Reference. It has enough
// information to retrieve a database instance object.
type InstanceReference struct {
	// `name` is the name of a database instance.
	// +required
	Name string `json:"name,omitempty"`
}

// PITRStatus defines the observed state of PITR
type PITRStatus struct {
	// BackupTotal stores the total number of current existing backups managed
	// by a PITR.
	// +optional
	BackupTotal int `json:"backupTotal"`

	// AvailableRecoveryWindowTime represents the actual PITR recoverable time
	// ranges for an instance in the current timeline/incarnation.
	// +optional
	AvailableRecoveryWindowTime []TimeWindow `json:"availableRecoveryWindowTime,omitempty"`

	// AvailableRecoveryWindowSCN represents the actual PITR recoverable
	// SCN ranges for an instance in the current timeline/incarnation.
	// +optional
	AvailableRecoveryWindowSCN []SCNWindow `json:"availableRecoveryWindowSCN,omitempty"`

	// CurrentDatabaseIncarnation stores the current database incarnation number
	// for the PITR enabled instance.
	// +optional
	CurrentDatabaseIncarnation string `json:"currentDatabaseIncarnation,omitempty"`

	// Conditions represents the latest available observations
	// of the PITR's current state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

type TimeWindow struct {
	// Begin time.
	// +required
	Begin metav1.Time `json:"begin,omitempty"`

	// End time.
	// +required
	End metav1.Time `json:"end,omitempty"`
}

type SCNWindow struct {
	// Begin SCN.
	// +required
	Begin string `json:"begin,omitempty"`

	// End SCN.
	// +required
	End string `json:"end,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// PITR is the Schema for the PITR API
type PITR struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PITRSpec   `json:"spec,omitempty"`
	Status PITRStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PITRList contains a list of PITR
type PITRList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PITR `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PITR{}, &PITRList{})
}
