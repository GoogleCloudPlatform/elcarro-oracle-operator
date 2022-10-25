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

// Package utils features auxiliary functions for the Anthos DB Operator compliant resources.
package utils

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"

	snapv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
)

const (
	PlatformGCP                            = "GCP"
	PlatformBareMetal                      = "BareMetal"
	PlatformMinikube                       = "Minikube"
	PlatformKind                           = "Kind"
	EnginePostgres                         = "Postgres"
	EngineOracle                           = "Oracle"
	defaultStorageClassNameGCP             = "standard-rwo"
	defaultVolumeSnapshotClassNameGCP      = "csi-gce-pd-snapshot-class"
	defaultStorageClassNameBM              = "csi-trident"
	defaultVolumeSnapshotClassNameBM       = "csi-trident-snapshot-class"
	defaultStorageClassNameMinikube        = "csi-hostpath-sc"
	defaultVolumeSnapshotClassNameMinikube = "csi-hostpath-snapclass"
)

var (
	ErrPodUnschedulable      = errors.New("Pod is unschedulable")
	ErrNoResources           = errors.New("Insufficient resources to create Pod")
	NoResourcesMessageRegexp = "Insufficient memory|Insufficient cpu|enough free storage"
)

type platformConfig struct {
	storageClassName        string
	volumeSnapshotClassName string
}

func getPlatformConfig(p string, e string) (*platformConfig, error) {
	// for Postgres, it allows to have no platform specified
	if p == "" && e == EnginePostgres {
		return &platformConfig{}, nil
	}

	switch p {
	case PlatformGCP:
		return &platformConfig{
			storageClassName:        defaultStorageClassNameGCP,
			volumeSnapshotClassName: defaultVolumeSnapshotClassNameGCP,
		}, nil
	case PlatformBareMetal:
		return &platformConfig{
			storageClassName:        defaultStorageClassNameBM,
			volumeSnapshotClassName: defaultVolumeSnapshotClassNameBM,
		}, nil
	case PlatformMinikube, PlatformKind:
		return &platformConfig{
			storageClassName:        defaultStorageClassNameMinikube,
			volumeSnapshotClassName: defaultVolumeSnapshotClassNameMinikube,
		}, nil
	default:
		return nil, fmt.Errorf("the current release doesn't support deployment platform %q", p)
	}
}

func FindDiskSize(diskSpec *commonv1alpha1.DiskSpec, configSpec *commonv1alpha1.ConfigSpec, defaultDiskSpecs map[string]commonv1alpha1.DiskSpec, defaultDiskSize resource.Quantity) resource.Quantity {
	spec, exists := defaultDiskSpecs[diskSpec.Name]
	if !exists {
		return defaultDiskSize
	}

	if !diskSpec.Size.IsZero() {
		return diskSpec.Size
	}

	if configSpec != nil {
		for _, d := range configSpec.Disks {
			if d.Name == diskSpec.Name {
				if !d.Size.IsZero() {
					return d.Size
				}
				break
			}
		}
	}

	return spec.Size
}

func FindStorageClassName(diskSpec *commonv1alpha1.DiskSpec, configSpec *commonv1alpha1.ConfigSpec, defaultPlatform string, engineType string) (string, error) {
	if diskSpec.StorageClass != "" {
		return diskSpec.StorageClass, nil
	}

	if configSpec != nil {
		for _, d := range configSpec.Disks {
			if d.Name == diskSpec.Name {
				if d.StorageClass != "" {
					return d.StorageClass, nil
				}
				break
			}
		}

		if configSpec.StorageClass != "" {
			return configSpec.StorageClass, nil
		}
	}

	platform := setPlatform(defaultPlatform, configSpec)
	pc, err := getPlatformConfig(platform, engineType)
	if err != nil {
		return "", err
	}
	return pc.storageClassName, nil
}

func setPlatform(defaultPlatform string, configSpec *commonv1alpha1.ConfigSpec) string {
	platform := defaultPlatform
	if configSpec != nil && configSpec.Platform != "" {
		platform = configSpec.Platform
	}
	return platform
}

func FindVolumeSnapshotClassName(volumneSnapshotClass string, configSpec *commonv1alpha1.ConfigSpec, defaultPlatform string, engineType string) (string, error) {
	if volumneSnapshotClass != "" {
		return volumneSnapshotClass, nil
	}

	if configSpec != nil && configSpec.VolumeSnapshotClass != "" {
		return configSpec.VolumeSnapshotClass, nil
	}

	platform := setPlatform(defaultPlatform, configSpec)
	pc, err := getPlatformConfig(platform, engineType)
	if err != nil {
		return "", err
	}
	return pc.volumeSnapshotClassName, nil
}

// DiskSpaceTotal is a helper function to calculate the total amount
// of allocated space across all disks requested for an instance.
func DiskSpaceTotal(inst commonv1alpha1.Instance) (int64, error) {
	spec := inst.InstanceSpec()
	if spec.Disks == nil {
		return -1, fmt.Errorf("failed to detect requested disks for inst: %v", spec)
	}
	var total int64
	for _, d := range spec.Disks {
		i, ok := d.Size.AsInt64()
		if !ok {
			return -1, fmt.Errorf("Invalid size provided for disk: %v. An integer must be provided.\n", d)
		}
		total += i
	}

	return total, nil
}

// SnapshotDisks takes a snapshot of each disk as provided in diskSpecs. Ownership of the snapshots are granted to owner object.
// getPvcSnapshotName is a function that, given a DiskSpec, returns the full PVC name, snapshot name, and volumeSnapshotClassName of that disk.
// Taking snapshots here is best-effort only: it will returns errors even if only 1 disk failed the snapshot, and upon retry it will try to take snapshot of all disks again.
func SnapshotDisks(ctx context.Context, diskSpecs []commonv1alpha1.DiskSpec, owner metav1.Object, c client.Client, scheme *runtime.Scheme,
	getPvcSnapshotName func(commonv1alpha1.DiskSpec) (string, string, string), applyOpts []client.PatchOption) error {
	for _, diskSpec := range diskSpecs {

		fullPVCName, snapshotName, vsc := getPvcSnapshotName(diskSpec)
		snap, err := newSnapshot(owner, scheme, fullPVCName, snapshotName, vsc)
		if err != nil {
			return err
		}

		if err := c.Patch(ctx, snap, client.Apply, applyOpts...); err != nil {
			return err
		}
	}
	return nil
}

// newSnapshot returns the snapshot for the given pv and set owner to own that snapshot.
func newSnapshot(owner v1.Object, scheme *runtime.Scheme, pvcName, snapName, volumeSnapshotClassName string) (*snapv1.VolumeSnapshot, error) {

	snapshot := &snapv1.VolumeSnapshot{
		TypeMeta:   metav1.TypeMeta{APIVersion: snapv1.SchemeGroupVersion.String(), Kind: "VolumeSnapshot"},
		ObjectMeta: metav1.ObjectMeta{Name: snapName, Namespace: owner.GetNamespace(), Labels: map[string]string{"name": snapName}},
		Spec: snapv1.VolumeSnapshotSpec{
			Source:                  snapv1.VolumeSnapshotSource{PersistentVolumeClaimName: &pvcName},
			VolumeSnapshotClassName: func() *string { s := string(volumeSnapshotClassName); return &s }(),
		},
	}

	// Set the owner resource to own the VolumeSnapshot resource.
	if err := ctrl.SetControllerReference(owner, snapshot, scheme); err != nil {
		return snapshot, err
	}

	return snapshot, nil
}

// LoadBalancerAnnotations returns cloud provider specific annotations that must be attached to a LoadBalancer k8s service during creation
func LoadBalancerAnnotations(options *commonv1alpha1.DBLoadBalancerOptions) map[string]string {
	var annotations map[string]string
	if options != nil {
		if options.GCP.LoadBalancerType == "Internal" {
			annotations = map[string]string{
				"cloud.google.com/load-balancer-type": "Internal",
			}
		}
	}
	return annotations
}

// LoadBalancerIpAddress returns an IP address address for the Load Balancer if specified. Otherwise, the empty string is returned.
func LoadBalancerIpAddress(options *commonv1alpha1.DBLoadBalancerOptions) string {
	if options != nil {
		return options.GCP.LoadBalancerIP
	}
	return ""
}

// LoadBalancerURL returns a URL that can be used to connect to a Load Balancer.
func LoadBalancerURL(svc *corev1.Service, port int) string {
	if svc == nil || len(svc.Status.LoadBalancer.Ingress) == 0 {
		return ""
	}

	hostName := svc.Status.LoadBalancer.Ingress[0].Hostname
	if hostName == "" {
		hostName = svc.Status.LoadBalancer.Ingress[0].IP
	}

	return net.JoinHostPort(hostName, fmt.Sprintf("%d", port))
}

func ObjectKeyOf(sts *appsv1.StatefulSet, pvc *corev1.PersistentVolumeClaim, i int) client.ObjectKey {
	// name template from https://github.com/kubernetes/kubernetes/blob/v1.23.5/pkg/controller/ssettatefulset/stateful_set_utils.go#L96
	return client.ObjectKey{
		Namespace: sts.Namespace,
		Name:      fmt.Sprintf("%v-%v-%v", pvc.GetName(), sts.Name, i),
	}
}

// Check if POD initialiazation is stuck because of insufficient resources
// It will return error if Pod creation is stuck and needs manual intervention.
func VerifyPodsStatus(ctx context.Context, cli client.Client, sts *appsv1.StatefulSet) error {
	pods, err := FindPods(ctx, cli, sts)
	if err != nil {
		return err
	}
	for _, pod := range pods {
		if cond := GetPodCondition(pod, corev1.PodScheduled); cond != nil && cond.Reason == corev1.PodReasonUnschedulable {
			r := regexp.MustCompile(NoResourcesMessageRegexp)
			if match := r.MatchString(cond.Message); match {
				return fmt.Errorf("%s: %w", cond.Message, ErrNoResources)
			}
			return fmt.Errorf("%s: %w", cond.Message, ErrPodUnschedulable)

		}
	}
	return nil
}

func FindPods(ctx context.Context, cli client.Client, sts *appsv1.StatefulSet) ([]corev1.Pod, error) {
	if sts == nil || sts.Spec.Selector == nil {
		return nil, nil
	}
	var pods corev1.PodList
	labels := sts.Spec.Selector.MatchLabels
	if err := cli.List(ctx, &pods, client.MatchingLabels(labels), client.InNamespace(sts.Namespace)); err != nil {
		return nil, err
	}
	return pods.Items, nil
}

func GetPodCondition(pod corev1.Pod, condType corev1.PodConditionType) *corev1.PodCondition {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == condType {
			return &cond
		}
	}
	return nil
}
