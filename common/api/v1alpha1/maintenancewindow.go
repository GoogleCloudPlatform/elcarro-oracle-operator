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

// TimeRange defines a window of time.
// Both start time and duration are required.
// +kubebuilder:object:generate=true
type TimeRange struct {
	// Start time.
	// +required
	Start *metav1.Time `json:"start,omitempty"`

	// Duration of the maintenance window
	// +required
	Duration *metav1.Duration `json:"duration,omitempty"`
}

// MaintenanceWindowSpec defines the time ranges during which maintenance may be started on a database.
// +kubebuilder:object:generate=true
type MaintenanceWindowSpec struct {
	// Maintenance time ranges.
	TimeRanges []TimeRange `json:"timeRanges,omitempty"`
}
