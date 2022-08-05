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

package pitrcontroller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/util"
	"github.com/go-logr/logr"
	"github.com/robfig/cron"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	pb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/pitr/proto"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

const (
	deploymentTemplate = "%s-pitr-agent-deployment"
	// PITRSvcTemplate is a string template for agent service names.
	PITRSvcTemplate = "%s-pitr-agent-svc"
	agentName       = "pitr-agent"
	pitrCmd         = "/pitr_agent"
	agentImageKey   = "agent"

	// DefaultPITRAgentPort is PITR Agent's default port number.
	DefaultPITRAgentPort = 3204
)

var (
	requeueInterval = 10 * time.Second
)

type backupControl interface {
	List(ctx context.Context, opts ...client.ListOption) ([]v1alpha1.Backup, error)
}

type pitrControl interface {
	AvailableRecoveryWindows(ctx context.Context, p *v1alpha1.PITR) ([]*pb.Range, error)
	UpdateStatus(ctx context.Context, p *v1alpha1.PITR) error
}

// PITRReconciler reconciles a PITR object
type PITRReconciler struct {
	client.Client
	Log        logr.Logger
	Scheme     *runtime.Scheme
	BackupCtrl backupControl
	PITRCtrl   pitrControl
}

// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=pitrs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=pitrs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=instances,verbs=get;watch;list;
// +kubebuilder:rbac:groups=core,resources=services,verbs=list;watch;get;patch;create
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

func (r *PITRReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("pitr", req.NamespacedName)
	log.Info("reconciling PITR requests")

	var p v1alpha1.PITR
	if err := r.Get(ctx, req.NamespacedName, &p); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	validationErrs := validatePITRSpec(p.Spec)
	if len(validationErrs) != 0 {
		log.Error(errors.New("PITR spec is invalid"), strings.Join(validationErrs, "\n"))
		p.Status.Conditions = k8s.Upsert(p.Status.Conditions, k8s.Ready, metav1.ConditionFalse, k8s.CreatePending, strings.Join(validationErrs, "\n"))
		return ctrl.Result{}, r.PITRCtrl.UpdateStatus(ctx, &p)
	}

	var i v1alpha1.Instance
	if err := r.Get(ctx, types.NamespacedName{
		Name:      p.Spec.InstanceRef.Name,
		Namespace: p.GetNamespace(),
	}, &i); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ensureAgent(ctx, &p, &i); err != nil {
		return ctrl.Result{}, err
	}

	res, err := r.ensureBackup(ctx, &p, &i)
	if err != nil || !res.IsZero() {
		return res, err
	}

	if err = r.ensureBackupSchedule(ctx, &p, &i); err != nil {
		return ctrl.Result{}, err
	}

	if err = r.updateStatus(ctx, &p, &i, log); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("reconciling PITR: DONE")

	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

func (r *PITRReconciler) ensureBackupSchedule(ctx context.Context, p *v1alpha1.PITR, i *v1alpha1.Instance) error {
	applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("pitr-controller")}
	if err := r.Patch(ctx, backupScheduleTemplate(p, i), client.Apply, applyOpts...); err != nil {
		return err
	}

	return nil
}

// Ensure at least one successful backup for each incarnation.
func (r *PITRReconciler) ensureBackup(ctx context.Context, p *v1alpha1.PITR, i *v1alpha1.Instance) (ctrl.Result, error) {
	var backups v1alpha1.BackupList
	if err := r.List(ctx, &backups, client.InNamespace(i.GetNamespace()), client.MatchingLabels{controllers.PITRLabel: p.GetName(), controllers.IncarnationLabel: i.Status.CurrentDatabaseIncarnation}); err != nil {
		r.Log.Error(err, "failed to get a list of Backups applicable to pitr")
		return ctrl.Result{}, err
	}

	applyOpts := []client.PatchOption{client.ForceOwnership, client.FieldOwner("pitr-controller")}
	initialBackup := backupTemplate(p, i)
	initialBackup.ObjectMeta.Name = fmt.Sprintf("%s-incarnation-%s", initialBackup.ObjectMeta.Name, i.Status.CurrentDatabaseIncarnation)
	if len(backups.Items) == 0 {
		r.Log.Info("reconciling PITR ensureBackup: initial backup for current incarnation doesn't exist, creating...")
		if err := r.Patch(ctx, initialBackup, client.Apply, applyOpts...); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	} else if len(backups.Items) == 1 {
		initialBackupReadyCond := k8s.FindCondition(backups.Items[0].Status.Conditions, k8s.Ready)
		switch initialBackupReadyCond.Reason {
		case k8s.BackupFailed:
			r.Log.Info("reconciling PITR ensureBackup: initial backup for current incarnation failed, deleting...")
			if err := r.Delete(ctx, initialBackup); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		case k8s.BackupInProgress, k8s.BackupPending:
			r.Log.Info("reconciling PITR ensureBackup: initial backup for current incarnation in progress, waiting...")
			return ctrl.Result{RequeueAfter: requeueInterval}, nil
		}
	}

	var allBackups v1alpha1.BackupList

	if err := r.List(ctx, &allBackups, client.InNamespace(i.GetNamespace()), client.MatchingLabels{controllers.PITRLabel: p.GetName()}); err != nil {
		r.Log.Error(err, "failed to get a list of Backups applicable to pitr")
		return ctrl.Result{}, err
	}
	p.Status.BackupTotal = len(allBackups.Items)
	return ctrl.Result{}, r.Status().Update(ctx, p)
}

// calculateBackupRetentionCnt calculates number of backups to keep based on backup schedule and recover window
func calculateBackupRetentionCnt(backupSchedule string, recoverWindow time.Duration) int32 {
	startTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	endTime := startTime.Add(recoverWindow)

	// backupSchedule is validated in validatePITRSpec()
	schedule, _ := cron.ParseStandard(backupSchedule)
	backupRetentionCnt := int32(0)
	for startTime.Before(endTime) {
		startTime = schedule.Next(startTime)
		backupRetentionCnt++
	}
	backupRetentionCnt++
	return backupRetentionCnt
}

func backupScheduleTemplate(p *v1alpha1.PITR, i *v1alpha1.Instance) *v1alpha1.BackupSchedule {
	backupSpec := backupTemplate(p, i).Spec
	// backup schedule defaults to every 4 hours
	backupSchedule := "0 */4 * * *"
	// recover window defaults to 7 days
	recoverWindow := time.Hour * 24 * 7
	if p.Spec.BackupSchedule != "" {
		backupSchedule = p.Spec.BackupSchedule
	}
	backupRetentionCnt := calculateBackupRetentionCnt(backupSchedule, recoverWindow)

	PITRbackupSchedule := &v1alpha1.BackupSchedule{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "oracle.db.anthosapis.com/v1alpha1",
			Kind:       "BackupSchedule",
		},
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: p.APIVersion,
					Kind:       p.Kind,
					Name:       p.Name,
					UID:        p.UID,
				},
			},
			Namespace: i.GetNamespace(),
			Name:      "pitr-backup-schedule",
			Labels: map[string]string{
				controllers.PITRLabel: p.GetName(),
			},
		},
		Spec: v1alpha1.BackupScheduleSpec{
			BackupScheduleSpec: commonv1alpha1.BackupScheduleSpec{
				Schedule: backupSchedule,
				BackupRetentionPolicy: &commonv1alpha1.BackupRetentionPolicy{
					BackupRetention: &backupRetentionCnt,
				},
			},
			BackupSpec: backupSpec,
			BackupLabels: map[string]string{
				controllers.PITRLabel: p.GetName(),
			},
		},
	}
	return PITRbackupSchedule
}

func backupTemplate(p *v1alpha1.PITR, i *v1alpha1.Instance) *v1alpha1.Backup {
	PITRbackup := &v1alpha1.Backup{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "oracle.db.anthosapis.com/v1alpha1",
			Kind:       "Backup",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pitr-backup",
			Namespace: i.GetNamespace(),
			Labels: map[string]string{
				controllers.PITRLabel: p.GetName(),
			},
		},
		Spec: v1alpha1.BackupSpec{
			BackupSpec: commonv1alpha1.BackupSpec{
				Instance: i.GetName(),
				// TODO: support PITR using snapshot backup
				Type: "Physical",
			},
			Subtype: "Instance",
			GcsDir:  p.Spec.StorageURI,
		},
	}
	return PITRbackup
}

func (r *PITRReconciler) ensureAgent(ctx context.Context, p *v1alpha1.PITR, i *v1alpha1.Instance) error {
	// TODO better validation
	if p.Spec.Images == nil {
		return errors.New("PITR .spec.images must be specified")
	}
	agentImage, ok := p.Spec.Images[agentImageKey]
	if !ok {
		return fmt.Errorf("failed to find an required image from %v, want image with key %s", p.Spec.Images, agentImageKey)
	}

	options := []client.PatchOption{client.ForceOwnership, client.FieldOwner("pitr-controller")}
	pitrLabel := map[string]string{controllers.PITRLabel: p.GetName()}
	instlabels := map[string]string{"instance": i.GetName()}
	uid := controllers.DefaultUID
	if i.Spec.DatabaseUID != nil {
		uid = *i.Spec.DatabaseUID
	}
	gid := controllers.DefaultGID
	if i.Spec.DatabaseGID != nil {
		gid = *i.Spec.DatabaseGID
	}

	logDiskPVC, logDiskMount := controllers.GetPVCNameAndMount(i.GetName(), "LogDisk")
	// TODO better way to find the PVC
	logDiskPVC = fmt.Sprintf("%s-%s-sts-0", logDiskPVC, i.GetName())
	deployName := fmt.Sprintf(deploymentTemplate, p.GetName())
	// for now, PITR and DB instance are in the same namespace.
	deployNS := p.GetNamespace()

	dbdaemonSvc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Name: fmt.Sprintf(controllers.DbdaemonSvcName, i.GetName()), Namespace: i.GetNamespace()}, dbdaemonSvc); err != nil {
		return err
	}
	dbdaemonIP := dbdaemonSvc.Spec.ClusterIP
	dbdaemonPort := consts.DefaultDBDaemonPort

	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf(PITRSvcTemplate, p.GetName()), Namespace: p.GetNamespace()},
		Spec: corev1.ServiceSpec{
			Selector: pitrLabel,
			Ports: []corev1.ServicePort{
				{
					Name:       "pitr",
					Protocol:   "TCP",
					Port:       DefaultPITRAgentPort,
					TargetPort: intstr.FromInt(DefaultPITRAgentPort),
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}

	deployment := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: deployNS},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: pitrLabel,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:    pitrLabel,
					Namespace: deployNS,
				},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:    pointer.Int64Ptr(uid),
						RunAsGroup:   pointer.Int64Ptr(gid),
						FSGroup:      pointer.Int64Ptr(gid),
						RunAsNonRoot: pointer.Bool(true),
					},
					Volumes: []corev1.Volume{
						{
							Name: "log",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: logDiskPVC,
									ReadOnly:  true,
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:    agentName,
							Image:   agentImage,
							Command: []string{pitrCmd},
							Args: []string{
								"--dbservice=" + dbdaemonIP,
								"--dbport=" + strconv.Itoa(dbdaemonPort),
								"--dest=" + p.Spec.StorageURI,
								"--port=" + strconv.Itoa(DefaultPITRAgentPort),
							},

							Ports: []corev1.ContainerPort{
								{Name: "pitr-port", Protocol: "TCP", ContainerPort: DefaultPITRAgentPort},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: pointer.BoolPtr(false),
							},
							ImagePullPolicy: corev1.PullAlways,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "log",
									ReadOnly:  true,
									MountPath: logDiskMount,
								},
							},
						},
					},
					// Add pod affinity for pitr agent pod, so that pitr agent pod can access DB disk.
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: instlabels,
									},
									Namespaces:  []string{p.GetNamespace()},
									TopologyKey: "kubernetes.io/hostname",
								},
							},
						},
					},
				},
			},
		},
	}

	if err := ctrl.SetControllerReference(p, deployment, r.Scheme); err != nil {
		return err
	}

	if err := r.Patch(ctx, deployment, client.Apply, options...); err != nil {
		return err
	}

	if err := ctrl.SetControllerReference(p, svc, r.Scheme); err != nil {
		return err
	}

	if err := r.Patch(ctx, svc, client.Apply, options...); err != nil {
		return err
	}

	return nil
}

func (r *PITRReconciler) updateStatus(ctx context.Context, p *v1alpha1.PITR, i *v1alpha1.Instance, log logr.Logger) error {
	backups, err := r.BackupCtrl.List(ctx, client.InNamespace(i.GetNamespace()), client.MatchingLabels{controllers.PITRLabel: p.GetName(), controllers.IncarnationLabel: i.Status.CurrentDatabaseIncarnation})
	if err != nil {
		log.Error(err, "failed to get a list of Backups applicable to pitr")
		return errors.New("failed to update available recovery window")
	}

	windows, err := r.PITRCtrl.AvailableRecoveryWindows(ctx, p)
	if err != nil {
		log.Error(err, "failed to get status from data plane")
		return errors.New("failed to update available recovery window")
	}

	timeToBackups := make(map[int64]v1alpha1.Backup)
	for _, b := range backups {
		if b.Status.Phase != commonv1alpha1.BackupSucceeded {
			continue
		}
		bTimestamp, err := time.Parse(time.RFC3339, b.Annotations[controllers.TimestampAnnotation])
		if err != nil {
			log.Error(err, "failed to parse backup timestamp", "backup", b)
			continue
		}
		timeToBackups[bTimestamp.Unix()] = b
	}

	keys := make([]int64, len(timeToBackups))
	idx := 0
	for k := range timeToBackups {
		keys[idx] = k
		idx += 1
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i] < keys[j]
	})

	var aWindowTime []v1alpha1.TimeWindow
	var aWindowSCN []v1alpha1.SCNWindow

	for _, w := range windows {
		firstIdx := sort.Search(len(keys), func(i int) bool {
			return w.GetStart().GetTime().AsTime().Unix() <= keys[i]
		})

		if firstIdx >= len(keys) || keys[firstIdx] > w.GetEnd().GetTime().AsTime().Unix() {
			continue
		}

		aWindowTime = append(aWindowTime, v1alpha1.TimeWindow{
			Begin: metav1.NewTime(time.Unix(keys[firstIdx], 0)),
			End:   metav1.NewTime(w.GetEnd().Time.AsTime()),
		})

		aWindowSCN = append(aWindowSCN, v1alpha1.SCNWindow{
			Begin: timeToBackups[keys[firstIdx]].Annotations[controllers.SCNAnnotation],
			End:   w.GetEnd().GetScn(),
		})
	}

	p.Status.AvailableRecoveryWindowTime = aWindowTime
	p.Status.AvailableRecoveryWindowSCN = aWindowSCN
	p.Status.CurrentDatabaseIncarnation = i.Status.CurrentDatabaseIncarnation

	if len(aWindowTime) > 0 && len(aWindowSCN) > 0 {
		p.Status.Conditions = k8s.Upsert(p.Status.Conditions, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, "")
	}
	return r.PITRCtrl.UpdateStatus(ctx, p)
}

func (r *PITRReconciler) instanceToPITR(obj client.Object) []reconcile.Request {
	var requests []reconcile.Request
	var PITRList v1alpha1.PITRList
	inst := obj.(*v1alpha1.Instance)
	if err := r.List(context.Background(), &PITRList, client.InNamespace(inst.GetNamespace()), client.MatchingLabels{"instance": inst.GetName()}); err != nil {
		r.Log.Info("Failed to list pitr", "instance", inst.GetName())
		return []reconcile.Request{}
	}
	for _, pitr := range PITRList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      pitr.GetName(),
				Namespace: pitr.GetNamespace(),
			}})
	}
	r.Log.Info("Instance event triggered reconcile ", "requests", requests)
	return requests
}

func validatePITRSpec(spec v1alpha1.PITRSpec) []string {
	errMsg := []string{}
	if !strings.HasPrefix(spec.StorageURI, util.GSPrefix) {
		errMsg = append(errMsg, "spec.storageURI only suppore GCS schemes.")
	}
	if spec.BackupSchedule != "" {
		if _, err := cron.ParseStandard(spec.BackupSchedule); err != nil {
			errMsg = append(errMsg, "spec.backupSchedule cannot be parsed.")
		}
	}
	return errMsg
}

func (r *PITRReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PITR{}).
		Watches(
			&source.Kind{Type: &v1alpha1.Instance{}},
			handler.EnqueueRequestsFromMapFunc(r.instanceToPITR),
		).
		Complete(r)
}
