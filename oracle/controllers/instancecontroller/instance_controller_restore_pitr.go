// Copyright 2022 Google LLC
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

package instancecontroller

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
	"github.com/go-logr/logr"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *InstanceReconciler) findPITRBackupForRestore(ctx context.Context, inst v1alpha1.Instance, log logr.Logger) (*v1alpha1.Backup, error) {
	// Preflight check for PITR restore
	PITRRestoreSpec := inst.Spec.Restore.PITRRestore
	if PITRRestoreSpec.SCN == "" && PITRRestoreSpec.Timestamp == nil {
		return nil, fmt.Errorf("PITR preflight check: must specify either .PITRRestore.SCN or .PITRRestore.Time")
	}
	if PITRRestoreSpec.SCN != "" && PITRRestoreSpec.Timestamp != nil {
		return nil, fmt.Errorf("PITR preflight check: .PITRRestore.SCN and .PITRRestore.Time cannot be specified at the same time")
	}
	p, err := r.findRestorePITR(ctx, &inst)
	if err != nil {
		return nil, err
	}

	inc := PITRRestoreSpec.Incarnation
	if inc == "" {
		if PITRRestoreSpec.PITRRef != nil {
			// PITRRef was specified.
			inc = p.Status.CurrentDatabaseIncarnation
		} else {
			inc = inst.Status.CurrentDatabaseIncarnation
		}
	}

	// Find closest backup with timestamp/scn smaller than restore point in target incarnation
	var backupList v1alpha1.BackupList
	targetLabels := client.MatchingLabels{
		controllers.PITRLabel:        p.GetName(),
		controllers.IncarnationLabel: inc,
	}
	if err := r.List(ctx, &backupList, client.InNamespace(p.GetNamespace()), targetLabels); err != nil {
		return nil, fmt.Errorf("failed to get backups for a PITR restore: %v", err)
	}

	if len(backupList.Items) == 0 {
		return nil, fmt.Errorf("failed to find any backup for a PITR restore")
	}

	b, err := findPITRBackup(backupList.Items, PITRRestoreSpec, log)
	if err == nil {
		return b, nil
	}

	// Try to find closest backup in parent incarnation
	targetLabels = client.MatchingLabels{
		controllers.PITRLabel:        p.GetName(),
		controllers.IncarnationLabel: backupList.Items[0].Labels[controllers.ParentIncarnationLabel],
	}
	if err := r.List(ctx, &backupList, client.InNamespace(p.GetNamespace()), targetLabels); err != nil {
		return nil, fmt.Errorf("failed to get backups for a PITR restore: %v", err)
	}
	return findPITRBackup(backupList.Items, PITRRestoreSpec, log)
}

func findPITRBackup(backups []v1alpha1.Backup, pitrRestoreSpec *v1alpha1.PITRRestoreSpec, log logr.Logger) (*v1alpha1.Backup, error) {
	if len(backups) == 0 {
		return nil, fmt.Errorf("failed to find any backup for a PITR restore")
	}

	if pitrRestoreSpec.Timestamp != nil {
		return findPITRBackupBasedonTimestamp(backups, pitrRestoreSpec.Timestamp.Time, log)
	}
	targetSCN, err := strconv.ParseInt(pitrRestoreSpec.SCN, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse .pitrRestore.scn: %v", err)
	}
	return findPITRBackupBasedonSCN(backups, targetSCN, log)
}

func findPITRBackupBasedonTimestamp(backups []v1alpha1.Backup, targetTimestamp time.Time, log logr.Logger) (*v1alpha1.Backup, error) {
	var backup *v1alpha1.Backup
	targetCurrentTimestampDistance := time.Duration(math.MaxInt64)

	for _, b := range backups {
		backupReadyCond := k8s.FindCondition(b.Status.Conditions, k8s.Ready)
		if !k8s.ConditionStatusEquals(backupReadyCond, v1.ConditionTrue) || b.Annotations[controllers.TimestampAnnotation] == "" {
			continue
		}

		currentTimestamp, err := time.Parse(time.RFC3339, b.Annotations[controllers.TimestampAnnotation])
		if err != nil {
			log.Error(err, "findPITRBackupBasedonTimestamp: failed to parse backup timestamp")
			continue
		}

		newTargetCurrentTimestampDistance := targetTimestamp.Sub(currentTimestamp)
		if newTargetCurrentTimestampDistance >= 0 && newTargetCurrentTimestampDistance < targetCurrentTimestampDistance {
			targetCurrentTimestampDistance = newTargetCurrentTimestampDistance
			backup = b.DeepCopy()
		}
	}

	if backup == nil {
		return nil, fmt.Errorf("findPITRBackupBasedonTimestamp: failed to find valid backup for PITR restore based on timestamp")
	}
	return backup, nil
}

func findPITRBackupBasedonSCN(backups []v1alpha1.Backup, targetSCN int64, log logr.Logger) (*v1alpha1.Backup, error) {
	var backup *v1alpha1.Backup
	targetCurrentSCNDistance := int64(math.MaxInt64)

	for _, b := range backups {
		backupReadyCond := k8s.FindCondition(b.Status.Conditions, k8s.Ready)
		if !k8s.ConditionStatusEquals(backupReadyCond, v1.ConditionTrue) || b.Annotations[controllers.SCNAnnotation] == "" {
			continue
		}

		currentSCN, err := strconv.ParseInt(b.Annotations[controllers.SCNAnnotation], 10, 64)
		if err != nil {
			log.Error(err, "findPITRBackupBasedonSCN: failed to parse backup SCN")
			continue
		}

		newTargetCurrentSCNDistance := targetSCN - currentSCN
		if newTargetCurrentSCNDistance >= 0 && newTargetCurrentSCNDistance < targetCurrentSCNDistance {
			targetCurrentSCNDistance = newTargetCurrentSCNDistance
			backup = b.DeepCopy()
		}
	}
	if backup == nil {
		return nil, fmt.Errorf("findPITRBackupBasedonSCN: failed to find valid backup for PITR restore based on scn")
	}
	return backup, nil
}

func (r *InstanceReconciler) findRestorePITR(ctx context.Context, inst *v1alpha1.Instance) (v1alpha1.PITR, error) {
	var p v1alpha1.PITR
	if inst.Spec.Restore.PITRRestore == nil {
		return p, fmt.Errorf("failed to get a PITR object, pitrRestore must be specified in %v", inst.Spec.Restore)
	}

	if inst.Spec.Restore.PITRRestore.PITRRef != nil {
		if err := r.Get(
			ctx, types.NamespacedName{
				Namespace: inst.Spec.Restore.PITRRestore.PITRRef.Namespace,
				Name:      inst.Spec.Restore.PITRRestore.PITRRef.Name,
			},
			&p,
		); err != nil {
			return p, fmt.Errorf("failed to get a PITR object for a PITR restore: %v", err)
		}
		return p, nil
	}

	var PITRList v1alpha1.PITRList
	if err := r.List(ctx, &PITRList, client.InNamespace(inst.GetNamespace())); err != nil {
		return p, fmt.Errorf("failed to get a PITR object for a PITR restore: %v", err)
	}

	found := false
	for _, candidate := range PITRList.Items {
		if candidate.Spec.InstanceRef.Name == inst.GetName() {
			found = true
			p = candidate
			break
		}
	}
	if !found {
		return p, fmt.Errorf("PITR preflight check: instance doesn't have PITR enabled or specified")
	}
	// TODO: check PITRRestoreSpec.scn/timestamp against actual recovery window
	return PITRList.Items[0], nil
}
