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

package maintenance

import (
	"errors"
	"time"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
)

// timeRangeInRange returns true iff the specified time lies in the range.
// The range check is inclusive for start-time and exclusive for end-time.
func timeRangeInRange(tr *commonv1alpha1.TimeRange, t time.Time) bool {
	if !timeRangeIsValid(tr) {
		return false
	}

	start := tr.Start.Rfc3339Copy().Time
	if t.Before(start) {
		return false
	}

	end := start.Add(tr.Duration.Duration)

	return end.After(t)
}

// timeRangeIsValid verifies if fields on TimeRange are correctly set.
// In particular, Start and Duration fields should be set.
func timeRangeIsValid(tr *commonv1alpha1.TimeRange) bool {
	return tr != nil && tr.Start != nil && tr.Duration != nil
}

// HasValidTimeRanges validates that there are non-zero time-ranges  and all time-ranges specified are valid.
func HasValidTimeRanges(mw *commonv1alpha1.MaintenanceWindowSpec) bool {
	if mw == nil || len(mw.TimeRanges) == 0 {
		return false
	}

	for _, tr := range mw.TimeRanges {
		if !timeRangeIsValid(&tr) {
			return false
		}
	}

	return true
}

// InRange returns true iff the specified time is in any one of the time ranges.
func InRange(mw *commonv1alpha1.MaintenanceWindowSpec, t time.Time) bool {
	for _, tr := range mw.TimeRanges {
		if timeRangeInRange(&tr, t) {
			return true
		}
	}

	return false
}

// NoFutureWindows error can be used by a caller to detect that
// there are no maintenance windows available.
var NoFutureWindows = errors.New("no future windows")

// NextWindow returns the start time of the current or next maintenance window,
// coupled with the duration of that window.
// If no future windows are available, NoFutureWindows error is returned.
func NextWindow(mw *commonv1alpha1.MaintenanceWindowSpec, t time.Time) (*time.Time, *time.Duration, error) {
	var min *time.Time
	var d *time.Duration
	for _, tr := range mw.TimeRanges {
		if !timeRangeIsValid(&tr) {
			continue
		}

		trStart := tr.Start.Rfc3339Copy().Time
		if t.Before(trStart) {
			if min == nil {
				min = &trStart
				d = &tr.Duration.Duration
			}
			if min.After(trStart) {
				min = &trStart
				d = &tr.Duration.Duration
			}
		}
	}

	if min != nil {
		return min, d, nil
	}

	return nil, nil, NoFutureWindows
}
