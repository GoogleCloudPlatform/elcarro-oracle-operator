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

package instancecontroller

import (
	"slices"
	"strings"
	"testing"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPatchingSMEntryCondition(t *testing.T) {
	enabledServices := map[commonv1alpha1.Service]bool{
		commonv1alpha1.Patching: true,
	}
	/*
		Tests covered:
		Patching workflow should trigger when a new image is specified from a stable state.
		Not having a new image should not trigger patching.
		Patching should start from other stable states like RestoreComplete when a new image is specified.
		Patching shouldn't start from PatchingRecoveryCompleted stage when buggy image is specified. (This avoids patching retry loop)
	*/
	testCases := []struct {
		Name                string
		ActiveImages        map[string]string
		SpImages            map[string]string
		LastFailedImages    map[string]string
		InstanceReadyCond   v1.Condition
		DbInstanceReadyCond v1.Condition
		ExpectedResult      bool
	}{
		{
			Name: "Having a new image should trigger patching",
			ActiveImages: map[string]string{
				"service":         "service_image",
				"dbinit":          "dbinit_image",
				"monitoring":      "monitoring_image",
				"logging_sidecar": "logging_sidecar_image",
			},
			SpImages: map[string]string{
				"service":         "service_image_v2",
				"dbinit":          "dbinit_image",
				"monitoring":      "monitoring_image",
				"logging_sidecar": "logging_sidecar_image",
			},
			InstanceReadyCond: v1.Condition{
				Status: v1.ConditionTrue,
				Reason: k8s.CreateComplete,
			},
			DbInstanceReadyCond: v1.Condition{
				Status: v1.ConditionTrue,
				Reason: k8s.CreateComplete,
			},
			ExpectedResult: true,
		},
		{
			Name: "Not having a new image should not trigger patching",
			ActiveImages: map[string]string{
				"service":         "service_image",
				"dbinit":          "dbinit_image",
				"monitoring":      "monitoring_image",
				"logging_sidecar": "logging_sidecar_image",
			},
			SpImages: map[string]string{
				"service":         "service_image",
				"dbinit":          "dbinit_image",
				"monitoring":      "monitoring_image",
				"logging_sidecar": "logging_sidecar_image",
			},
			InstanceReadyCond: v1.Condition{
				Status: v1.ConditionTrue,
				Reason: k8s.CreateComplete,
			},
			DbInstanceReadyCond: v1.Condition{
				Status: v1.ConditionTrue,
				Reason: k8s.CreateComplete,
			},
			ExpectedResult: false,
		},
		{
			Name: "Instance in RestoreComplete state should permit patching",
			ActiveImages: map[string]string{
				"service":         "service_image",
				"dbinit":          "dbinit_image",
				"monitoring":      "monitoring_image",
				"logging_sidecar": "logging_sidecar_image",
			},
			SpImages: map[string]string{
				"service":         "service_image_v2",
				"dbinit":          "dbinit_image",
				"monitoring":      "monitoring_image",
				"logging_sidecar": "logging_sidecar_image",
			},
			InstanceReadyCond: v1.Condition{
				Status: v1.ConditionTrue,
				Reason: k8s.RestoreComplete,
			},
			DbInstanceReadyCond: v1.Condition{
				Status: v1.ConditionTrue,
				Reason: k8s.CreateComplete,
			},
			ExpectedResult: true,
		},
		{
			Name: "Patching shouldn't start when a buggy image is specified",
			ActiveImages: map[string]string{
				"service":         "service_image",
				"dbinit":          "dbinit_image",
				"monitoring":      "monitoring_image",
				"logging_sidecar": "logging_sidecar_image",
			},
			SpImages: map[string]string{
				"service":         "buggy_service_image",
				"dbinit":          "dbinit_image",
				"monitoring":      "monitoring_image",
				"logging_sidecar": "logging_sidecar_image",
			},
			LastFailedImages: map[string]string{
				"service":         "buggy_service_image",
				"dbinit":          "dbinit_image",
				"monitoring":      "monitoring_image",
				"logging_sidecar": "logging_sidecar_image",
			},
			InstanceReadyCond: v1.Condition{
				Status: v1.ConditionTrue,
				Reason: k8s.PatchingRecoveryCompleted,
			},
			DbInstanceReadyCond: v1.Condition{
				Status: v1.ConditionTrue,
				Reason: k8s.CreateComplete,
			},
			ExpectedResult: false,
		},
	}
	for _, tc := range testCases {
		if tc.ExpectedResult != IsPatchingStateMachineEntryCondition(enabledServices, tc.ActiveImages, tc.SpImages, tc.LastFailedImages, &tc.InstanceReadyCond, &tc.DbInstanceReadyCond) {
			t.Errorf("Patching shouldn't have started under the following conditions: %s,%s,%s,%s,%s", tc.ActiveImages, tc.SpImages, tc.LastFailedImages, &tc.InstanceReadyCond, &tc.DbInstanceReadyCond)
		}
	}

}

type shortDisk struct {
	name, size string
}

func TestFilterDiskWithSizeChanged(t *testing.T) {
	tests := []struct {
		name string
		old  []shortDisk
		new  []shortDisk
		want []shortDisk
	}{
		{
			"empty list",
			[]shortDisk{},
			[]shortDisk{},
			[]shortDisk{},
		},
		{
			"no change",
			[]shortDisk{{"foo", "1Gi"}, {"bar", "2Gi"}},
			[]shortDisk{{"bar", "2Gi"}, {"foo", "1Gi"}},
			[]shortDisk{},
		},
		{
			"size change",
			[]shortDisk{{"foo", "1Gi"}, {"bar", "2Gi"}},
			[]shortDisk{{"bar", "2Gi"}, {"foo", "2Gi"}},
			[]shortDisk{{"foo", "2Gi"}},
		},
		{
			"new disk",
			[]shortDisk{{"foo", "1Gi"}},
			[]shortDisk{{"bar", "2Gi"}, {"foo", "1Gi"}},
			[]shortDisk{{"bar", "2Gi"}},
		},
		{
			"size change & new disk",
			[]shortDisk{{"foo", "1Gi"}},
			[]shortDisk{{"bar", "2Gi"}, {"foo", "2Gi"}},
			[]shortDisk{{"foo", "2Gi"}, {"bar", "2Gi"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldPvc := makePvcs(tt.old)
			newPvc := makePvcs(tt.new)
			want := makePvcs(tt.want)
			got := FilterDiskWithSizeChanged(oldPvc, newPvc, logr.Discard())
			if len(want) != len(got) {
				t.Fatalf("Mismatch size: want %v, got %v", len(want), len(got))
			}
			slices.SortFunc(want, func(a, b corev1.PersistentVolumeClaim) int {
				return strings.Compare(a.Name, b.Name)
			})
			slices.SortFunc(got, func(a, b *corev1.PersistentVolumeClaim) int {
				return strings.Compare(a.Name, b.Name)
			})
			for i := range want {
				if want[i].Name != got[i].Name {
					t.Errorf("Mismatch at index %v: want %q got %q", i, want[i].Name, got[i].Name)
				}
				if !want[i].Spec.Resources.Requests.Storage().Equal(*got[i].Spec.Resources.Requests.Storage()) {
					t.Errorf("Mismatch t aindex %v: want %q got %q",
						i,
						want[i].Spec.Resources.Requests.Storage(),
						got[i].Spec.Resources.Requests.Storage())
				}
			}
		})
	}
}

func makePvcs(list []shortDisk) []corev1.PersistentVolumeClaim {
	var ret []corev1.PersistentVolumeClaim
	for _, i := range list {
		ret = append(ret, corev1.PersistentVolumeClaim{
			ObjectMeta: v1.ObjectMeta{
				Name: i.name,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				Resources: corev1.ResourceRequirements{
					Limits: nil,
					Requests: map[corev1.ResourceName]resource.Quantity{
						"storage": resource.MustParse(i.size),
					},
				},
			},
		})
	}

	return ret
}
