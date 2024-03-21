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
	corev1 "k8s.io/api/core/v1"
)

//+kubebuilder:object:generate=true

// CredentialSpec defines the desired state of user credentials.
// The credential can be expressed in one of the 3 following ways:
//  1. A plaintext password;
//  2. A reference to a k8s secret;
//  3. A reference to a remote GSM secret (note that it only works for GKE).
type CredentialSpec struct {
	// Plaintext password.
	// +optional
	Password string `json:"password,omitempty"`

	// A reference to a k8s secret.
	// +optional
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`

	// A reference to a GSM secret.
	// +optional
	GsmSecretRef *GsmSecretReference `json:"gsmSecretRef,omitempty"`
}

//+kubebuilder:object:generate=true

// GsmSecretReference represents a Google Secret Manager Secret (GSM) Reference.
// It has enough information to retrieve a secret from Google Secret manager.
type GsmSecretReference struct {
	// ProjectId identifies the project where the secret resource is.
	// +required
	ProjectId string `json:"projectId,omitempty"`

	// SecretId identifies the secret.
	// +required
	SecretId string `json:"secretId,omitempty"`

	// Version is the version of the secret.
	// If "latest" is specified, underlying the latest SecretId is used.
	// +required
	Version string `json:"version,omitempty"`
}
