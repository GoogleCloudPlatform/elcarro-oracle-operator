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

// ConfigSpec defines the desired state of Config.
type ConfigSpec struct {
	// Service agent and other data plane agent images.
	// This is an optional map that allows a customer to specify agent images
	// different from those chosen/provided by the Oracle Operator by default.
	// See an example of how this map can be used in
	// config/samples/v1alpha1_config_gcp1.yaml
	// +optional
	Images map[string]string `json:"images,omitempty"`

	// Deployment platform.
	// Presently supported values are: GCP (default), BareMetal.
	// +optional
	// +kubebuilder:validation:Enum=GCP;BareMetal;Minikube;Kind
	Platform string `json:"platform,omitempty"`

	// Disks slice describes at minimum two disks:
	// data and log (archive log), and optionally a backup disk.
	Disks []commonv1alpha1.DiskSpec `json:"disks,omitempty"`

	// Storage class to use for dynamic provisioning.
	// This value varies depending on a platform.
	// For GCP (and the default) it is "csi-gce-pd".
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// Volume Snapshot class to use for storage snapshots.
	// This value varies depending on a platform.
	// For GCP (and the default) it is "csi-gce-pd-snapshot-class".
	// +optional
	VolumeSnapshotClass string `json:"volumeSnapshotClass,omitempty"`

	// Log Levels for the various components.
	// This is an optional map for component -> log level
	// See an example of how this map can be used in
	// config/samples/v1alpha1_config_gcp1.yaml
	// +optional
	LogLevel map[string]string `json:"logLevel,omitempty"`

	// HostAntiAffinityNamespaces is an optional list of namespaces that need
	// to be included in anti-affinity by hostname rule. The effect of the rule
	// is forbidding scheduling a database pod in the current namespace on a host
	// that already runs a database pod in any of the listed namespaces.
	// +optional
	HostAntiAffinityNamespaces []string `json:"hostAntiAffinityNamespaces,omitempty"`
}

// ConfigStatus defines the observed state of Config.
type ConfigStatus struct {
}

// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:JSONPath=".spec.platform",name="Platform",type="string"
// +kubebuilder:printcolumn:name="Disk Sizes",type="string",JSONPath=".spec.diskSizes"
// +kubebuilder:printcolumn:JSONPath=".spec.storageClass",name="Storage Class",type="string"
// +kubebuilder:printcolumn:JSONPath=".spec.volumeSnapshotClass",name="Volume Snapshot Class",type="string"

// Config is the Schema for the configs API.
type Config struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConfigSpec   `json:"spec,omitempty"`
	Status ConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ConfigList contains a list of Config.
type ConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Config `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Config{}, &ConfigList{})
}
