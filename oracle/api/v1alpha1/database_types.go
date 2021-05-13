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

// DatabaseSpec defines the desired state of Database.
type DatabaseSpec struct {
	// Database specs that are common across all database engines.
	commonv1alpha1.DatabaseSpec `json:",inline"`

	// AdminPassword is the password for the sys admin of the database.
	// +optional
	// +kubebuilder:validation:MaxLength=30
	// +kubebuilder:validation:MinLength=5
	AdminPassword string `json:"admin_password,omitempty"`

	// AdminPasswordGsmSecretRef is a reference to the secret object containing
	// sensitive information to pass to config agent.
	// This field is optional, and may be empty if plaintext password is used.
	// +optional
	AdminPasswordGsmSecretRef *commonv1alpha1.GsmSecretReference `json:"adminPasswordGsmSecretRef,omitempty"`

	// Users specifies an optional list of users to be created in this database.
	// +optional
	Users []UserSpec `json:"users"`
}

// UserSpec defines the desired state of the Database Users.
type UserSpec struct {
	// User specs that are common across all database engines.
	commonv1alpha1.UserSpec `json:",inline"`

	// Privileges specifies an optional list of privileges to grant to the user.
	// +optional
	Privileges []PrivilegeSpec `json:"privileges"`
}

// PrivilegeSpec defines the desired state of roles and privileges.
type PrivilegeSpec string

// DatabaseStatus defines the observed state of Database.
type DatabaseStatus struct {
	// Database status that is common across all database engines.
	commonv1alpha1.DatabaseStatus `json:",inline"`

	// List of user names.
	UserNames []string `json:"usernames,omitempty"`

	// UserResourceVersions is a map of username to user resource version
	// (plaintext or GSM). For GSM Resource version, use format:
	// "projects/{ProjectId}/secrets/{SecretId}/versions/{Version}".
	UserResourceVersions map[string]string `json:"UserResourceVersions,omitempty"`

	// ObservedGeneration is the latest generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// IsChangeApplied indicates whether database changes have been applied
	// +optional
	IsChangeApplied metav1.ConditionStatus `json:"isChangeApplied,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:categories=genericdatabases,shortName=gdb
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:JSONPath=".spec.instance",name="Instance",type="string"
// +kubebuilder:printcolumn:JSONPath=".status.usernames",name="Users",type="string"
// +kubebuilder:printcolumn:JSONPath=".status.phase",name="Phase",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].status`,name="DatabaseReadyStatus",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,name="DatabaseReadyReason",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="Ready")].message`,name="DatabaseReadyMessage",type="string",priority=1
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="UserReady")].status`,name="UserReadyStatus",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="UserReady")].reason`,name="UserReadyReason",type="string"
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=="UserReady")].message`,name="UserReadyMessage",type="string",priority=1

// Database is the Schema for the databases API.
type Database struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DatabaseSpec   `json:"spec,omitempty"`
	Status DatabaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DatabaseList contains a list of Database.
type DatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Database `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Database{}, &DatabaseList{})
}
