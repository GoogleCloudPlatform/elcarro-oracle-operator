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
	"fmt"
	"testing"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
)

func TestTimeRangeIsValid(t *testing.T) {
	var tests = []struct {
		name string
		tr   commonv1alpha1.TimeRange
		want bool
	}{
		{
			name: "valid values",
			tr: commonv1alpha1.TimeRange{
				Start:    &v1.Time{Time: time.Now()},
				Duration: &v1.Duration{Duration: time.Hour},
			},
			want: true,
		},
		{
			name: "missing Duration",
			tr: commonv1alpha1.TimeRange{
				Start: &v1.Time{Time: time.Now()},
			},
			want: false,
		},
		{
			name: "missing Start",
			tr: commonv1alpha1.TimeRange{
				Duration: &v1.Duration{Duration: time.Hour},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		testname := fmt.Sprintf("TestTimeRangeIsValid %s", tt.name)
		t.Run(testname, func(t *testing.T) {
			act := timeRangeIsValid(&tt.tr)
			if act != tt.want {
				t.Errorf("got %v, want %v", act, tt.want)
			}
		})
	}
}

func TestTimeRangeInRange(t *testing.T) {
	now := time.Now()
	startTime := now.Add(-time.Minute)
	duration := 2 * time.Minute
	tr := commonv1alpha1.TimeRange{
		Start:    &v1.Time{Time: startTime},
		Duration: &v1.Duration{Duration: duration},
	}
	var tests = []struct {
		name string
		when time.Time
		want bool
	}{
		{
			name: "start time should be in range",
			when: startTime,
			want: true,
		},
		{
			name: "time in between should be in range",
			when: now,
			want: true,
		},
		{
			name: "time before start should not be in range",
			when: startTime.Add(-duration),
			want: false,
		},
		{
			name: "time after end time should not be in range",
			when: startTime.Add(2 * duration),
			want: false,
		},
		{
			name: "end time should not be in range",
			when: startTime.Add(duration),
			want: false,
		},
	}
	for _, tt := range tests {
		testname := fmt.Sprintf("TestTimeRangeInRange %s", tt.name)
		t.Run(testname, func(t *testing.T) {
			act := timeRangeInRange(&tr, tt.when)
			if act != tt.want {
				t.Errorf("got %v, want %v", act, tt.want)
			}
		})
	}
}

func TestInRange(t *testing.T) {
	n := time.Now()
	s1 := n
	d1 := time.Hour
	e1 := s1.Add(d1)
	s2 := e1.Add(3 * time.Hour)
	d2 := time.Minute
	e2 := s2.Add(d2)
	mw := &commonv1alpha1.MaintenanceWindowSpec{
		TimeRanges: []commonv1alpha1.TimeRange{
			{
				Start:    &v1.Time{Time: s1},
				Duration: &v1.Duration{Duration: d1},
			},
			{
				Start:    &v1.Time{Time: s2},
				Duration: &v1.Duration{Duration: d2},
			},
		},
	}
	var tests = []struct {
		name string
		when time.Time
		want bool
	}{
		{
			name: "time before first range",
			when: s1.Add(-d1),
			want: false,
		},
		{
			name: "time in first range",
			when: s1.Add(d1 / 2),
			want: true,
		},
		{
			name: "time between first & second range",
			when: e1.Add(s2.Sub(e1) / 2),
			want: false,
		},
		{
			name: "time in second range",
			when: s2.Add(d2 / 2),
			want: true,
		},
		{
			name: "time after second range",
			when: e2.Add(d2 / 2),
			want: false,
		},
	}
	for _, tt := range tests {
		testname := fmt.Sprintf("TestInRange %s", tt.name)
		t.Run(testname, func(t *testing.T) {
			act := InRange(mw, tt.when)
			if act != tt.want {
				t.Errorf("got %v, want %v", act, tt.want)
			}
		})
	}
}

func TestHasValidTimeRanges(t *testing.T) {
	validTr1 := commonv1alpha1.TimeRange{
		Start:    &v1.Time{Time: time.Now()},
		Duration: &v1.Duration{Duration: time.Minute * 20},
	}
	validTr2 := commonv1alpha1.TimeRange{
		Start:    &v1.Time{Time: time.Now().Add(time.Hour)},
		Duration: &v1.Duration{Duration: time.Hour},
	}
	invalidTr := commonv1alpha1.TimeRange{
		Start: &v1.Time{Time: time.Now().Add(time.Hour)},
	}

	var tests = []struct {
		name string
		spec *commonv1alpha1.MaintenanceWindowSpec
		want bool
	}{
		{
			name: "nil windows",
			spec: &commonv1alpha1.MaintenanceWindowSpec{TimeRanges: nil},
			want: false,
		},
		{
			name: "no windows",
			spec: &commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{}},
			want: false,
		},
		{
			name: "one valid window",
			spec: &commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{validTr1}},
			want: true,
		},
		{
			name: "two valid window2",
			spec: &commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{validTr1, validTr2}},
			want: true,
		},
		{
			name: "only one invalid window",
			spec: &commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{invalidTr}},
			want: false,
		},
		{
			name: "one invalid and one valid window",
			spec: &commonv1alpha1.MaintenanceWindowSpec{TimeRanges: []commonv1alpha1.TimeRange{validTr1, invalidTr}},
			want: false,
		},
	}
	for _, tt := range tests {
		testname := fmt.Sprintf("TestHasValidTimeRanges %s", tt.name)
		t.Run(testname, func(t *testing.T) {
			act := HasValidTimeRanges(tt.spec)
			if act != tt.want {
				t.Errorf("got %v, want %v", act, tt.want)
			}
		})
	}
}

func TestNextWindow(t *testing.T) {
	n := time.Now()
	s1 := n
	d1 := time.Hour
	e1 := s1.Add(d1)
	s2 := e1.Add(3 * time.Hour)
	d2 := time.Minute
	e2 := s2.Add(d2)
	mw := &commonv1alpha1.MaintenanceWindowSpec{
		TimeRanges: []commonv1alpha1.TimeRange{
			{
				Start:    &v1.Time{Time: s1},
				Duration: &v1.Duration{Duration: d1},
			},
			{
				Start:    &v1.Time{Time: s2},
				Duration: &v1.Duration{Duration: d2},
			},
		},
	}
	var tests = []struct {
		name         string
		when         time.Time
		wantStart    *time.Time
		wantDuration *time.Duration
		wantError    error
	}{
		{
			name:         "time before first range",
			when:         s1.Add(-d1),
			wantStart:    &s1,
			wantDuration: &d1,
			wantError:    nil,
		},
		{
			name:         "at start of first range",
			when:         s1,
			wantStart:    &s1,
			wantDuration: &d1,
			wantError:    nil,
		},
		{
			name:         "in middle of first range",
			when:         s1,
			wantStart:    &s1,
			wantDuration: &d1,
			wantError:    nil,
		},
		{
			name:         "end of first range",
			when:         e1,
			wantStart:    &s2,
			wantDuration: &d2,
			wantError:    nil,
		},
		{
			name:         "between first and second range",
			when:         e1.Add(s2.Sub(e1) / 2),
			wantStart:    &s2,
			wantDuration: &d2,
			wantError:    nil,
		},
		{
			name:         "end of second range",
			when:         e2,
			wantStart:    nil,
			wantDuration: nil,
			wantError:    NoFutureWindows,
		},
	}
	for _, tt := range tests {
		testname := fmt.Sprintf("TestNextWindow %s", tt.name)
		t.Run(testname, func(t *testing.T) {
			aStart, aDuration, aErr := NextWindow(mw, tt.when)
			if aStart != tt.wantStart && aDuration != tt.wantDuration && aErr != tt.wantError {
				t.Errorf("got (%v, %v, %v), want (%v, %v, %v)", aStart, aDuration, aErr, tt.wantStart, tt.wantDuration, tt.wantError)
			}
		})
	}
}
