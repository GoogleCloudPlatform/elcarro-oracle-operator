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

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
)

type realBackupScheduleControl struct {
	client client.Client
}

func (r *realBackupScheduleControl) Get(name, namespace string) (*v1alpha1.BackupSchedule, error) {
	key := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}
	var backupSchedule v1alpha1.BackupSchedule
	err := r.client.Get(context.TODO(), key, &backupSchedule)
	return &backupSchedule, err
}

func (r *realBackupScheduleControl) UpdateStatus(schedule *v1alpha1.BackupSchedule) error {
	return r.client.Status().Update(context.TODO(), schedule)
}

type realCronAnythingControl struct {
	client client.Client
}

func (r *realCronAnythingControl) Create(cron *v1alpha1.CronAnything) error {
	return r.client.Create(context.TODO(), cron)
}

func (r *realCronAnythingControl) Get(name, namespace string) (*v1alpha1.CronAnything, error) {
	ca := &v1alpha1.CronAnything{}
	err := r.client.Get(context.TODO(), client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, ca)
	return ca, err
}

func (r *realCronAnythingControl) Update(ca *v1alpha1.CronAnything) error {
	return r.client.Update(context.TODO(), ca)
}

type realBackupControl struct {
	client client.Client
}

func (r *realBackupControl) List(cronAnythingName string) ([]*v1alpha1.Backup, error) {
	listOptions := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{commonv1alpha1.CronAnythingCreatedByLabel: cronAnythingName}),
	}
	var backupList v1alpha1.BackupList
	err := r.client.List(context.TODO(), &backupList, listOptions)
	if err != nil {
		return nil, err
	}
	var backups []*v1alpha1.Backup
	for _, b := range backupList.Items {
		if b.DeletionTimestamp != nil {
			continue
		}
		backups = append(backups, b.DeepCopy())
	}
	return backups, nil
}

func (r *realBackupControl) Delete(backup *v1alpha1.Backup) error {
	return r.client.Delete(context.TODO(), backup)
}
