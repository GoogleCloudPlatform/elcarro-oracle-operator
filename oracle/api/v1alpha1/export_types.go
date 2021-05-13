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

// ExportSpec defines the desired state of Export
type ExportSpec struct {
	// Instance is the resource name within namespace to export from.
	// +required
	Instance string `json:"instance"`

	// DatabaseName is the database resource name within Instance to export from.
	// +required
	DatabaseName string `json:"databaseName"`

	// Type of the Export. If omitted, the default of DataPump is assumed.
	// +kubebuilder:validation:Enum=DataPump
	// +optional
	Type string `json:"type,omitempty"`

	// ExportObjectType is the type of objects to export. If omitted, the default
	// of Schemas is assumed.
	// Supported options at this point are: Schemas or Tables.
	// +kubebuilder:validation:Enum=Schemas;Tables
	// +optional
	ExportObjectType string `json:"exportObjectType,omitempty"`

	// ExportObjects are objects, schemas or tables, exported by DataPump.
	// +required
	ExportObjects []string `json:"exportObjects,omitempty"`

	// GcsPath is a full path in GCS bucket to transfer exported files to.
	// A user is to ensure proper write access to the bucket from within the
	// Oracle Operator.
	// +required
	GcsPath string `json:"gcsPath,omitempty"`

	// GcsLogPath is an optional full path in GCS. If set up ahead of time, export
	// logs can be optionally transferred to set GCS bucket. A user is to ensure
	// proper write access to the bucket from within the Oracle Operator.
	// +optional
	GcsLogPath string `json:"gcsLogPath,omitempty"`

	// FlashbackTime is an optional time. If this time is set, the SCN that most
	// closely matches the time is found, and this SCN is used to enable the
	// Flashback utility. The export operation is performed with data that is
	// consistent up to this SCN.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Format=date-time
	// +optional
	FlashbackTime *metav1.Time `json:"flashbackTime,omitempty"`
}

// ExportStatus defines the observed state of Export.
type ExportStatus struct {
	// Conditions represents the latest available observations
	// of the export's current state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:JSONPath=".spec.instance",name="Instance Name",type="string"
// +kubebuilder:printcolumn:JSONPath=".spec.databaseName",name="Database Name",type="string"
// +kubebuilder:printcolumn:JSONPath=".spec.exportObjectType",name="Export Object Type",type="string"
// +kubebuilder:printcolumn:JSONPath=".spec.exportObjects",name="Export Objects",type="string"
// +kubebuilder:printcolumn:JSONPath=".spec.gcsPath",name="GCS Path",type="string"
// +kubebuilder:printcolumn:JSONPath=".spec.gcsLogPath",name="GCS Log Path",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].status`,name="ReadyStatus",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,name="ReadyReason",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].message`,name="ReadyMessage",type="string",priority=1

// Export is the Schema for the exports API.
type Export struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ExportSpec   `json:"spec,omitempty"`
	Status ExportStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ExportList contains a list of Export.
type ExportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Export `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Export{}, &ExportList{})
}
