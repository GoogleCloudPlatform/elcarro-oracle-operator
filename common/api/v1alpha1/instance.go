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

// Service is a service provided by the operator.
type Service string

// InstanceMode describes how an instance will be managed by the operator.
type InstanceMode string

const (
	// Monitoring service provides the ability to collect
	// monitoring data from the database and the cluster.
	Monitoring Service = "Monitoring"

	// BackupAndRestore service provides database backups and restore functionalities.
	BackupAndRestore Service = "Backup"

	// Security service
	Security Service = "Security"

	// Logging service
	Logging Service = "Logging"

	// Patching service provides software and database patching.
	Patching Service = "Patching"

	// ManuallySetUpStandby means that operator will skip DB creation during
	// provisioning, instance will be ready for users to manually set up standby.
	ManuallySetUpStandby InstanceMode = "ManuallySetUpStandby"
)

//+kubebuilder:object:generate=true

// InstanceSpec represents the database engine agnostic
// part of the spec describing the desired state of an Instance.
type InstanceSpec struct {
	// Type of a database engine.
	// +required
	// +kubebuilder:validation:Enum=Oracle
	Type string `json:"type,omitempty"`

	// HostingType conveys whether an Instance is meant to be hosted on a cloud
	// (single or multiple), on-prem, on Bare Metal, etc.
	// It is meant to be used as a filter and aggregation dimension.
	// +optional
	// +kubebuilder:validation:Enum="";Cloud;MultiCloud;Hybrid;BareMetal;OnPrem
	HostingType string `json:"hostingType,omitempty"`

	// DeploymentType reflects a fully managed (DBaaS) vs. semi-managed database.
	// +optional
	// +kubebuilder:validation:Enum="";InCluster;CloudSQL;RDS
	DeploymentType string `json:"deploymentType,omitempty"`

	// CloudProvider is only relevant if the hosting type is Cloud,
	// MultiCloud, Hybrid or Bare Metal.
	// +optional
	// +kubebuilder:validation:Enum=GCP;AWS;Azure;OCI
	CloudProvider string `json:"cloudProvider,omitempty"`

	// Version of a database.
	// +required
	Version string `json:"version,omitempty"`

	// Edition of a database.
	// +required
	Edition string `json:"edition,omitempty"`

	// Disks slice describes at minimum two disks:
	// data and log (archive log), and optionally a backup disk.
	Disks []DiskSpec `json:"disks,omitempty"`

	// Service agent and other data plane GCR images.
	// This is an optional map that allows a customer to specify GCR images
	// different from those chosen/provided.
	// +optional
	Images map[string]string `json:"images,omitempty"`

	// Source IP CIDR ranges allowed for a client.
	// +optional
	SourceCidrRanges []string `json:"sourceCidrRanges,omitempty"`

	// Parameters contains the database flags in the map format
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`

	// Patching contains all the patching related attributes like patch version and image.
	// +optional
	Patching *PatchingSpec `json:"patching,omitempty"`

	// Services list the optional semi-managed services that
	// the customers can choose from.
	// +optional
	Services map[Service]bool `json:"services,omitempty"`

	// MinMemoryForDBContainer overrides the default safe limit for
	// scheduling the db container without crashes due to memory pressure.
	// +optional
	MinMemoryForDBContainer string `json:"minMemoryForDBContainer,omitempty"`

	// MaintenanceWindow specifies the time windows during which database downtimes are allowed for maintenance.
	// +optional
	MaintenanceWindow *MaintenanceWindowSpec `json:"maintenanceWindow,omitempty"`

	// Mode specifies how this instance will be managed by the operator.
	// +optional
	// +kubebuilder:validation:Enum=ManuallySetUpStandby
	Mode InstanceMode `json:"mode,omitempty"`
}

// PatchingSpec contains the patching related details.
type PatchingSpec struct {
	// Patch version
	PatchVersion string `json:"patchVersion,omitempty"`
	// gcr link containing the patched service image.
	PatchedServiceImage string `json:"patchedServiceImage,omitempty"`
}

//+kubebuilder:object:generate=true

// InstanceStatus defines the observed state of Instance
type InstanceStatus struct {
	// Phase is a summary of current state of the Instance.
	// +optional
	Phase InstancePhase `json:"phase,omitempty"`

	// Conditions represents the latest available observations
	// of the Instance's current state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Endpoint is presently expressed in the format of <instanceName>-svc.<ns>.
	Endpoint string `json:"endpoint,omitempty"`

	// URL represents an IP and a port number info needed in order to
	// establish a database connection from outside a cluster.
	URL string `json:"url,omitempty"`

	// Description is for a human consumption.
	// E.g. when an Instance is restored from a backup
	// this field is populated with the human readable
	// restore details.
	Description string `json:"description,omitempty"`

	// ObservedGeneration is the latest generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// IsChangeApplied indicates whether instance changes have been applied
	// +optional
	IsChangeApplied metav1.ConditionStatus `json:"isChangeApplied,omitempty"`
}

// Instance represents the contract for the Anthos DB Operator compliant
// database Operator providers to abide by.
type Instance interface {
	runtime.Object
	InstanceSpec() InstanceSpec
	InstanceStatus() InstanceStatus
}
