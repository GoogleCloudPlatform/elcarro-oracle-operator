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

package k8s

import (
	"testing"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
)

func TestUpsertNew(t *testing.T) {
	iStatus := v1alpha1.InstanceStatus{}

	InstanceUpsertCondition(&iStatus, "TestName", v1.ConditionTrue, "reason", "message")

	if len(iStatus.GenericInstanceStatus.Conditions) != 1 {
		t.Errorf("TestUpsertNew")
	}

	if iStatus.GenericInstanceStatus.Conditions[0].Type != "TestName" {
		t.Errorf("TestUpsertNew 2")
	}
}

func TestUpsertOld(t *testing.T) {
	iStatus := v1alpha1.InstanceStatus{
		GenericInstanceStatus: commonv1alpha1.GenericInstanceStatus{
			Conditions: []v1.Condition{
				{
					Type:   "TestName",
					Status: v1.ConditionFalse,
				},
			},
		},
	}

	InstanceUpsertCondition(&iStatus, "TestName", v1.ConditionTrue, "reason", "message")

	if len(iStatus.Conditions) != 1 {
		t.Errorf("TestUpsertNew")
	}

	if iStatus.Conditions[0].Type != "TestName" || iStatus.Conditions[0].Status != v1.ConditionTrue {
		t.Errorf("TestUpsertNew 2")
	}
}

func TestUpsertDoNotDelete(t *testing.T) {
	iStatus := v1alpha1.InstanceStatus{
		GenericInstanceStatus: commonv1alpha1.GenericInstanceStatus{
			Conditions: []v1.Condition{
				{
					Type:   "TestName",
					Status: v1.ConditionFalse,
				},
			},
		},
	}

	InstanceUpsertCondition(&iStatus, "TestName2", v1.ConditionTrue, "reason", "message")

	if len(iStatus.Conditions) != 2 {
		t.Errorf("TestUpsertNew")
	}

	cond := FindCondition(iStatus.Conditions, "TestName2")
	if cond.Type != "TestName2" || cond.Status != v1.ConditionTrue {
		t.Errorf("TestUpsertNew 2")
	}
}

func TestInstanceUpsertCondition(t *testing.T) {
	now := time.Now()
	oldTime := v1.Time{Time: time.Unix(now.Unix()-1000, 0)}
	newTime := v1.Time{Time: now}
	v1Now = func() v1.Time {
		return newTime
	}

	testCases := []struct {
		Name          string
		NewCond       *v1.Condition
		ExistingConds []v1.Condition
		wantNumConds  int
	}{
		{
			Name: "Upsert new to empty list",
			NewCond: &v1.Condition{
				Type:               "NewCond",
				Status:             v1.ConditionTrue,
				Reason:             "NewReason",
				Message:            "NewMessage",
				LastTransitionTime: newTime,
			},
			wantNumConds: 1,
		},
		{
			Name: "Upsert new to non-empty list",
			NewCond: &v1.Condition{
				Type:               "NewCond",
				Status:             v1.ConditionTrue,
				Reason:             "NewReason",
				LastTransitionTime: newTime,
			},
			ExistingConds: []v1.Condition{
				{
					Type:   "OldCond",
					Status: v1.ConditionTrue,
					Reason: "OldReason",
				}},
			wantNumConds: 2,
		},
		{
			Name: "Upsert existing - status transition",
			NewCond: &v1.Condition{
				Type:               "OldCond",
				Status:             v1.ConditionTrue,
				Reason:             "OldReason",
				LastTransitionTime: newTime,
			},
			ExistingConds: []v1.Condition{
				{
					Type:               "OldCond",
					Status:             v1.ConditionFalse,
					Reason:             "OldReason",
					LastTransitionTime: oldTime,
				},
			},
			wantNumConds: 1,
		},
		{
			Name: "Upsert existing - now status transition",
			NewCond: &v1.Condition{
				Type:               "OldCond",
				Status:             v1.ConditionFalse,
				Reason:             "NewReason",
				LastTransitionTime: oldTime,
			},
			ExistingConds: []v1.Condition{
				{
					Type:               "OldCond",
					Status:             v1.ConditionFalse,
					Reason:             "OldReason",
					LastTransitionTime: oldTime,
				},
			},
			wantNumConds: 1,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			iStatus := &v1alpha1.InstanceStatus{
				GenericInstanceStatus: commonv1alpha1.GenericInstanceStatus{
					Conditions: tc.ExistingConds,
				},
			}
			updatedCond := InstanceUpsertCondition(iStatus, tc.NewCond.Type, tc.NewCond.Status, tc.NewCond.Reason, tc.NewCond.Message)

			if tc.wantNumConds != len(iStatus.Conditions) {
				t.Errorf("Wrong number of conditions: got %d, want %d", len(iStatus.Conditions), tc.wantNumConds)
			}

			foundCond := false
			for i, _ := range iStatus.Conditions {
				if updatedCond == &iStatus.Conditions[i] {
					foundCond = true
					break
				}
			}
			if !foundCond {
				t.Errorf("InstanceUpsertCondition did not return a pointer to the actual struct inside the status object - it probably returned a pointer to a copy")
			}

			if tc.NewCond.Type != updatedCond.Type || tc.NewCond.Status != updatedCond.Status || tc.NewCond.Reason != updatedCond.Reason || tc.NewCond.Message != updatedCond.Message || tc.NewCond.LastTransitionTime != updatedCond.LastTransitionTime {
				t.Errorf("Condition not correctly updated: got %+v, want %+v", *updatedCond, *tc.NewCond)
			}
		})
	}
}

func TestUpsert(t *testing.T) {
	now := time.Now()
	oldTime := v1.Time{Time: time.Unix(now.Unix()-1000, 0)}
	newTime := v1.Time{Time: now}
	v1Now = func() v1.Time {
		return newTime
	}

	testCases := []struct {
		Name          string
		NewCond       *v1.Condition
		ExistingConds []v1.Condition
		wantNumConds  int
	}{
		{
			Name: "Upsert new to empty list",
			NewCond: &v1.Condition{
				Type:               "NewCond",
				Status:             v1.ConditionTrue,
				Reason:             "NewReason",
				Message:            "NewMessage",
				LastTransitionTime: newTime,
			},
			wantNumConds: 1,
		},
		{
			Name: "Upsert new to non-empty list",
			NewCond: &v1.Condition{
				Type:               "NewCond",
				Status:             v1.ConditionTrue,
				Reason:             "NewReason",
				LastTransitionTime: newTime,
			},
			ExistingConds: []v1.Condition{
				{
					Type:   "OldCond",
					Status: v1.ConditionTrue,
					Reason: "OldReason",
				}},
			wantNumConds: 2,
		},
		{
			Name: "Upsert existing - status transition",
			NewCond: &v1.Condition{
				Type:               "OldCond",
				Status:             v1.ConditionTrue,
				Reason:             "OldReason",
				LastTransitionTime: newTime,
			},
			ExistingConds: []v1.Condition{
				{
					Type:               "OldCond",
					Status:             v1.ConditionFalse,
					Reason:             "OldReason",
					LastTransitionTime: oldTime,
				},
			},
			wantNumConds: 1,
		},
		{
			Name: "Upsert existing - now status transition",
			NewCond: &v1.Condition{
				Type:               "OldCond",
				Status:             v1.ConditionFalse,
				Reason:             "NewReason",
				LastTransitionTime: oldTime,
			},
			ExistingConds: []v1.Condition{
				{
					Type:               "OldCond",
					Status:             v1.ConditionFalse,
					Reason:             "OldReason",
					LastTransitionTime: oldTime,
				},
			},
			wantNumConds: 1,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			dStatus := &commonv1alpha1.DatabaseStatus{
				Conditions: tc.ExistingConds,
			}
			dStatus.Conditions = Upsert(dStatus.Conditions, tc.NewCond.Type, tc.NewCond.Status, tc.NewCond.Reason, tc.NewCond.Message)

			if tc.wantNumConds != len(dStatus.Conditions) {
				t.Errorf("Wrong number of conditions: got %d, want %d", len(dStatus.Conditions), tc.wantNumConds)
			}

			foundCond := FindCondition(dStatus.Conditions, tc.NewCond.Type)
			if foundCond == nil {
				t.Errorf("New Condition not found in database status conditions after upsert %+v", dStatus.Conditions)
			}

			if tc.NewCond.Type != foundCond.Type || tc.NewCond.Status != foundCond.Status || tc.NewCond.Reason != foundCond.Reason || tc.NewCond.Message != foundCond.Message || tc.NewCond.LastTransitionTime != foundCond.LastTransitionTime {
				t.Errorf("Condition not correctly updated: got %+v, want %+v", foundCond.LastTransitionTime, tc.NewCond.LastTransitionTime)
			}
		})
	}
}

func TestElapsedTimeFromLastTransitionTime(t *testing.T) {
	now, err := time.Parse(time.RFC3339, "2006-01-02T15:04:05Z")
	if err != nil {
		t.Fatalf("Unable to parse time string %v", err)
	}

	oldTime := v1.Time{Time: time.Unix(now.Unix()-1000, 0)}
	newTime := v1.Time{Time: now}
	v1Now = func() v1.Time {
		return newTime
	}

	testCases := []struct {
		Name         string
		Cond         *v1.Condition
		WantDuration time.Duration
	}{
		{
			Name: "Round to seconds",
			Cond: &v1.Condition{
				LastTransitionTime: oldTime,
			},
			WantDuration: 1000000000000,
		},
		{
			Name:         "Handle nil condition",
			Cond:         nil,
			WantDuration: 0,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			gotDuration := ElapsedTimeFromLastTransitionTime(tc.Cond, time.Second)
			if gotDuration != tc.WantDuration {
				t.Errorf("Wrong duration: got %d, want %d", gotDuration, tc.WantDuration)
			}
		})
	}
}
