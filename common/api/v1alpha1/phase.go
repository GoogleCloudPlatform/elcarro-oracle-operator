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

type InstancePhase string

const (
	InstanceCreating  InstancePhase = "Creating"
	InstanceUpdating  InstancePhase = "Updating"
	InstanceRestoring InstancePhase = "Restoring"
	InstanceDeleting  InstancePhase = "Deleting"
	InstanceReady     InstancePhase = "Ready"
)

type DatabasePhase string

const (
	DatabasePending  DatabasePhase = "Pending"
	DatabaseCreating DatabasePhase = "Creating"
	DatabaseUpdating DatabasePhase = "Updating"
	DatabaseDeleting DatabasePhase = "Deleting"
	DatabaseReady    DatabasePhase = "Ready"
)

type UserPhase string

const (
	UserPending  UserPhase = "Pending"
	UserCreating UserPhase = "Creating"
	UserUpdating UserPhase = "Updating"
	UserDeleting UserPhase = "Deleting"
	UserReady    UserPhase = "Ready"
)

type BackupPhase string

const (
	BackupPending    BackupPhase = "Pending"
	BackupInProgress BackupPhase = "InProgress"
	BackupFailed     BackupPhase = "Failed"
	BackupSucceeded  BackupPhase = "Succeeded"
)

type RestorePhase string

const (
	RestorePending    RestorePhase = "Pending"
	RestoreInProgress RestorePhase = "InProgress"
	RestoreFailed     RestorePhase = "Failed"
	RestoreSucceeded  RestorePhase = "Succeeded"
)
