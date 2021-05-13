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
	"fmt"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
)

// DiskSpaceTotal is a helper function to calculate the total amount
// of allocated space across all disks requested for an instance.
func DiskSpaceTotal(inst commonv1alpha1.GenericInstance) (int64, error) {
	spec := inst.GenericInstanceSpec()
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
