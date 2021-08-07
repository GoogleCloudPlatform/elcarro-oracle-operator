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
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"

	commonctl "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/controllers"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/cronanythingcontroller"
)

var backupKind = schema.GroupVersion{Group: "oracle.db.anthosapis.com", Version: "v1alpha1"}.WithKind("Backup")

type BackupScheduleReconciler struct {
	*commonctl.BackupScheduleReconciler
}

// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=backupschedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=backupschedules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=cronanythings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=backups,verbs=list;delete

// NewBackupScheduleReconciler returns a BackupScheduleReconciler object.
func NewBackupScheduleReconciler(mgr manager.Manager, realBackupScheduleControl *RealBackupScheduleControl, realCronAnythingControl *cronanythingcontroller.RealCronAnythingControl, realBackupControl *RealBackupControl) *BackupScheduleReconciler {
	b := commonctl.NewBackupScheduleReconciler(mgr, realBackupScheduleControl, realCronAnythingControl, realBackupControl)

	return &BackupScheduleReconciler{
		BackupScheduleReconciler: b,
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
