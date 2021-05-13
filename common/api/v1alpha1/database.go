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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

//+kubebuilder:object:generate=true

// DatabaseSpec defines the desired state of Database.
type DatabaseSpec struct {
	// Name of the instance that the database belongs to.
	// +required
	Instance string `json:"instance,omitempty"`

	// Name of the database.
	// +required
	Name string `json:"name,omitempty"`
}

//+kubebuilder:object:generate=true

// DatabaseStatus defines the observed state of Database
type DatabaseStatus struct {
	// Phase is a summary of the current state of the Database.
	// +optional
	Phase DatabasePhase `json:"phase,omitempty"`

	// Conditions represents the latest available observations of the
	// Database's current state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
