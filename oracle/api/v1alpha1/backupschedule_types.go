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

// This is the contract. This BackupSchedule is Anthos DB Operator compliant.
var _ commonv1alpha1.BackupSchedule = &BackupSchedule{}

// BackupScheduleSpec defines the common specifications for a BackupSchedule.
func (i *BackupSchedule) BackupScheduleSpec() *commonv1alpha1.BackupScheduleSpec {
	return &i.Spec.BackupScheduleSpec
}

// BackupScheduleStatus defines the common status for a BackupSchedule.
func (i *BackupSchedule) BackupScheduleStatus() *commonv1alpha1.BackupScheduleStatus {
	return &i.Status.BackupScheduleStatus
}

type BackupScheduleSpec struct {
	commonv1alpha1.BackupScheduleSpec `json:",inline"`

	// BackupSpec defines the Backup that will be created on the provided schedule.
	BackupSpec BackupSpec `json:"backupSpec"`
}

// BackupScheduleStatus defines the observed state of BackupSchedule.
type BackupScheduleStatus struct {
	commonv1alpha1.BackupScheduleStatus `json:",inline"`
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
