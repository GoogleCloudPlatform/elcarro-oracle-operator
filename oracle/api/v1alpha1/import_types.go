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

// ImportSpec defines the desired state of Import.
type ImportSpec struct {
	// Instance is the resource name within same namespace to import into.
	// +required
	Instance string `json:"instance,omitempty"`

	// DatabaseName is the database resource name within Instance to import into.
	// +required
	DatabaseName string `json:"databaseName,omitempty"`

	// Type of the Import. If not specified, the default of DataPump is assumed,
	// which is the only supported option currently.
	// +kubebuilder:validation:Enum=DataPump
	// +optional
	Type string `json:"type,omitempty"`

	// GcsPath is a full path to the input file in GCS containing import data.
	// A user is to ensure proper write access to the bucket from within the
	// Oracle Operator.
	// +required
	GcsPath string `json:"gcsPath,omitempty"`

	// GcsLogPath is an optional path in GCS to copy import log to.
	// A user is to ensure proper write access to the bucket from within the
	// Oracle Operator.
	// +optional
	GcsLogPath string `json:"gcsLogPath,omitempty"`

	// Options is a map of options and their values for usage with the
	// specified Import Type. Right now this is only supported for passing
	// additional impdp specific options.
	// +optional
	Options map[string]string `json:"options,omitempty"`
}

// ImportStatus defines the observed state of Import.
type ImportStatus struct {
	// Conditions represents the latest available observations
	// of the import's current state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:JSONPath=".spec.instance",name="Instance Name",type="string"
// +kubebuilder:printcolumn:JSONPath=".spec.databaseName",name="Database Name",type="string"
// +kubebuilder:printcolumn:JSONPath=".spec.gcsPath",name="GCS Path",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].status`,name="ReadyStatus",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,name="ReadyReason",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].message`,name="ReadyMessage",type="string",priority=1
// +kubebuilder:printcolumn:JSONPath=".spec.gcsLogPath",name="GCS Log Path",type="string"

// Import is the Schema for the imports API.
type Import struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ImportSpec   `json:"spec,omitempty"`
	Status ImportStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ImportList contains a list of Import.
type ImportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Import `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Import{}, &ImportList{})
}
