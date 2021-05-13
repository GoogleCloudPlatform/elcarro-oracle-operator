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
)

func TestGetPVCNameAndMount(t *testing.T) {

	testCases := []struct {
		Name        string
		DiskName    string
		wantPVCName string
		wantMount   string
	}{
		{
			Name:        "DataDisk",
			DiskName:    "DataDisk",
			wantPVCName: "inst-pvc-u02",
			wantMount:   "u02",
		},
		{
			Name:        "LogDisk",
			DiskName:    "LogDisk",
			wantPVCName: "inst-pvc-u03",
			wantMount:   "u03",
		},
		{
			Name:        "BackupDisk",
			DiskName:    "BackupDisk",
			wantPVCName: "inst-pvc-u04",
			wantMount:   "u04",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			gotPVCName, gotMount := GetPVCNameAndMount("inst", tc.DiskName)

			if gotPVCName != tc.wantPVCName {
				t.Errorf("got pvcName %v, want %v", gotPVCName, tc.wantPVCName)
			}

			if gotMount != tc.wantMount {
				t.Errorf("got pvcName %v, want %v", gotMount, tc.wantMount)
			}
		})
	}
}
