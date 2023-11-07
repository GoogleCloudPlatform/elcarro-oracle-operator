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

package controllers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s/ownerref"
	"github.com/go-logr/logr"
	snapv1 "github.com/kubernetes-csi/external-snapshotter/client/v4/apis/volumesnapshot/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/pkg/utils"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
)

const (
	configAgentName = "config-agent"
	// OperatorName is the default operator name.
	OperatorName = "operator"
	scriptDir    = "/agents"
	// DefaultUID is the default Database pod user uid.
	DefaultUID = int64(54321)
	// DefaultGID is the default Database pod user gid.
	DefaultGID                  = int64(54322)
	safeMinMemoryForDBContainer = "4.0Gi"
	podInfoMemRequestSubPath    = "request_memory"
	dbContainerName             = "oracledb"
	podInfoVolume               = "podinfo"
	StoppedReplicaCnt           = 0
	DefaultReplicaCnt           = 1
)

var (
	podInfoDir      = "/etc/podinfo"
	defaultDiskSize = resource.MustParse("100Gi")
	dialTimeout     = 3 * time.Minute
	configList      = []string{configAgentName, OperatorName}
	defaultDisks    = []commonv1alpha1.DiskSpec{
		{
			Name: "DataDisk",
			Size: resource.MustParse("100Gi"),
		},
		{
			Name: "LogDisk",
			Size: resource.MustParse("150Gi"),
		},
	}
)

// NewDBDaemonSvc returns the service for the database daemon server.
func NewDBDaemonSvc(inst *v1alpha1.Instance, scheme *runtime.Scheme) (*corev1.Service, error) {
	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf(DbdaemonSvcName, inst.Name), Namespace: inst.Namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"instance": inst.Name},
			Ports: []corev1.ServicePort{
				{
					Name:       "dbdaemon",
					Protocol:   "TCP",
					Port:       consts.DefaultDBDaemonPort,
					TargetPort: intstr.FromInt(consts.DefaultDBDaemonPort),
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}

	// Set the Instance resource to own the Service resource.
	if err := ctrl.SetControllerReference(inst, svc, scheme); err != nil {
		return svc, err
	}

	return svc, nil
}

// NewAgentSvc returns the service for the agent.
func NewAgentSvc(inst *v1alpha1.Instance, scheme *runtime.Scheme) (*corev1.Service, error) {
	var ports []corev1.ServicePort
	for service, enabled := range inst.Spec.Services {
		switch service {
		case commonv1alpha1.Monitoring:
			if enabled {
				ports = append(ports, corev1.ServicePort{
					Name:     consts.MonitoringAgentName,
					Protocol: "TCP",
					Port:     consts.DefaultMonitoringAgentPort,
				})
			}
		}
	}
	if len(ports) == 0 {
		return nil, nil
	}
	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf(AgentSvcName, inst.Name),
			Namespace: inst.Namespace,
			Labels:    map[string]string{"app": "agent-svc"},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"instance-agent": fmt.Sprintf("%s-agent", inst.Name)},
			Ports:    ports,
			Type:     corev1.ServiceTypeClusterIP,
		},
	}

	// Set the Instance resource to own the Service resource.
	if err := ctrl.SetControllerReference(inst, svc, scheme); err != nil {
		return svc, err
	}

	return svc, nil
}

// NewConfigMap returns the config map for database env variables.
func NewConfigMap(inst *v1alpha1.Instance, scheme *runtime.Scheme, cmName string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: inst.Namespace},
		Data: map[string]string{
			"SCRIPTS_DIR":           scriptDir,
			"INSTALL_DIR":           "/stage",
			"HEALTHCHECK_DB_SCRIPT": "health-check-db.sh",
		},
	}

	// Set the Instance resource to own the ConfigMap resource.
	if err := ctrl.SetControllerReference(inst, cm, scheme); err != nil {
		return cm, err
	}

	return cm, nil
}

// NewSts returns the statefulset for the database pod.
func NewSts(sp StsParams, pvcs []corev1.PersistentVolumeClaim, podTemplate corev1.PodTemplateSpec) (*appsv1.StatefulSet, error) {
	var replicas int32 = DefaultReplicaCnt
	sts := &appsv1.StatefulSet{
		// It looks like the version needs to be explicitly set to avoid the
		// "incorrect version specified in apply patch" error.
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: metav1.ObjectMeta{Name: sp.StsName, Namespace: sp.Inst.Namespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			// UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"instance": sp.Inst.Name, "statefulset": sp.StsName},
			},
			Template: podTemplate,
			// Do we need a pointer to a service in a StatefulSet?
			// ServiceName:          sp.svcName,
			VolumeClaimTemplates: pvcs,
		},
	}

	// Set the Instance resource to own the StatefulSet resource.
	if err := ctrl.SetControllerReference(sp.Inst, sts, sp.Scheme); err != nil {
		return sts, err
	}

	return sts, nil
}

// GetLogLevelArgs returns agent args for log level.
func GetLogLevelArgs(config *v1alpha1.Config) map[string][]string {
	agentArgs := make(map[string][]string)
	if config == nil {
		return agentArgs
	}

	for _, name := range configList {
		args := []string{}
		if len(config.Spec.LogLevel[name]) > 0 {
			args = append(args, fmt.Sprintf("--v=%s", config.Spec.LogLevel[name]))
		}
		agentArgs[name] = args
	}

	return agentArgs
}

func MonitoringPodTemplate(inst *v1alpha1.Instance, monitoringSecret *corev1.Secret, images map[string]string) corev1.PodTemplateSpec {
	svcName := fmt.Sprintf(SvcName, inst.Name)
	dbdName := GetDBDomain(inst)
	names := []string{inst.Spec.CDBName}
	if dbdName != "" {
		names = append(names, dbdName)
	}
	falseVal := false

	containers := []corev1.Container{{
		Name:  "monitor",
		Image: images["monitoring"], // TODO: Use constant
		Env: []corev1.EnvVar{
			{
				Name:  "DATA_SOURCE_URI",
				Value: fmt.Sprintf("oracle://%s:%d/%s", svcName, consts.SecureListenerPort, strings.Join(names, ".")),
			},
			{
				Name:  "DATA_SOURCE_USER_FILE",
				Value: "/mon-creds/username",
			},
			{
				Name:  "DATA_SOURCE_PASS_FILE",
				Value: "/mon-creds/password",
			},
		},
		// TODO: Standardize metrics port.
		Ports: []corev1.ContainerPort{
			{ContainerPort: 9187, Protocol: corev1.ProtocolTCP},
		},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &falseVal,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"NET_RAW"}},
		},
		ImagePullPolicy: corev1.PullAlways,
		VolumeMounts: []corev1.VolumeMount{
			{MountPath: "/mon-creds/", Name: "mon-creds"},
		},
	}}

	podSpec := corev1.PodSpec{
		SecurityContext: &corev1.PodSecurityContext{},
		Containers:      containers,
		// Add pod affinity for agent pod, so that k8s will try to schedule the agent pod
		// to the same node where the paired DB pod is located. In this way, we can avoid
		// unnecessary cross node communication.
		Affinity: &corev1.Affinity{
			PodAffinity: &corev1.PodAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
					{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"instance":  inst.Name,
								"task-type": DatabaseTaskType,
							},
						},
						Namespaces:  []string{inst.Namespace},
						TopologyKey: "kubernetes.io/hostname",
					},
				},
			},
		},
		Volumes: []corev1.Volume{{
			Name: "mon-creds",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: monitoringSecret.Name,
				},
			},
		}},
	}

	template := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: inst.Namespace,
			// Inform prometheus/opentel that we report metrics.
			Annotations: map[string]string{
				"prometheus.io/scrape": "true",
			},
		},
		Spec: podSpec,
	}

	return template
}

// NewPVCs returns PVCs.
func NewPVCs(sp StsParams) ([]corev1.PersistentVolumeClaim, error) {
	var pvcs []corev1.PersistentVolumeClaim

	for _, diskSpec := range sp.Disks {
		var configSpec *commonv1alpha1.ConfigSpec
		if sp.Config != nil {
			configSpec = &sp.Config.Spec.ConfigSpec
		}
		rl := corev1.ResourceList{corev1.ResourceStorage: utils.FindDiskSize(&diskSpec, configSpec, DefaultDiskSpecs, defaultDiskSize)}
		pvcName, mount := GetPVCNameAndMount(sp.Inst.Name, diskSpec.Name)
		var pvc corev1.PersistentVolumeClaim

		// Determine storage class (from disk spec or config)
		storageClass, err := utils.FindStorageClassName(&diskSpec, configSpec, utils.PlatformGCP, utils.EngineOracle)
		if err != nil || storageClass == "" {
			return nil, fmt.Errorf("failed to identify a storageClassName for disk %q", diskSpec.Name)
		}
		sp.Log.Info("storage class identified", "disk", diskSpec.Name, "StorageClass", storageClass)

		var pvcAnnotations map[string]string
		if diskSpec.Annotations != nil {
			pvcAnnotations = diskSpec.Annotations
		}

		var ownerRef []metav1.OwnerReference
		if !sp.Inst.Spec.RetainDisksAfterInstanceDeletion {
			// Instead of manually handling PVCs after an instance is deleted,
			// we leverage ownerRef to automatically delete PVCs if necessary.
			ownerRef = []metav1.OwnerReference{
				ownerref.New(sp.Inst, true, true),
			}
		}

		pvc = corev1.PersistentVolumeClaim{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
			ObjectMeta: metav1.ObjectMeta{
				Name:            pvcName,
				Namespace:       sp.Inst.Namespace,
				Annotations:     pvcAnnotations,
				OwnerReferences: ownerRef,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{"ReadWriteOnce"},
				Resources:        corev1.ResourceRequirements{Requests: rl},
				StorageClassName: func() *string { s := storageClass; return &s }(),
			},
		}

		if sp.Restore != nil && sp.Restore.BackupID != "" {
			sp.Log.Info("starting a restore process for disk", "mount", mount)
			pvc.Spec.DataSource = &corev1.TypedLocalObjectReference{
				APIGroup: func() *string { s := string("snapshot.storage.k8s.io"); return &s }(),
				Kind:     "VolumeSnapshot",
				Name:     fmt.Sprintf("%s-%s", sp.Restore.BackupID, mount),
			}
		} else {
			sp.Log.Info("starting a provisioning process for disk", "mount", mount)
		}

		pvcs = append(pvcs, pvc)
	}

	return pvcs, nil
}

func buildPVCMounts(sp StsParams) []corev1.VolumeMount {
	var diskMounts []corev1.VolumeMount

	for _, diskSpec := range sp.Disks {
		pvcName, mount := GetPVCNameAndMount(sp.Inst.Name, diskSpec.Name)
		diskMounts = append(diskMounts, corev1.VolumeMount{
			Name:      pvcName,
			MountPath: fmt.Sprintf("/%s", mount),
		})
	}

	return diskMounts
}

// NewPodTemplate returns the pod template for the database statefulset.
func NewPodTemplate(sp StsParams, inst v1alpha1.Instance) corev1.PodTemplateSpec {
	cdbName := inst.Spec.CDBName
	DBDomain := GetDBDomain(&inst)
	labels := map[string]string{
		"instance":    sp.Inst.Name,
		"statefulset": sp.StsName,
		"task-type":   DatabaseTaskType,
	}

	// Set default safeguard memory if the database resource is not specified.
	dbResource := sp.Inst.Spec.DatabaseResources
	if dbResource.Requests == nil {
		dbResource.Requests = corev1.ResourceList{}
	}
	if dbResource.Requests.Memory() == nil {
		sp.Log.Info("NewPodTemplate: No memory request found for DB. Setting default safeguard memory", "SafeMinMemoryForDBContainer", safeMinMemoryForDBContainer)
		dbResource.Requests[corev1.ResourceMemory] = resource.MustParse(safeMinMemoryForDBContainer)
	}

	// Kind cluster can only use local images
	imagePullPolicy := corev1.PullAlways
	if sp.Config != nil && sp.Config.Spec.Platform == utils.PlatformKind {
		imagePullPolicy = corev1.PullIfNotPresent
	}

	sp.Log.Info("NewPodTemplate: creating new template with images", "images", sp.Images)
	dataDiskPVC, dataDiskMountName := GetPVCNameAndMount(sp.Inst.Name, "DataDisk")

	containers := []corev1.Container{
		{
			Name:      dbContainerName,
			Resources: dbResource,
			Image:     sp.Images["service"],
			Command:   []string{fmt.Sprintf("%s/init_container.sh", scriptDir)},
			Env: []corev1.EnvVar{
				{
					Name:  "SCRIPTS_DIR",
					Value: scriptDir,
				},
				{
					Name:  "PROVISIONDONE_FILE",
					Value: consts.ProvisioningDoneFile,
				},
			},
			Args: []string{cdbName, DBDomain},
			Ports: []corev1.ContainerPort{
				{Name: "secure-listener", Protocol: "TCP", ContainerPort: consts.SecureListenerPort},
				{Name: "ssl-listener", Protocol: "TCP", ContainerPort: consts.SSLListenerPort},
			},
			VolumeMounts: append([]corev1.VolumeMount{
				{Name: "var-tmp", MountPath: "/var/tmp"},
				{Name: "agent-repo", MountPath: "/agents"},
				{Name: podInfoVolume, MountPath: podInfoDir, ReadOnly: true},
			},
				buildPVCMounts(sp)...),
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &sp.PrivEscalation,
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"NET_RAW"}},
			},
			EnvFrom: []corev1.EnvFromSource{
				{
					ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: sp.ConfigMap.ObjectMeta.Name}},
				},
			},
			ImagePullPolicy: imagePullPolicy,
		},
		{
			Name:    "dbdaemon",
			Image:   sp.Images["service"],
			Command: []string{fmt.Sprintf("%s/init_dbdaemon.sh", scriptDir)},
			Args:    []string{cdbName},
			Ports: []corev1.ContainerPort{
				{Name: "dbdaemon", Protocol: "TCP", ContainerPort: consts.DefaultDBDaemonPort},
			},
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &sp.PrivEscalation,
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"NET_RAW"}},
			},
			VolumeMounts: append([]corev1.VolumeMount{
				{Name: "var-tmp", MountPath: "/var/tmp"},
				{Name: "agent-repo", MountPath: "/agents"},
				{Name: podInfoVolume, MountPath: podInfoDir},
			},
				buildPVCMounts(sp)...),
			ImagePullPolicy: imagePullPolicy,
		},
		{
			Name:    "alert-log-sidecar",
			Image:   sp.Images["logging_sidecar"],
			Command: []string{"/logging_main"},
			Args:    []string{"--logType=ALERT"},
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &sp.PrivEscalation,
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"NET_RAW"}},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: dataDiskPVC, MountPath: fmt.Sprintf("/%s", dataDiskMountName)},
				{Name: podInfoVolume, MountPath: podInfoDir, ReadOnly: true},
			},
			ImagePullPolicy: imagePullPolicy,
		},
		{
			Name:    "listener-log-sidecar",
			Image:   sp.Images["logging_sidecar"],
			Command: []string{"/logging_main"},
			Args:    []string{"--logType=LISTENER"},
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &sp.PrivEscalation,
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"NET_RAW"}},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: dataDiskPVC, MountPath: fmt.Sprintf("/%s", dataDiskMountName)},
				{Name: podInfoVolume, MountPath: podInfoDir, ReadOnly: true},
			},
			ImagePullPolicy: imagePullPolicy,
		},
	}
	initContainers := []corev1.Container{
		{
			Name:    "dbinit",
			Image:   sp.Images["dbinit"],
			Command: []string{"sh", "-c", "cp -r agent_repo/. /agents/ && chmod -R 750 /agents/*"},
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &sp.PrivEscalation,
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"NET_RAW"}},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "agent-repo", MountPath: "/agents"},
			},
			ImagePullPolicy: imagePullPolicy,
		},
	}

	volumes := []corev1.Volume{
		{
			Name:         "var-tmp",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
		{
			Name:         "agent-repo",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
		{
			Name: podInfoVolume,
			VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{
				Items: []corev1.DownwardAPIVolumeFile{{
					Path: podInfoMemRequestSubPath,
					ResourceFieldRef: &corev1.ResourceFieldSelector{
						ContainerName: dbContainerName,
						Resource:      "requests.memory",
						Divisor:       resource.MustParse("1Mi"),
					},
				},
				},
			}},
		},
	}

	uid := sp.Inst.Spec.DatabaseUID
	if uid == nil {
		sp.Log.Info("set pod user ID to default value", "UID", DefaultUID)
		// consts are not addressable
		uid = func(i int64) *int64 { return &i }(DefaultUID)
	}

	gid := sp.Inst.Spec.DatabaseGID
	if gid == nil {
		sp.Log.Info("set pod group ID to default value", "GID", DefaultGID)
		// consts are not addressable
		gid = func(i int64) *int64 { return &i }(DefaultGID)
	}

	// for minikube/kind, the default csi-hostpath-driver mounts persistent volumes writable by root only, so explicitly
	// change owner and permissions of mounted pvs with an init container.
	if sp.Config != nil && (sp.Config.Spec.Platform == utils.PlatformMinikube || sp.Config.Spec.Platform == utils.PlatformKind) {
		initContainers = addHostpathInitContainer(sp, initContainers, *uid, *gid)
	}

	podSpec := corev1.PodSpec{
		SecurityContext: &corev1.PodSecurityContext{
			RunAsUser:    uid,
			RunAsGroup:   gid,
			FSGroup:      gid,
			RunAsNonRoot: func(b bool) *bool { return &b }(true),
		},
		// ImagePullSecrets: []corev1.LocalObjectReference {{Name: GcrSecretName }},
		Containers:            containers,
		InitContainers:        initContainers,
		ShareProcessNamespace: func(b bool) *bool { return &b }(true),
		// ServiceAccountName:
		// TerminationGracePeriodSeconds:
		Tolerations: inst.Spec.PodSpec.Tolerations,
		Volumes:     volumes,
		Affinity:    inst.Spec.PodSpec.Affinity,
	}

	// TODO(bdali): consider adding priority class name, secret mount.

	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:    labels,
			Namespace: sp.Namespace,
			// Annotations: annotations,
		},
		Spec: podSpec,
	}
}

// NewSnapshot returns the snapshot for the given instance and pv.
func NewSnapshotInst(inst *v1alpha1.Instance, scheme *runtime.Scheme, pvcName, snapName, volumeSnapshotClassName string) (*snapv1.VolumeSnapshot, error) {
	snap := &snapv1.VolumeSnapshot{
		TypeMeta:   metav1.TypeMeta{APIVersion: snapv1.SchemeGroupVersion.String(), Kind: "VolumeSnapshot"},
		ObjectMeta: metav1.ObjectMeta{Name: snapName, Namespace: inst.Namespace, Labels: map[string]string{"name": snapName}},
		Spec: snapv1.VolumeSnapshotSpec{
			Source:                  snapv1.VolumeSnapshotSource{PersistentVolumeClaimName: &pvcName},
			VolumeSnapshotClassName: func() *string { s := string(volumeSnapshotClassName); return &s }(),
		},
	}

	// Set the Instance resource to own the VolumeSnapshot resource.
	if err := ctrl.SetControllerReference(inst, snap, scheme); err != nil {
		return snap, err
	}

	return snap, nil
}

// checkStatusInstance attempts to determine a state of an database instance.
// In particular:
//   - has provisioning finished?
//   - is Instance up and accepting connection requests?
var CheckStatusInstanceFunc = func(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, instName, cdbName, namespace, clusterIP, DBDomain string, log logr.Logger) (string, error) {
	if clusterIP != "" {
		log.Info("resources/checkStatusInstance", "inst name", instName, "clusterIP", clusterIP)
	} else {
		log.Info("resources/checkStatusInstance", "inst name", instName)
	}

	checkStatusReq := &CheckStatusRequest{
		Name:            instName,
		CdbName:         cdbName,
		CheckStatusType: CheckStatusRequest_INSTANCE,
		DbDomain:        DBDomain,
	}
	cdOut, err := CheckStatus(ctx, r, dbClientFactory, namespace, instName, *checkStatusReq)
	if err != nil {
		return "", fmt.Errorf("resource/checkStatusInstance: failed on CheckStatus call: %v", err)
	}
	log.Info("resource/CheckStatusInstance: DONE with this output", "out", cdOut)

	return cdOut.Status, nil
}

// GetDBDomain figures out DBDomain from DBUniqueName and DBDomain.
func GetDBDomain(inst *v1alpha1.Instance) string {
	// Does DBUniqueName contain a DB Domain suffix?
	if strings.Contains(inst.Spec.DBUniqueName, ".") {
		domainFromName := strings.SplitN(inst.Spec.DBUniqueName, ".", 2)[1]
		return domainFromName
	}

	return inst.Spec.DBDomain
}

func addHostpathInitContainer(sp StsParams, containers []corev1.Container, uid, gid int64) []corev1.Container {
	volumeMounts := buildPVCMounts(sp)
	cmd := ""
	for _, mount := range volumeMounts {
		if cmd != "" {
			cmd += " && "
		}
		cmd += fmt.Sprintf("chown %d:%d %s ", uid, gid, mount.MountPath)
	}
	sp.Log.Info("add an init container for csi-hostpath-sc type pv", "cmd", cmd)
	return append(containers, corev1.Container{
		Name:    "prepare-pv-container",
		Image:   "busybox:latest",
		Command: []string{"sh", "-c", cmd},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                func(i int64) *int64 { return &i }(0),
			RunAsGroup:               func(i int64) *int64 { return &i }(0),
			RunAsNonRoot:             func(b bool) *bool { return &b }(false),
			AllowPrivilegeEscalation: &sp.PrivEscalation,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"NET_RAW"}},
		},
		VolumeMounts: volumeMounts,
	})
}

func DiskSpecs(inst *v1alpha1.Instance, config *v1alpha1.Config) []commonv1alpha1.DiskSpec {
	if inst != nil && inst.Spec.Disks != nil {
		return inst.Spec.Disks
	}
	if config != nil && config.Spec.Disks != nil {
		return config.Spec.Disks
	}
	return defaultDisks
}

func RequestedMemoryInMi() (int, error) {
	p := filepath.Join(podInfoDir, podInfoMemRequestSubPath)
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, fmt.Errorf("Failed to open file [%v], error: %v", p, err)
	} else {
		s := string(b)
		if i, err := strconv.Atoi(s); err != nil {
			return 0, fmt.Errorf("Failed to convert [%v] to int, error: %w", s, err)
		} else {
			return i, nil
		}
	}
}
