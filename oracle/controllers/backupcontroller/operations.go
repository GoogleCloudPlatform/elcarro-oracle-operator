package backupcontroller

import (
	"context"
	"fmt"
	"strings"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type RealBackupControl struct {
	Client client.Client
}

func (c *RealBackupControl) GetBackup(name, namespace string) (*v1alpha1.Backup, error) {
	key := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}
	backup := &v1alpha1.Backup{}
	err := c.Client.Get(context.TODO(), key, backup)
	return backup, err
}

func (c *RealBackupControl) GetInstance(name, namespace string) (*v1alpha1.Instance, error) {
	key := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}
	inst := &v1alpha1.Instance{}
	err := c.Client.Get(context.TODO(), key, inst)
	return inst, err
}

func (c *RealBackupControl) LoadConfig(namespace string) (*v1alpha1.Config, error) {
	var configs v1alpha1.ConfigList
	if err := c.Client.List(context.TODO(), &configs, client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	if len(configs.Items) == 0 {
		return nil, nil
	}

	if len(configs.Items) > 1 {
		return nil, fmt.Errorf("this release only supports a single customer provided config (received %d)", len(configs.Items))
	}

	return &configs.Items[0], nil
}

func (c *RealBackupControl) UpdateStatus(obj client.Object) error {
	return c.Client.Status().Update(context.TODO(), obj)
}

func (c *RealBackupControl) UpdateBackup(obj client.Object) error {
	return c.Client.Update(context.TODO(), obj)
}

func (c *RealBackupControl) ValidateBackupSpec(backup *v1alpha1.Backup) bool {
	var errMsgs []string
	if backup.Spec.Type != commonv1alpha1.BackupTypeSnapshot && backup.Spec.Type != commonv1alpha1.BackupTypePhysical {
		errMsgs = append(errMsgs, fmt.Sprintf("backup does not support type %q", backup.Spec.Type))
	}
	if backup.Spec.Type == commonv1alpha1.BackupTypeSnapshot && backup.Spec.Subtype != "" && backup.Spec.Subtype != "Instance" {
		errMsgs = append(errMsgs, fmt.Sprintf("%s backup only support .spec.subtype 'Instance'", backup.Spec.Type))
	}
	if backup.Spec.Instance == "" {
		errMsgs = append(errMsgs, fmt.Sprintf("spec.Instance is not set in the backup request: %v", backup))
	}
	if len(errMsgs) > 0 {
		reason := ""
		brc := k8s.FindCondition(backup.Status.Conditions, k8s.Ready)
		if brc != nil {
			// do not change condition reason
			reason = brc.Reason
		}
		backup.Status.Conditions = k8s.Upsert(backup.Status.Conditions, k8s.Ready, v1.ConditionUnknown, reason, strings.Join(errMsgs, msgSep))
		return false
	}
	return true
}
