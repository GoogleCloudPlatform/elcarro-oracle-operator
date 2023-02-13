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

package k8s

import (
	"strings"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
)

const (
	// Condition Types
	Ready                   = "Ready"
	DatabaseInstanceReady   = "DatabaseInstanceReady"
	DatabaseInstanceTimeout = "DatabaseInstanceTimeout"
	UserReady               = "UserReady"
	StandbyReady            = "StandbyReady"
	PauseMode               = "Pause"
	StandbyDRReady          = "StandbyDRReady"
	InstanceStopped         = "InstanceStopped"

	// Condition Reasons
	// Backup schedule concurrent policy is relying on the backup ready condition’s reason,
	// BackupReady and BackupFailed means backup job is not running and scheduler will continue creating backup.
	BackupPending                         = "BackupPending"
	BackupReady                           = "BackupReady"
	BackupInProgress                      = "BackupInProgress"
	BackupDeleting                        = "BackupDeleting"
	BackupFailed                          = "BackupFailed"
	CreateComplete                        = "CreateComplete"
	CreateFailed                          = "CreateFailed"
	CreateInProgress                      = "CreateInProgress"
	CreatePending                         = "CreatePending"
	BootstrapPending                      = "BootstrapPending"
	BootstrapInProgress                   = "BootstrapInProgress"
	ReconcileServices                     = "ReconcileServices"
	RestorePending                        = "RestorePending"
	ResizingInProgress                    = "ResizingInProgress"
	ImportComplete                        = "ImportComplete"
	ImportFailed                          = "ImportFailed"
	ImportInProgress                      = "ImportInProgress"
	ImportPending                         = "ImportPending"
	RestoreComplete                       = "RestoreComplete"
	RestoreFailed                         = "RestoreFailed"
	RestorePreparationInProgress          = "RestorePreparationInProgress"
	RestorePreparationComplete            = "RestorePreparationComplete"
	RestoreInProgress                     = "RestoreInProgress"
	PostRestoreBootstrapInProgress        = "PostRestoreBootstrapInProgress"
	PostRestoreBootstrapComplete          = "PostRestoreBootstrapComplete"
	PostRestoreDatabasePatchingInProgress = "PostRestoreDatabasePatchingInProgress"
	SyncInProgress                        = "SyncInProgress"
	UserOutOfSync                         = "UserOutOfSync"
	SyncComplete                          = "SyncComplete"
	ManuallySetUpStandbyInProgress        = "ManuallySetUpStandbyInProgress"
	PromoteStandbyInProgress              = "PromoteStandbyInProgress"
	PromoteStandbyComplete                = "PromoteStandbyComplete"
	PromoteStandbyFailed                  = "PromoteStandbyFailed"

	DBShutDown = "DBShutdown"

	InstanceStoppingInProgress = "InstanceStoppingInProgress"

	ExportComplete   = "ExportComplete"
	ExportFailed     = "ExportFailed"
	ExportInProgress = "ExportInProgress"
	ExportPending    = "ExportPending"

	ParameterUpdateInProgress         = "ParameterUpdateInProgress"
	ParameterUpdateComplete           = "ParameterUpdateComplete"
	ParameterUpdateRollbackInProgress = "ParameterUpdateRollbackInProgress"

	StandbyDRInProgress                     = "StandbyDRInProgress"
	StandbyDRVerifyCompleted                = "StandbyDRVerifyCompleted"
	StandbyDRVerifyFailed                   = "StandbyDRVerifyFailed"
	StandbyDRCreateInProgress               = "StandbyDRCreateInProgress"
	StandbyDRCreateFailed                   = "StandbyDRCreateFailed"
	StandbyDRCreateCompleted                = "StandbyDRCreateCompleted"
	StandbyDRSetUpDataGuardFailed           = "StandbyDRSetUpDataGuardFailed"
	StandbyDRSetUpDataGuardCompleted        = "StandbyDRSetUpDataGuardCompleted"
	StandbyDRDataGuardReplicationInProgress = "StandbyDRDataGuardReplicationInProgress"
	StandbyDRPromoteFailed                  = "StandbyDRPromoteFailed"
	StandbyDRPromoteCompleted               = "StandbyDRPromoteCompleted"
	StandbyDRBootstrapFailed                = "StandbyDRBootstrapFailed"
	StandbyDRBootstrapCompleted             = "StandbyDRBootstrapCompleted"

	PatchingBackupStarted      = "PatchingBackupStarted"
	PatchingBackupCompleted    = "PatchingBackupCompleted"
	PatchingBackupFailure      = "PatchingBackupFailure"
	PatchingRecoveryInProgress = "PatchingRecoveryInProgress"
	PatchingRecoveryCompleted  = "PatchingRecoveryCompleted"
	PatchingRecoveryFailure    = "PatchingRecoveryFailure"

	DeploymentSetPatchingInProgress         = "DeploymentSetPatchingInProgress"
	DeploymentSetPatchingRollbackInProgress = "DeploymentSetPatchingRollbackInProgress"
	DeploymentSetPatchingFailure            = "DeploymentSetPatchingFailure"
	DeploymentSetPatchingComplete           = "DeploymentSetPatchingComplete"
	StatefulSetPatchingInProgress           = "StatefulSetPatchingInProgress"
	StatefulSetPatchingComplete             = "StatefulSetPatchingComplete"
	StatefulSetPatchingFailure              = "StatefulSetPatchingFailure"
	DatabasePatchingInProgress              = "DatabasePatchingInProgress"
	DatabasePatchingComplete                = "DatabasePatchingComplete"
	DatabasePatchingFailure                 = "DatabasePatchingFailure"
	NotSupported                            = "NotSupported"
)

var (
	v1Now = func() v1.Time {
		return v1.Now().Rfc3339Copy()
	}
)

type StatusCond struct {
	instanceStatus v1alpha1.InstanceStatus
}

func FindCondition(conditions []v1.Condition, name string) *v1.Condition {
	for i, c := range conditions {
		if c.Type == name {
			return &conditions[i]
		}
	}
	return nil
}

func FindConditionOrFailed(conditions []v1.Condition, name string) (bool, *v1.Condition) {
	for i, c := range conditions {
		if c.Type == name {
			return strings.Contains(c.Reason, "Failed"), &conditions[i]
		}
	}
	return false, nil
}

func ConditionStatusEquals(cond *v1.Condition, status v1.ConditionStatus) bool {
	if cond == nil {
		return false
	}
	return cond.Status == status
}

func ConditionReasonEquals(cond *v1.Condition, reason string) bool {
	if cond == nil {
		return false
	}
	return cond.Reason == reason
}

func InstanceUpsertCondition(iStatus *v1alpha1.InstanceStatus, name string, status v1.ConditionStatus, reason, message string) *v1.Condition {
	iStatus.Conditions = Upsert(iStatus.Conditions, name, status, reason, message)
	return FindCondition(iStatus.Conditions, name)
}

func Upsert(conditions []v1.Condition, name string, status v1.ConditionStatus, reason, message string) []v1.Condition {

	if cond := FindCondition(conditions, name); cond != nil {
		if !ConditionStatusEquals(cond, status) || !ConditionReasonEquals(cond, reason) { // LastTransitionTime refers to the time Status changes
			cond.Status = status
			cond.Reason = reason
			cond.LastTransitionTime = v1Now()
		}
		cond.Message = message
		return conditions
	}

	cond := v1.Condition{Type: name, Status: status, Reason: reason, Message: message, LastTransitionTime: v1Now()}
	conditions = append(conditions, cond)
	return conditions
}

func ElapsedTimeFromLastTransitionTime(condition *v1.Condition, roundTo time.Duration) time.Duration {
	if condition == nil {
		return 0
	}
	return v1Now().Sub(condition.LastTransitionTime.Time).Round(roundTo)
}
