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

//+kubebuilder:object:generate=true

// UserSpec defines the common desired state of User.
type UserSpec struct {
	// Name of the Instance that the User belongs to. Immutable.
	Instance string `json:"instance,omitempty"`

	// Name of the User.
	// +required
	Name string `json:"name,omitempty"`

	// Credential of the User. See definition for 'CredentialSpec'.
	// +required
	CredentialSpec `json:",inline"`
}

//+kubebuilder:object:generate=true

// UserStatus defines the observed state of User
type UserStatus struct {
	// Phase defines where the User is in its lifecycle.
	// +optional
	Phase UserPhase `json:"phase,omitempty"`

	// Conditions represent the current state of the User.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Description is for a human consumption.
	Description string `json:"description,omitempty"`
}
