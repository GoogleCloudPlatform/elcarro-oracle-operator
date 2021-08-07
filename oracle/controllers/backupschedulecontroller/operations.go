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

package backupschedulecontroller

import (
	"context"
	"encoding/json"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
)

type RealBackupScheduleControl struct {
	Client client.Client
}

func (r *RealBackupScheduleControl) Get(name, namespace string) (commonv1alpha1.BackupSchedule, error) {
	key := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}
	var backupSchedule v1alpha1.BackupSchedule
	err := r.Client.Get(context.TODO(), key, &backupSchedule)
	return &backupSchedule, err
}

func (r *RealBackupScheduleControl) UpdateStatus(schedule commonv1alpha1.BackupSchedule) error {
	return r.Client.Status().Update(context.TODO(), schedule.(*v1alpha1.BackupSchedule))
}

func (r *RealBackupScheduleControl) GetBackupBytes(schedule commonv1alpha1.BackupSchedule) ([]byte, error) {
	specBytes, err := json.Marshal(schedule.(*v1alpha1.BackupSchedule).Spec.BackupSpec)
	if err != nil {
		return nil, err
	}

	var specMap map[string]interface{}
	err = json.Unmarshal(specBytes, &specMap)
	if err != nil {
		return nil, err
	}

	backupMap := make(map[string]interface{})
	backupMap["apiVersion"] = backupKind.GroupVersion().String()
	backupMap["kind"] = backupKind.Kind
	backupMap["spec"] = specMap
	return json.Marshal(backupMap)
}

type RealBackupControl struct {
	Client client.Client
}

func (r *RealBackupControl) List(cronAnythingName string) ([]commonv1alpha1.Backup, error) {
	listOptions := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{commonv1alpha1.CronAnythingCreatedByLabel: cronAnythingName}),
	}
	var backupList v1alpha1.BackupList
	err := r.Client.List(context.TODO(), &backupList, listOptions)
	if err != nil {
		return nil, err
	}
	var backups []commonv1alpha1.Backup
	for _, b := range backupList.Items {
		if b.DeletionTimestamp != nil {
			continue
		}
		backups = append(backups, b.DeepCopy())
	}
	return backups, nil
}

func (r *RealBackupControl) Delete(backup commonv1alpha1.Backup) error {
	return r.Client.Delete(context.TODO(), backup.(*v1alpha1.Backup))
}
