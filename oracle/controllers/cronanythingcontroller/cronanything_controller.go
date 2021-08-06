package cronanythingcontroller

import (
	ctrl "sigs.k8s.io/controller-runtime"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/controllers"
	oraclev1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/go-logr/logr"
)

// CronAnythingReconciler reconciles a CronAnything object
type CronAnythingReconciler struct {
	*commonv1alpha1.ReconcileCronAnything
}

//+kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=cronanythings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=cronanythings/status,verbs=get;update;patch

func NewCronAnythingReconciler(mgr ctrl.Manager, log logr.Logger, realCronAnythingControl *RealCronAnythingControl) (*CronAnythingReconciler, error) {
	r, err := commonv1alpha1.NewCronAnythingReconciler(mgr, log, realCronAnythingControl)
	if err != nil {
		return nil, err
	}
	return &CronAnythingReconciler{
		r,
	}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CronAnythingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&oraclev1alpha1.CronAnything{}).
		Complete(r)
}
