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

// This is the contract. This CronAnything is Anthos DB Operator compliant.
var _ commonv1alpha1.CronAnything = &CronAnything{}

// CronAnythingSpec defines the common specifications for a CronAnything.
func (i *CronAnything) CronAnythingSpec() *commonv1alpha1.CronAnythingSpec {
	return &i.Spec.CronAnythingSpec
}

// CronAnythingStatus defines the common status for a CronAnything.
func (i *CronAnything) CronAnythingStatus() *commonv1alpha1.CronAnythingStatus {
	return &i.Status.CronAnythingStatus
}

// CronAnythingSpec defines the desired state of CronAnything.
type CronAnythingSpec struct {
	// CronAnythingSpec represents the database engine agnostic
	// part of the spec describing the desired state of an CronAnything.
	commonv1alpha1.CronAnythingSpec `json:",inline"`
}

// CronAnythingStatus defines the observed state of CronAnything.
type CronAnythingStatus struct {
	// CronAnythingStatus represents the database engine agnostic
	// part of the status describing the observed state of an CronAnything.
	commonv1alpha1.CronAnythingStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// CronAnything is the Schema for the CronAnything API.
type CronAnything struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CronAnythingSpec   `json:"spec,omitempty"`
	Status CronAnythingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CronAnythingList contains a list of CronAnything.
type CronAnythingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CronAnything `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CronAnything{}, &CronAnythingList{})
}
