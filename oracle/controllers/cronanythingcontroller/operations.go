package cronanythingcontroller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
)

type RealCronAnythingControl struct {
	client.Client
}

func (r *RealCronAnythingControl) Get(key client.ObjectKey) (commonv1alpha1.CronAnything, error) {
	ca := &v1alpha1.CronAnything{}
	err := r.Client.Get(context.TODO(), key, ca)
	return ca, err
}

func (r *RealCronAnythingControl) Update(ca commonv1alpha1.CronAnything) error {
	if err := r.Client.Status().Update(context.TODO(), ca.(*v1alpha1.CronAnything)); err != nil {
		return err
	}
	return r.Client.Update(context.TODO(), ca.(*v1alpha1.CronAnything))
}

func (r *RealCronAnythingControl) Create(ns string, name string, cas commonv1alpha1.CronAnythingSpec, owner commonv1alpha1.BackupSchedule) error {
	ca := &v1alpha1.CronAnything{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
		Spec: v1alpha1.CronAnythingSpec{
			CronAnythingSpec: cas,
		},
	}
	err := controllerutil.SetControllerReference(owner, ca, r.Scheme())
	if err != nil {
		return err
	}
	return r.Client.Create(context.TODO(), ca)
}
