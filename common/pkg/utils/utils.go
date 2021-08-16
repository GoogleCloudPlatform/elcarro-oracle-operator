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
	"fmt"

	snapv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
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
	defaultStorageClassNameGCP             = "csi-gce-pd"
	defaultVolumeSnapshotClassNameGCP      = "csi-gce-pd-snapshot-class"
	defaultStorageClassNameBM              = "csi-trident"
	defaultVolumeSnapshotClassNameBM       = "csi-trident-snapshot-class"
	defaultStorageClassNameMinikube        = "csi-hostpath-sc"
	defaultVolumeSnapshotClassNameMinikube = "csi-hostpath-snapclass"
)

var (
	defaultDiskSize = resource.MustParse("100Gi")

	DefaultDiskSpecs = map[string]commonv1alpha1.DiskSpec{
		"DataDisk": {
			Name: "DataDisk",
			Size: resource.MustParse("100Gi"),
		},
		"LogDisk": {
			Name: "LogDisk",
			Size: resource.MustParse("150Gi"),
		},
		"BackupDisk": {
			Name: "BackupDisk",
			Size: resource.MustParse("100Gi"),
		},
	}
)

type platformConfig struct {
	storageClassName        string
	volumeSnapshotClassName string
}

func getPlatformConfig(p string) (*platformConfig, error) {
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

func FindDiskSize(diskSpec *commonv1alpha1.DiskSpec, configSpec *commonv1alpha1.ConfigSpec) resource.Quantity {
	spec, exists := DefaultDiskSpecs[diskSpec.Name]
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

func FindStorageClassName(diskSpec *commonv1alpha1.DiskSpec, configSpec *commonv1alpha1.ConfigSpec, defaultPlatform string) (string, error) {
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

	pc, err := getPlatformConfig(platform)
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

func FindVolumeSnapshotClassName(volumneSnapshotClass string, configSpec *commonv1alpha1.ConfigSpec, defaultPlatform string) (string, error) {
	if volumneSnapshotClass != "" {
		return volumneSnapshotClass, nil
	}

	if configSpec != nil && configSpec.VolumeSnapshotClass != "" {
		return configSpec.VolumeSnapshotClass, nil
	}

	platform := setPlatform(defaultPlatform, configSpec)
	pc, err := getPlatformConfig(platform)
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
		ObjectMeta: metav1.ObjectMeta{Name: snapName, Namespace: owner.GetNamespace(), Labels: map[string]string{"snap": snapName}},
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
