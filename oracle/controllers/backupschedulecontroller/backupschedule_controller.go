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
	"fmt"
	"reflect"
	"sort"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

const (
	defaultTriggerDeadlineSeconds int64 = 30
	defaultRetention              int32 = 7
	defaultMaxHistoryRecords      int32 = 7
)

var (
	defaultTimeFormat = "20060102-150405"
	backupKind        = schema.GroupVersion{Group: "oracle.db.anthosapis.com", Version: "v1alpha1"}.WithKind("Backup")
)

type backupScheduleControl interface {
	Get(name, namespace string) (*v1alpha1.BackupSchedule, error)
	UpdateStatus(backupSchedule *v1alpha1.BackupSchedule) error
}

type cronAnythingControl interface {
	Create(cron *v1alpha1.CronAnything) error
	Get(name, namespace string) (*v1alpha1.CronAnything, error)
	Update(cron *v1alpha1.CronAnything) error
}

type backupControl interface {
	List(cronAnythingName string) ([]*v1alpha1.Backup, error)
	Delete(backup *v1alpha1.Backup) error
}

var _ reconcile.Reconciler = &BackupScheduleReconciler{}

// BackupScheduleReconciler reconciles a BackupSchedule object
type BackupScheduleReconciler struct {
	client.Client
	Log                logr.Logger
	scheme             *runtime.Scheme
	backupScheduleCtrl backupScheduleControl
	cronAnythingCtrl   cronAnythingControl
	backupCtrl         backupControl
}

// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=backupschedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=backupschedules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=cronanythings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=backups,verbs=list;delete

// Reconcile is a generic reconcile function for BackupSchedule resources.
func (r *BackupScheduleReconciler) Reconcile(_ context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("backupschedule", req.NamespacedName)
	backupSchedule, err := r.backupScheduleCtrl.Get(req.Name, req.Namespace)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	cron, err := r.lookupCron(backupSchedule)
	if err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	if errors.IsNotFound(err) {
		log.Info("No cron found for backup schedule. Creating new one", "backupSchedule", backupSchedule.Namespace+"/"+backupSchedule.Name)
		err := r.createCron(backupSchedule)
		return reconcile.Result{}, err
	}

	err = r.updateCron(backupSchedule, cron)
	if err != nil {
		return reconcile.Result{}, err
	}

	var backups []*v1alpha1.Backup

	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		backups, err = r.getSortedBackupsForCron(cron)
		if err != nil {
			return err
		}
		backupSchedule, err := r.backupScheduleCtrl.Get(req.Name, req.Namespace)
		if err != nil {
			return err
		}
		return r.updateHistory(backupSchedule, backups)
	})

	if err != nil {
		return reconcile.Result{}, err
	}

	return ctrl.Result{}, r.pruneBackups(backupSchedule.Spec.BackupRetentionPolicy, backups)
}

func (r *BackupScheduleReconciler) lookupCron(backupSchedule *v1alpha1.BackupSchedule) (*v1alpha1.CronAnything, error) {
	cron, err := r.cronAnythingCtrl.Get(r.getCronName(backupSchedule), backupSchedule.Namespace)
	if err != nil {
		return nil, err
	}
	return cron, nil
}

func (r *BackupScheduleReconciler) createCron(backupSchedule *v1alpha1.BackupSchedule) error {
	name := r.getCronName(backupSchedule)
	triggerDeadlineSeconds := defaultTriggerDeadlineSeconds
	if backupSchedule.Spec.StartingDeadlineSeconds != nil {
		triggerDeadlineSeconds = *backupSchedule.Spec.StartingDeadlineSeconds
	}

	backupBytes, err := getBackupBytes(backupSchedule)
	if err != nil {
		return err
	}

	cron := &v1alpha1.CronAnything{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: backupSchedule.Namespace,
		},
		Spec: v1alpha1.CronAnythingSpec{
			CronAnythingSpec: commonv1alpha1.CronAnythingSpec{
				Schedule:               backupSchedule.Spec.Schedule,
				TriggerDeadlineSeconds: &triggerDeadlineSeconds,
				ConcurrencyPolicy:      commonv1alpha1.ForbidConcurrent,
				FinishableStrategy: &commonv1alpha1.FinishableStrategy{
					Type: commonv1alpha1.FinishableStrategyStringField,
					StringField: &commonv1alpha1.StringFieldStrategy{
						FieldPath: fmt.Sprintf("{.status.conditions[?(@.type==\"%s\")].reason}", k8s.Ready),
						FinishedValues: []string{
							k8s.BackupReady,
							k8s.BackupFailed,
						},
					},
				},
				ResourceBaseName:        &name,
				ResourceTimestampFormat: &defaultTimeFormat,
				Template: runtime.RawExtension{
					Raw: backupBytes,
				},
			},
		},
	}

	err = controllerutil.SetControllerReference(backupSchedule, cron, r.scheme)
	if err != nil {
		return err
	}

	err = r.cronAnythingCtrl.Create(cron)
	if err != nil {
		return err
	}
	return nil
}

func (r *BackupScheduleReconciler) updateCron(backupSchedule *v1alpha1.BackupSchedule, cron *v1alpha1.CronAnything) error {
	backupBytes, err := getBackupBytes(backupSchedule)
	if err != nil {
		return err
	}

	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		freshCron, err := r.cronAnythingCtrl.Get(cron.Name, cron.Namespace)
		if err != nil {
			return err
		}

		templatesEqual, err := r.compareTemplate(freshCron.Spec.Template.Raw, backupBytes)
		if err != nil {
			return err
		}

		scheduleEqual := backupSchedule.Spec.Schedule == freshCron.Spec.Schedule
		startingDeadlineSecondsEqual := compareInt64Pointers(backupSchedule.Spec.StartingDeadlineSeconds, freshCron.Spec.TriggerDeadlineSeconds)

		r.Log.Info("backup schedule diff", "templateUnchanged", templatesEqual, "scheduleUnchanged", scheduleEqual, "StartingDeadlineSecondsUnchanged", startingDeadlineSecondsEqual)

		if templatesEqual && scheduleEqual && startingDeadlineSecondsEqual {
			return nil
		}
		freshCron.Spec.Schedule = backupSchedule.Spec.Schedule
		freshCron.Spec.Template.Raw = backupBytes
		freshCron.Spec.TriggerDeadlineSeconds = backupSchedule.Spec.StartingDeadlineSeconds
		return r.cronAnythingCtrl.Update(freshCron)
	})
}

func (r *BackupScheduleReconciler) updateHistory(backupSchedule *v1alpha1.BackupSchedule, sortedBackups []*v1alpha1.Backup) error {
	newBackupHistory := []v1alpha1.BackupHistoryRecord{}
	for _, backup := range sortedBackups {
		newBackupHistory = append(newBackupHistory, v1alpha1.BackupHistoryRecord{
			BackupName:   backup.GetName(),
			CreationTime: backup.GetCreationTimestamp(),
			Phase:        backup.Status.Phase,
		})
	}
	backupTotal := int32(len(newBackupHistory))
	if backupTotal > defaultMaxHistoryRecords {
		newBackupHistory = newBackupHistory[:defaultMaxHistoryRecords]
	}
	backupSchedule.Status.BackupTotal = &backupTotal
	backupSchedule.Status.BackupHistory = newBackupHistory
	return r.backupScheduleCtrl.UpdateStatus(backupSchedule)
}

func (r *BackupScheduleReconciler) pruneBackups(retention *v1alpha1.BackupRetentionPolicy, sortedBackups []*v1alpha1.Backup) error {
	max := defaultRetention
	if retention != nil && retention.BackupRetention != nil {
		max = *retention.BackupRetention
	}
	if max == 0 {
		return nil
	}

	count := max
	for _, backup := range sortedBackups {
		if count <= 0 {
			r.Log.Info("deleting backup", "backup", backup)
			if err := r.backupCtrl.Delete(backup); err != nil {
				return err
			}
		}
		if backup.Status.Phase == commonv1alpha1.BackupSucceeded && count > 0 {
			count -= 1
		}
	}
	return nil
}

func (r *BackupScheduleReconciler) compareTemplate(left, right []byte) (bool, error) {
	var leftMap map[string]interface{}
	err := json.Unmarshal(left, &leftMap)
	if err != nil {
		return false, err
	}

	var rightMap map[string]interface{}
	err = json.Unmarshal(right, &rightMap)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(leftMap, rightMap), nil
}

func compareInt64Pointers(i1, i2 *int64) bool {
	if i1 == nil && i2 == nil {
		return true
	}
	if i1 == nil || i2 == nil {
		return false
	}
	return *i1 == *i2
}
func (r *BackupScheduleReconciler) getCronName(backupSchedule *v1alpha1.BackupSchedule) string {
	return fmt.Sprintf("%s-cron", backupSchedule.Name)
}

func (r *BackupScheduleReconciler) getSortedBackupsForCron(cron *v1alpha1.CronAnything) ([]*v1alpha1.Backup, error) {
	backupList, err := r.backupCtrl.List(cron.Name)
	if err != nil {
		return nil, err
	}

	sort.Slice(backupList, func(i, j int) bool {
		iTime := backupList[i].GetCreationTimestamp()
		jTime := backupList[j].GetCreationTimestamp()
		return jTime.Before(&iTime)
	})
	return backupList, nil
}

func getBackupBytes(backupSchedule *v1alpha1.BackupSchedule) ([]byte, error) {
	specBytes, err := json.Marshal(backupSchedule.Spec.BackupSpec)
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

// NewBackupScheduleReconciler returns a BackupScheduleReconciler object.
func NewBackupScheduleReconciler(mgr manager.Manager) *BackupScheduleReconciler {
	return &BackupScheduleReconciler{
		Client:             mgr.GetClient(),
		Log:                ctrl.Log.WithName("controllers").WithName("BackupSchedule"),
		scheme:             mgr.GetScheme(),
		backupScheduleCtrl: &realBackupScheduleControl{client: mgr.GetClient()},
		cronAnythingCtrl:   &realCronAnythingControl{client: mgr.GetClient()},
		backupCtrl:         &realBackupControl{client: mgr.GetClient()},
	}
}

// SetupWithManager configures the reconciler.
func (r *BackupScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.BackupSchedule{}).
		Watches(&source.Kind{Type: &v1alpha1.CronAnything{}},
			&handler.EnqueueRequestForOwner{OwnerType: &v1alpha1.BackupSchedule{}, IsController: true}).
		Complete(r)
}
