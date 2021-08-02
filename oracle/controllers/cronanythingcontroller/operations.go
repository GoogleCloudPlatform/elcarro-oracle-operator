package cronanythingcontroller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

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
