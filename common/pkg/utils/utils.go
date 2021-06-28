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

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	snapv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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
