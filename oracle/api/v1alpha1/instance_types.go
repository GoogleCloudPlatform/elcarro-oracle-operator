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
	// CDB will be renamed.  The CDBName should meet Oracle SID requirements:
	// uppercase, alphanumeric, max 8 characters, and not start with a number.
	// +optional
	// +kubebuilder:validation:MaxLength=8
	// +kubebuilder:validation:Pattern=^[A-Z][A-Z0-9]*$
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

	// ReplicationSettings provides configuration for initializing an
	// instance as a standby for the specified primary instance. These
	// settings can only be used when initializing an instance, adding them
	// to an already created instance is an error. Once a standby is
	// created with these settings you may promote the standby to its own
	// independent instance by removing these settings.
	// DBUniqueName must be set when initializing a standby instance.
	// +optional
	ReplicationSettings *ReplicationSettings `json:"replicationSettings,omitempty"`

	// EnableDnfs enables configuration of Oracle's dNFS functionality.
	// +optional
	EnableDnfs bool `json:"enableDnfs,omitempty"`
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

	// Point In Time Recovery restore spec.
	// +optional
	PITRRestore *PITRRestoreSpec `json:"pitrRestore,omitempty"`

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

type PITRRestoreSpec struct {
	// Incarnation number to restore to. This is optional, default to current incarnation.
	// +optional
	Incarnation string `json:"incarnation,omitempty"`

	// Set ONLY ONE of the following as restore point.

	// Timestamp to restore to.
	// +optional
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Format=date-time
	Timestamp *metav1.Time `json:"timestamp,omitempty"`

	// SCN to restore to.
	// +optional
	SCN string `json:"scn,omitempty"`

	// PITRRef specifies the PITR object from which to read backup data.
	// +optional
	PITRRef *PITRReference `json:"pitrRef,omitempty"`
}

type PITRReference struct {
	// `namespace` is the namespace in which the PITR object is created.
	// +required
	Namespace string `json:"namespace,omitempty"`
	// `name` is the name of the PITR.
	// +required
	Name string `json:"name,omitempty"`
}

// ReplicationSettings provides configuration for initializing an
// instance as a standby for the specified primary instance. These
// settings can only be used when initializing an instance, adding them
// to an already created instance is an error. Once a standby is
// created with these settings you may promote the standby to its own
// independent instance by removing these settings.
type ReplicationSettings struct {
	// PrimaryHost is the hostname of the primary's listener.
	// +required
	PrimaryHost string `json:"primaryHost"`
	// PrimaryPort is the port of the primary's listener.
	// +required
	PrimaryPort int32 `json:"primaryPort"`
	// PrimaryServiceName is the service name of the primary
	// database on the listener at PrimaryHost:PrimaryPort.
	// +required
	PrimaryServiceName string `json:"primaryServiceName"`
	// PrimaryUser specifies the user name and credential to authenticate to the primary database as.
	// +required
	PrimaryUser commonv1alpha1.UserSpec `json:"primaryUser"`
	// PasswordFileURI is the URI to a copy of the primary's
	// password file for establishing an active dataguard connection.
	// Currently only gs:// (GCS) schemes are supported.
	// +required
	PasswordFileURI string `json:"passwordFileURI"`
	// BackupURI is the URI to a copy of the primary's RMAN backup.
	// Standby will be created from this backup when provided.
	// Currently only gs:// (GCS) schemes are supported.
	// +optional
	BackupURI string `json:"backupURI"`
}

// DataGuardOutput shows Data Guard utility output.
type DataGuardOutput struct {
	// LastUpdateTime is the last time the DataGuardOutput updated based on DB
	// Data Guard utility output.
	// +required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Format=date-time
	LastUpdateTime metav1.Time `json:"lastUpdateTime"`

	// StatusOutput is the output of "show configuration" and "show database <standby DB unique name>".
	StatusOutput []string `json:"statusOutput"`
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

	// LastDatabaseIncarnation stores the parent incarnation number
	LastDatabaseIncarnation string `json:"lastDatabaseIncarnation,omitempty"`

	// CurrentDatabaseIncarnation stores the current incarnation number
	CurrentDatabaseIncarnation string `json:"currentDatabaseIncarnation,omitempty"`

	// CurrentReplicationSettings stores the current replication settings of the
	// standby instance. Standby data replication uses it to promote a standby
	// instance. It will be updated to match with spec.replicationSettings before
	// promotion. It will be removed once data replication is completed.
	// +optional
	CurrentReplicationSettings *ReplicationSettings `json:"currentReplicationSettings,omitempty"`

	// DataGuardOutput stores the latest Data Guard utility status output.
	// +optional
	DataGuardOutput *DataGuardOutput `json:"dataGuardOutput,omitempty"`

	// LastFailedParameterUpdate is used to avoid getting into the failed
	// parameter update loop.
	LastFailedParameterUpdate map[string]string `json:"lastFailedParameterUpdate,omitempty"`

	// LockedByController is a shared lock field granting exclusive access
	// to maintenance operations to only one controller.
	// Empty value means unlocked.
	// Non-empty value contains the name of the owning controller.
	// +optional
	LockedByController string `json:"lockedBy,omitempty"`

	// DnfsEnabled stores whether dNFS has already been enabled or not.
	DnfsEnabled bool `json:"DnfsEnabled,omitempty"`
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
