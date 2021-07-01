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

import "k8s.io/apimachinery/pkg/api/resource"

//+kubebuilder:object:generate=true

// DiskSpec defines the desired state of a disk.
// (the structure is deliberately designed to be flexible, as a slice,
// so that if we change a disk layout for different hosting platforms,
// the model can be also adjusted to reflect that).
type DiskSpec struct {
	// Name of a disk.
	// Allowed values are: DataDisk,LogDisk,BackupDisk
	// +required
	// +kubebuilder:validation:Enum=DataDisk;LogDisk;BackupDisk
	Name string `json:"name"`

	// Disk size. If not specified, the defaults are: DataDisk:"100Gi", LogDisk:"150Gi",BackupDisk:"100Gi"
	// +optional
	Size resource.Quantity `json:"size,omitempty"`

	// StorageClass points to a particular CSI driver and is used
	// for disk provisioning.
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// A map of string keys and values that can be used by external tooling to
	// store and retrieve for the disk PVC.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// DiskType is a type that points to the disk type
type DiskType string
