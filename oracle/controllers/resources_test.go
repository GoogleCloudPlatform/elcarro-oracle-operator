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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
)

func TestBuildPVCMounts(t *testing.T) {

	testCases := []struct {
		Name         string
		InstanceName string
		DiskSpec     []commonv1alpha1.DiskSpec
		wantMounts   []corev1.VolumeMount
	}{
		{
			Name:         "default - data and log disk only",
			InstanceName: "myinst",
			DiskSpec: []commonv1alpha1.DiskSpec{
				{Name: "DataDisk"},
				{Name: "LogDisk"},
			},
			wantMounts: []corev1.VolumeMount{
				{
					Name:      "myinst-pvc-u02",
					MountPath: "/u02",
				},
				{
					Name:      "myinst-pvc-u03",
					MountPath: "/u03",
				},
			},
		},
		{
			Name:         "default - data, log and backup",
			InstanceName: "myinst",
			DiskSpec: []commonv1alpha1.DiskSpec{
				{Name: "DataDisk"},
				{Name: "LogDisk"},
				{Name: "BackupDisk"},
			},
			wantMounts: []corev1.VolumeMount{
				{
					Name:      "myinst-pvc-u02",
					MountPath: "/u02",
				},
				{
					Name:      "myinst-pvc-u03",
					MountPath: "/u03",
				},
				{
					Name:      "myinst-pvc-u04",
					MountPath: "/u04",
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			sp := StsParams{
				Disks: tc.DiskSpec,
				Inst: &v1alpha1.Instance{
					ObjectMeta: metav1.ObjectMeta{
						Name: tc.InstanceName,
					},
				},
			}

			gotVolumeMounts := buildPVCMounts(sp)

			if len(gotVolumeMounts) != len(tc.wantMounts) {
				t.Errorf("got len(volumeMounts)=%d, want %d", len(gotVolumeMounts), len(tc.wantMounts))
			}

			for _, wantMount := range tc.wantMounts {
				var gotMount corev1.VolumeMount
				for _, mount := range gotVolumeMounts {
					if mount.Name == wantMount.Name {
						gotMount = mount
						break
					}
				}
				if gotMount.MountPath != wantMount.MountPath {
					t.Errorf("got mountPath=%s, want %s", gotMount.MountPath, wantMount.MountPath)
				}
			}
		})
	}
}
