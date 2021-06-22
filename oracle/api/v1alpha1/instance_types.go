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

// This is the contract. This Instance is Anthos DB Operator compliant.
var _ commonv1alpha1.Instance = &Instance{}

// InstanceSpec defines the common specifications for an Instance.
func (i *Instance) InstanceSpec() commonv1alpha1.InstanceSpec {
	return i.Spec.InstanceSpec
}

// InstanceStatus defines the common status for an Instance.
func (i *Instance) InstanceStatus() commonv1alpha1.InstanceStatus {
	return i.Status.InstanceStatus
}

// Service is an Oracle Operator provided service.
type Service string

// InstanceSpec defines the desired state of Instance.
type InstanceSpec struct {
	// InstanceSpec represents the database engine agnostic
	// part of the spec describing the desired state of an Instance.
	commonv1alpha1.InstanceSpec `json:",inline"`

	// Restore and recovery request details.
	// This section should normally be commented out unless an actual
	// restore/recovery is required.
	// +optional
	Restore *RestoreSpec `json:"restore,omitempty"`

	// DatabaseUID represents an OS UID of a user running a database.
	// +optional
	DatabaseUID *int64 `json:"databaseUID,omitempty"`

	// DatabaseGID represents an OS group ID of a user running a database.
	// +optional
	DatabaseGID *int64 `json:"databaseGID,omitempty"`

	// DBDomain is an optional attribute to set a database domain.
	// +optional
	DBDomain string `json:"dbDomain,omitempty"`

	// CDBName is the intended name of the CDB attribute. If the CDBName is
	// different from the original name (with which the CDB was created) the
	// CDB will be renamed.
	// +optional
	CDBName string `json:"cdbName,omitempty"`

	// DBUniqueName represents a unique database name that would be
	// set for a database (if not provided, as a default,
	// the [_generic|_<zone name>] will be appended to a DatabaseName).
	// +optional
	DBUniqueName string `json:"dbUniqueName,omitempty"`

	// CharacterSet used to create a database (the default is AL32UTF8).
	// +optional
	CharacterSet string `json:"characterSet,omitempty"`

	// MemoryPercent represents the percentage of memory that should be allocated
	// for Oracle SGA (default is 25%).
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	MemoryPercent int `json:"memoryPercent,omitempty"`

	// DBNetworkServiceOptions allows to override some details of kubernetes
	// Service created to expose a connection to database.
	// +optional
	DBNetworkServiceOptions *DBNetworkServiceOptions `json:"dbNetworkServiceOptions,omitempty"`
}

type BackupReference struct {
	// `namespace` is the namespace in which the backup object is created.
	// +required
	Namespace string `json:"namespace,omitempty"`
	// `name` is the name of the backup.
	// +required
	Name string `json:"name,omitempty"`
}

// RestoreSpec defines optional restore and recovery attributes.
type RestoreSpec struct {
	// Backup type to restore from.
	// Oracle only supports: Snapshot or Physical.
	// +optional
	// +kubebuilder:validation:Enum=Snapshot;Physical
	BackupType commonv1alpha1.BackupType `json:"backupType,omitempty"`

	// Backup ID to restore from.
	// +optional
	BackupID string `json:"backupId,omitempty"`

	// Backup reference to restore from.
	// +optional
	BackupRef *BackupReference `json:"backupRef,omitempty"`

	// Similar to a (physical) backup, optionally indicate a degree
	// of parallelism, also known as DOP.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	Dop int32 `json:"dop,omitempty"`

	// Restore time limit.
	// Optional field defaulting to three times the backup time limit.
	// Don't include the unit (minutes), just the integer.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TimeLimitMinutes int32 `json:"timeLimitMinutes,omitempty"`

	// To overwrite an existing, up and running instance,
	// an explicit athorization is required. This is safeguard to avoid
	// accidentally destroying a perfectly healthy (status=Ready) instance.
	// +kubebuilder:validation:Enum=true;false
	// +optional
	Force bool `json:"force,omitempty"`

	// Request version as a date-time to avoid accidental triggering of
	// a restore operation when reapplying an older version of a resource file.
	// If at least one restore operation has occurred, any further restore
	// operation that have the same RequestTime or earlier than the last Restore
	// operation will be ignored.
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Format=date-time
	RequestTime metav1.Time `json:"requestTime"`
}

// DBNetworkServiceOptions contains customization options of kubernetes Service
// exposing a database connection.
type DBNetworkServiceOptions struct {
	// GCP contains Google Cloud specific attributes of Service configuration.
	// +optional
	GCP DBNetworkServiceOptionsGCP `json:"gcp,omitempty"`
}

// DBNetworkServiceOptionsGCP contains customization options of kubernetes
// Service created for database connection that are specific to GCP.
type DBNetworkServiceOptionsGCP struct {
	// LoadBalancerType let's define a type of load balancer, see
	// https://kubernetes.io/docs/concepts/services-networking/service/#internal-load-balancer
	// +kubebuilder:validation:Enum="";Internal;External
	// +optional
	LoadBalancerType string `json:"loadBalancerType,omitempty"`
}

// InstanceStatus defines the observed state of Instance.
type InstanceStatus struct {
	// InstanceStatus represents the database engine agnostic
	// part of the status describing the observed state of an Instance.
	commonv1alpha1.InstanceStatus `json:",inline"`

	// List of database names (e.g. PDBs) hosted in the Instance.
	DatabaseNames []string `json:"databasenames,omitempty"`

	// Last backup ID.
	BackupID string `json:"backupid,omitempty"`

	// +optional
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Format=date-time
	LastRestoreTime *metav1.Time `json:"lastRestoreTime,omitempty"`

	// CurrentServiceImage stores the image name used by the database instance.
	CurrentServiceImage string `json:"currentServiceImage,omitempty"`

	// CurrentParameters stores the last successfully set instance parameters.
	CurrentParameters map[string]string `json:"currentParameters,omitempty"`

	// LastFailedParameterUpdate is used to avoid getting into the failed
	// parameter update loop.
	LastFailedParameterUpdate map[string]string `json:"lastFailedParameterUpdate,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:categories=genericinstances,shortName=ginst
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:JSONPath=".spec.type",name="DB Engine",type="string"
// +kubebuilder:printcolumn:JSONPath=".spec.version",name="Version",type="string"
// +kubebuilder:printcolumn:JSONPath=".spec.edition",name="Edition",type="string"
// +kubebuilder:printcolumn:JSONPath=".status.endpoint",name="Endpoint",type="string"
// +kubebuilder:printcolumn:JSONPath=".status.url",name="URL",type="string"
// +kubebuilder:printcolumn:JSONPath=".status.databasenames",name="DB Names",type="string"
// +kubebuilder:printcolumn:JSONPath=".status.backupid",name="Backup ID",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].status`,name="ReadyStatus",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,name="ReadyReason",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].message`,name="ReadyMessage",type="string",priority=1
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="DatabaseInstanceReady")].status`,name="DBReadyStatus",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="DatabaseInstanceReady")].reason`,name="DBReadyReason",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="DatabaseInstanceReady")].message`,name="DBReadyMessage",type="string",priority=1
// +kubebuilder:printcolumn:JSONPath=".status.isChangeApplied",name="IsChangeApplied",type="string",priority=1

// Instance is the Schema for the instances API.
type Instance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceSpec   `json:"spec,omitempty"`
	Status InstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// InstanceList contains a list of Instance.
type InstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Instance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Instance{}, &InstanceList{})
}
