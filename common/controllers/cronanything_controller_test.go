/*
Copyright 2018 Google LLC.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cronanything "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
)

const (
	apiVersion      = "resource.k8s.io/v1alpha1"
	kind            = "resource"
	name            = "name"
	namespace       = "namespace"
	defaultCronExpr = "* * * * *"
)

var (
	baseTime = time.Date(2018, time.April, 20, 4, 20, 30, 0, time.UTC)
)

type fakeCronAnything struct {
	runtime.Object
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   cronanything.CronAnythingSpec   `json:"spec,omitempty"`
	Status cronanything.CronAnythingStatus `json:"status,omitempty"`
}

func (in *fakeCronAnything) CronAnythingSpec() *cronanything.CronAnythingSpec {
	return &in.Spec
}

func (in *fakeCronAnything) CronAnythingStatus() *cronanything.CronAnythingStatus {
	return &in.Status
}

func (in *fakeCronAnything) GetObjectKind() schema.ObjectKind {
	return schema.EmptyObjectKind
}

func createReconciler() (*ReconcileCronAnything, *fakeCronAnythingControl, *fakeResourceControl) {
	fakeRecorder := &record.FakeRecorder{}
	fakeCronAnythingControl := &fakeCronAnythingControl{}
	fakeResourceControl := &fakeResourceControl{
		deleteSlice: make([]string, 0),
	}
	fakeResourceResolver := &fakeResourceResolver{}
	return &ReconcileCronAnything{
		Log:                 ctrl.Log.WithName("controllers").WithName("CronAnything"),
		cronanythingControl: fakeCronAnythingControl,
		scheme:              runtime.NewScheme(),
		resourceResolver:    fakeResourceResolver,
		resourceControl:     fakeResourceControl,
		eventRecorder:       fakeRecorder,
		nextTrigger:         make(map[string]time.Time),
		currentTime:         func() time.Time { return baseTime },
	}, fakeCronAnythingControl, fakeResourceControl
}

func newFakeCronAnything(apiVersion, kind, name, namespace string) *fakeCronAnything {
	return &fakeCronAnything{
		ObjectMeta: metav1.ObjectMeta{
			CreationTimestamp: metav1.Time{
				Time: baseTime.Add(-60 * time.Second),
			},
			Name:      name,
			Namespace: namespace,
			UID:       "myUID",
		},
		Spec: cronanything.CronAnythingSpec{
			Schedule: defaultCronExpr,
			Template: runtime.RawExtension{
				Raw: []byte(fmt.Sprintf("{\"apiVersion\": \"%s\", \"kind\": \"%s\"}", apiVersion, kind)),
			},
		},
	}
}

func newUnstructuredResource(ca cronanything.CronAnything, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Resource",
			"metadata": map[string]interface{}{
				"namespace": "default",
				"name":      name,
				"uid":       name,
				"ownerReferences": []interface{}{
					map[string]interface{}{
						"apiVersion": controllerKind.GroupVersion().String(),
						"kind":       controllerKind.Kind,
						"uid":        string(ca.GetUID()),
						"controller": true,
					},
				},
			},
			"status": map[string]interface{}{
				"finishedAt": "",
			},
		},
	}
}

func TestCreateNewResourceOnTrigger(t *testing.T) {
	tVal := true
	fVal := false
	testCases := map[string]struct {
		cascadeDelete *bool
		hasOwnerRef   bool
	}{
		"cascadeDelete is not set, so no owner ref": {
			cascadeDelete: nil,
			hasOwnerRef:   false,
		},
		"cascadeDelete is false, so no owner ref": {
			cascadeDelete: &fVal,
			hasOwnerRef:   false,
		},
		"cascadeDelete is true, so has owner ref": {
			cascadeDelete: &tVal,
			hasOwnerRef:   true,
		},
	}

	for tn, tc := range testCases {
		t.Run(tn, func(t *testing.T) {
			reconciler, fakeCronAnythingControl, fakeResourceControl := createReconciler()

			ca := newFakeCronAnything(apiVersion, kind, name, namespace)
			ca.Spec.CascadeDelete = tc.cascadeDelete

			fakeCronAnythingControl.getCronAnything = ca

			fakeResourceControl.listResult = []*unstructured.Unstructured{
				newUnstructuredResource(ca, "resource1"),
				newUnstructuredResource(ca, "resource2"),
			}

			result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: namespace,
					Name:      name,
				},
			})
			if err != nil {
				t.Error(err)
			}

			if result.RequeueAfter == time.Duration(0) {
				t.Error("Expected result to have RequeueAfter, but it didn't")
			}

			expectedDuration, _ := time.ParseDuration("30s")
			if result.RequeueAfter != expectedDuration {
				t.Errorf("Expected next requeue to be after %f seconds, but it was %f seconds", expectedDuration.Seconds(), result.RequeueAfter.Seconds())
			}

			createTemplate := fakeResourceControl.createTemplate
			if createTemplate.Object["apiVersion"] != apiVersion {
				t.Errorf("Expected apiVersion to be %s, but found %s", apiVersion, createTemplate.Object["apiVersion"])
			}
			if createTemplate.Object["kind"] != kind {
				t.Errorf("Expected kind to be %s, but found %s", kind, createTemplate.Object["kind"])
			}

			if tc.hasOwnerRef && len(createTemplate.GetOwnerReferences()) == 0 {
				t.Errorf("Expected resource to have owner reference, but it didn't")
			}
			if !tc.hasOwnerRef && len(createTemplate.GetOwnerReferences()) == 1 {
				t.Errorf("Expected resource to have no owner reference, but it did")
			}
		})
	}
}

func TestAlreadyDeletedCronAnythingResource(t *testing.T) {
	reconciler, fakeCronAnythingControl, fakeResourceControl := createReconciler()

	ca := newFakeCronAnything(apiVersion, kind, name, namespace)
	ca.DeletionTimestamp = &metav1.Time{
		Time: baseTime,
	}
	ca.Status.LastScheduleTime = &metav1.Time{
		Time: baseTime.Add(-10 * time.Hour),
	}
	fakeCronAnythingControl.getCronAnything = ca

	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		},
	})
	if err != nil {
		t.Error(err)
	}

	if result.RequeueAfter != time.Duration(0) || result.Requeue != false {
		t.Errorf("Expected request to not be reqeueued, but found %v", result)
	}

	if fakeResourceControl.createTemplate != nil {
		t.Errorf("Expected no new resource to have been created")
	}
}

func TestSuspended(t *testing.T) {
	reconciler, fakeCronAnythingControl, fakeResourceControl := createReconciler()

	ca := newFakeCronAnything(apiVersion, kind, name, namespace)
	T := true
	ca.Spec.Suspend = &T
	ca.Status.LastScheduleTime = &metav1.Time{
		Time: baseTime.Add(-10 * time.Hour),
	}
	fakeCronAnythingControl.getCronAnything = ca

	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		},
	})
	if err != nil {
		t.Error(err)
	}

	if result.RequeueAfter != time.Duration(0) || result.Requeue != false {
		t.Errorf("Expected request to not be reqeueued, but found %v", result)
	}

	if fakeResourceControl.createTemplate != nil {
		t.Errorf("Expected no new resource to have been created")
	}
}

func TestScheduleTrigger(t *testing.T) {
	testCases := map[string]struct {
		currentTime      time.Time
		lastScheduleTime time.Time
		schedule         string

		expectCreate bool
	}{
		"single past deadline": {
			baseTime,
			baseTime.Add(-1 * time.Minute),
			defaultCronExpr,
			true,
		},
		"multiple past deadline": {
			baseTime,
			baseTime.Add(-1 * time.Hour),
			defaultCronExpr,
			true,
		},
		"none past deadline": {
			baseTime,
			baseTime.Add(-1 * time.Hour),
			"0 0 ? 12 ?",
			false,
		},
	}

	for tn, tc := range testCases {
		t.Run(tn, func(t *testing.T) {
			reconciler, fakeCronAnythingControl, fakeResourceControl := createReconciler()

			ca := newFakeCronAnything(apiVersion, kind, namespace, name)
			ca.Status.LastScheduleTime = &metav1.Time{
				Time: tc.lastScheduleTime,
			}
			ca.Spec.Schedule = tc.schedule

			fakeCronAnythingControl.getCronAnything = ca

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: namespace,
					Name:      name,
				},
			})
			if err != nil {
				t.Error(err)
			}

			didCreate := fakeResourceControl.createTemplate != nil
			if didCreate != tc.expectCreate {
				t.Errorf("Expected create: %t, did create: %t", tc.expectCreate, didCreate)
			}
		})
	}
}

func TestTriggerDeadline(t *testing.T) {
	reconciler, fakeCronAnythingControl, fakeResourceControl := createReconciler()

	ca := newFakeCronAnything(apiVersion, kind, namespace, name)
	ca.Status.LastScheduleTime = &metav1.Time{
		Time: baseTime.Add(-60 * time.Second),
	}
	twenty := int64(20)
	ca.Spec.TriggerDeadlineSeconds = &twenty
	fakeCronAnythingControl.getCronAnything = ca

	result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		},
	})
	if err != nil {
		t.Error(err)
	}

	if result.RequeueAfter == time.Duration(0) {
		t.Errorf("Expected request to be reqeueued, but found %v", result)
	}

	if fakeResourceControl.createCount != 0 {
		t.Errorf("Expected no resource to be created, but found one")
	}
}

func TestIsFinished(t *testing.T) {
	timestampFieldPath := "{.status.myCustomField}"
	stringFieldPath := "{.status.myCustomPhase}"
	testCases := map[string]struct {
		ca       *fakeCronAnything
		resource *unstructured.Unstructured
		result   bool
	}{
		"no strategy defined means not finished": {
			&fakeCronAnything{},
			&unstructured.Unstructured{},
			false,
		},
		"timestamp strategy, custom correct timestamp": {
			&fakeCronAnything{
				Spec: cronanything.CronAnythingSpec{
					FinishableStrategy: &cronanything.FinishableStrategy{
						Type: cronanything.FinishableStrategyTimestampField,
						TimestampField: &cronanything.TimestampFieldStrategy{
							FieldPath: timestampFieldPath,
						},
					},
				},
			},
			&unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"myCustomField": baseTime.Format(time.RFC3339),
					},
				},
			},
			true,
		},
		"timestamp strategy, custom incorrect timestamp": {
			&fakeCronAnything{
				Spec: cronanything.CronAnythingSpec{
					FinishableStrategy: &cronanything.FinishableStrategy{
						Type: cronanything.FinishableStrategyTimestampField,
						TimestampField: &cronanything.TimestampFieldStrategy{
							FieldPath: timestampFieldPath,
						},
					},
				},
			},
			&unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"myCustomField": "incorrectTimestamp",
					},
				},
			},
			false,
		},
		"timeestamp strategy, custom field does not exist": {
			&fakeCronAnything{
				Spec: cronanything.CronAnythingSpec{
					FinishableStrategy: &cronanything.FinishableStrategy{
						Type: cronanything.FinishableStrategyTimestampField,
						TimestampField: &cronanything.TimestampFieldStrategy{
							FieldPath: timestampFieldPath,
						},
					},
				},
			},
			&unstructured.Unstructured{
				Object: map[string]interface{}{},
			},
			false,
		},
		"timestamp strategy, custom advanced jsonpath expression": {
			ca: &fakeCronAnything{
				Spec: cronanything.CronAnythingSpec{
					FinishableStrategy: &cronanything.FinishableStrategy{
						Type: cronanything.FinishableStrategyTimestampField,
						TimestampField: &cronanything.TimestampFieldStrategy{
							FieldPath: `{.status.conditions[?(@.reason=="PodCompleted")].lastTransitionTime}`,
						},
					},
				},
			},
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"conditions": []interface{}{
							map[string]interface{}{
								"lastTransitionTime": "2018-08-23T22:42:51Z",
								"status":             "True",
								"type":               "Initialized",
							},
							map[string]interface{}{
								"lastTransitionTime": "2018-08-23T22:43:06Z",
								"status":             "True",
								"type":               "Ready",
							},
							map[string]interface{}{
								"lastTransitionTime": "2018-08-23T22:42:51Z",
								"status":             "True",
								"type":               "PodScheduled",
							},
							map[string]interface{}{
								"lastTransitionTime": "2018-09-19T19:26:00Z",
								"reason":             "PodCompleted",
								"status":             "True",
								"type":               "Initialized",
							},
						},
					},
				},
			},
			result: true,
		},
		"string strategy, not finished": {
			&fakeCronAnything{
				Spec: cronanything.CronAnythingSpec{
					FinishableStrategy: &cronanything.FinishableStrategy{
						Type: cronanything.FinishableStrategyStringField,
						StringField: &cronanything.StringFieldStrategy{
							FieldPath:      stringFieldPath,
							FinishedValues: []string{"Finished", "Failed"},
						},
					},
				},
			},
			&unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"myCustomPhase": "Running",
					},
				},
			},
			false,
		},
		"string strategy, finished": {
			&fakeCronAnything{
				Spec: cronanything.CronAnythingSpec{
					FinishableStrategy: &cronanything.FinishableStrategy{
						Type: cronanything.FinishableStrategyStringField,
						StringField: &cronanything.StringFieldStrategy{
							FieldPath:      stringFieldPath,
							FinishedValues: []string{"Finished", "Failed"},
						},
					},
				},
			},
			&unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"myCustomPhase": "Finished",
					},
				},
			},
			true,
		},
	}

	for tn, tc := range testCases {
		t.Run(tn, func(t *testing.T) {
			res, _ := isFinished(tc.ca, tc.resource)
			if res != tc.result {
				t.Errorf("Expected %t, but got %t", tc.result, res)
			}
		})
	}
}

func TestConcurrencyPolicy(t *testing.T) {
	testCases := map[string]struct {
		concurrencyPolicy             cronanything.ConcurrencyPolicy
		existingActiveResourceCount   int
		existingFinishedResourceCount int
		expectedDeleteCount           int
		expectedCreateCount           int
	}{
		"allow concurrent, existing resource": {
			cronanything.AllowConcurrent,
			1,
			1,
			0,
			1,
		},
		"allow concurrent, no existing resources": {
			cronanything.AllowConcurrent,
			0,
			0,
			0,
			1,
		},
		"forbid concurrent, existing active resource": {
			cronanything.ForbidConcurrent,
			1,
			0,
			0,
			0,
		},
		"forbid concurrent, existing finished resource": {
			cronanything.ForbidConcurrent,
			0,
			1,
			0,
			1,
		},
		"forbid concurrent, no existing resources": {
			cronanything.ForbidConcurrent,
			0,
			0,
			0,
			1,
		},
		"replace concurrent, existing active resource": {
			cronanything.ReplaceConcurrent,
			1,
			0,
			1,
			0,
		},
		"replace concurrent, existing finished resource": {
			cronanything.ReplaceConcurrent,
			0,
			2,
			0,
			1,
		},
		"replace concurrent, no existing resources": {
			cronanything.ReplaceConcurrent,
			0,
			0,
			0,
			1,
		},
		"replace concurrent, multiple active resources": {
			cronanything.ReplaceConcurrent,
			5,
			2,
			5,
			0,
		},
	}

	for tn, tc := range testCases {
		t.Run(tn, func(t *testing.T) {
			reconciler, fakeCronAnythingControl, fakeResourceControl := createReconciler()

			ca := newFakeCronAnything(apiVersion, kind, namespace, name)
			ca.Spec.ConcurrencyPolicy = tc.concurrencyPolicy
			ca.Spec.FinishableStrategy = &cronanything.FinishableStrategy{
				Type: cronanything.FinishableStrategyTimestampField,
				TimestampField: &cronanything.TimestampFieldStrategy{
					FieldPath: `{.status.finishedAt}`,
				},
			}

			fakeCronAnythingControl.getCronAnything = ca

			var resourceListResult []*unstructured.Unstructured
			for i := 0; i < tc.existingActiveResourceCount; i++ {
				resourceListResult = append(resourceListResult, newUnstructuredResource(ca, fmt.Sprintf("activeResource%d", i)))
			}
			for i := 0; i < tc.existingFinishedResourceCount; i++ {
				resource := newUnstructuredResource(ca, fmt.Sprintf("finishedResource%d", i))
				unstructured.SetNestedField(resource.Object, baseTime.Format(time.RFC3339), "status", "finishedAt")
				resourceListResult = append(resourceListResult, resource)
			}
			fakeResourceControl.listResult = resourceListResult

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: namespace,
					Name:      name,
				},
			})
			if err != nil {
				t.Error(err)
			}

			if len(fakeResourceControl.deleteSlice) != tc.expectedDeleteCount {
				t.Errorf("Expected %d deletes, but found %d", tc.expectedDeleteCount, len(fakeResourceControl.deleteSlice))
			}

			if fakeResourceControl.createCount != tc.expectedCreateCount {
				t.Error("Expected resource to be created, but it wasn't")
			}
		})
	}
}

func TestReplaceConcurrentIgnoreResourcesMarkedForDeletion(t *testing.T) {
	reconciler, fakeCronAnythingControl, fakeResourceControl := createReconciler()

	ca := newFakeCronAnything(apiVersion, kind, namespace, name)
	ca.Spec.ConcurrencyPolicy = cronanything.ReplaceConcurrent
	fakeCronAnythingControl.getCronAnything = ca

	resource := newUnstructuredResource(ca, "resource")
	unstructured.SetNestedField(resource.Object, baseTime.Format(time.RFC3339), "metadata", "deletionTimestamp")
	fakeResourceControl.listResult = []*unstructured.Unstructured{resource}

	_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		},
	})
	if err != nil {
		t.Error(err)
	}

	if len(fakeResourceControl.deleteSlice) != 0 {
		t.Errorf("Expected no delete, but found one")
	}
}

func TestCleanupHistory(t *testing.T) {
	testCases := map[string]struct {
		useHistoryCountLimit       bool
		historyCountLimit          int32
		useHistoryTimeLimitSeconds bool
		historyTimeLimitSeconds    uint64
		existingActiveResources    []string
		existingFinishedResources  map[string]time.Time
		deletedResources           []string
	}{
		"no active and no finished resourced": {
			true,
			3,
			true,
			60,
			[]string{},
			map[string]time.Time{},
			[]string{},
		},
		"historyCountLimit and finished resources": {
			true,
			2,
			false,
			0,
			[]string{"ar1", "ar2", "ar3"},
			map[string]time.Time{
				"fr1": baseTime.Add(-10 * time.Hour),
				"fr2": baseTime.Add(-9 * time.Hour),
				"fr3": baseTime.Add(-8 * time.Hour),
				"fr4": baseTime.Add(-7 * time.Hour),
				"fr5": baseTime.Add(-6 * time.Hour),
			},
			[]string{"fr1", "fr2", "fr3"},
		},
		"historyCountLimit and fewer finished resources": {
			true,
			5,
			false,
			0,
			[]string{"ar1", "ar2", "ar3"},
			map[string]time.Time{
				"fr1": baseTime.Add(-10 * time.Hour),
				"fr2": baseTime.Add(-9 * time.Hour),
				"fr3": baseTime.Add(-8 * time.Hour),
				"fr4": baseTime.Add(-7 * time.Hour),
				"fr5": baseTime.Add(-6 * time.Hour),
			},
			[]string{},
		},
		"historyTimeLimit and finished resources": {
			false,
			0,
			true,
			(6 * 60) + 30,
			[]string{"ar1", "ar2", "ar3"},
			map[string]time.Time{
				"fr1": baseTime.Add(-10 * time.Minute),
				"fr2": baseTime.Add(-9 * time.Minute),
				"fr3": baseTime.Add(-8 * time.Minute),
				"fr4": baseTime.Add(-7 * time.Minute),
				"fr5": baseTime.Add(-6 * time.Minute),
			},
			[]string{"fr1", "fr2", "fr3", "fr4"},
		},
		"historyCountLimit and no finished resources old enough": {
			false,
			0,
			true,
			12 * 60,
			[]string{"ar1", "ar2", "ar3"},
			map[string]time.Time{
				"fr1": baseTime.Add(-10 * time.Minute),
				"fr2": baseTime.Add(-9 * time.Minute),
				"fr3": baseTime.Add(-8 * time.Minute),
				"fr4": baseTime.Add(-7 * time.Minute),
				"fr5": baseTime.Add(-6 * time.Minute),
			},
			[]string{},
		},
		"no historyCountLimit or historyTimeLimit should not delete any": {
			false,
			0,
			false,
			0,
			[]string{"ar1", "ar2", "ar3"},
			map[string]time.Time{
				"fr1": baseTime.Add(-10 * time.Second),
				"fr2": baseTime.Add(-9 * time.Second),
				"fr3": baseTime.Add(-8 * time.Second),
				"fr4": baseTime.Add(-7 * time.Second),
				"fr5": baseTime.Add(-6 * time.Second),
			},
			[]string{},
		},
		"both historyCountLimit and historyTimeLimit, many old resources": {
			true,
			6,
			true,
			3 * 60,
			[]string{"ar1", "ar2", "ar3"},
			map[string]time.Time{
				"fr1": baseTime.Add(-10 * time.Minute),
				"fr2": baseTime.Add(-9 * time.Minute),
				"fr3": baseTime.Add(-8 * time.Minute),
				"fr4": baseTime.Add(-7 * time.Minute),
				"fr5": baseTime.Add(-6 * time.Minute),
			},
			[]string{"fr1", "fr2", "fr3", "fr4", "fr5"},
		},
		"both historyCountLimit and historyTimeLimit, many new resources": {
			true,
			1,
			true,
			3 * 60,
			[]string{"ar1", "ar2", "ar3"},
			map[string]time.Time{
				"fr1": baseTime.Add(-10 * time.Second),
				"fr2": baseTime.Add(-9 * time.Second),
				"fr3": baseTime.Add(-8 * time.Second),
				"fr4": baseTime.Add(-7 * time.Second),
				"fr5": baseTime.Add(-6 * time.Second),
			},
			[]string{"fr1", "fr2", "fr3", "fr4"},
		},
	}

	for tn, tc := range testCases {
		t.Run(tn, func(t *testing.T) {
			reconciler, fakeCronAnythingControl, fakeResourceControl := createReconciler()

			ca := newFakeCronAnything(apiVersion, kind, namespace, name)
			ca.Spec.FinishableStrategy = &cronanything.FinishableStrategy{
				Type: cronanything.FinishableStrategyTimestampField,
				TimestampField: &cronanything.TimestampFieldStrategy{
					FieldPath: `{.status.finishedAt}`,
				},
			}
			ca.Spec.Retention = &cronanything.ResourceRetention{
				ResourceTimestampStrategy: cronanything.ResourceTimestampStrategy{
					Type: cronanything.ResourceTimestampStrategyField,
					FieldResourceTimestampStrategy: &cronanything.FieldResourceTimestampStrategy{
						FieldPath: `{.status.finishedAt}`,
					},
				},
			}
			if tc.useHistoryCountLimit {
				ca.Spec.Retention.HistoryCountLimit = &tc.historyCountLimit
			}
			if tc.useHistoryTimeLimitSeconds {
				ca.Spec.Retention.HistoryTimeLimitSeconds = &tc.historyTimeLimitSeconds
			}
			T := true
			ca.Spec.Suspend = &T
			fakeCronAnythingControl.getCronAnything = ca

			var resourceListResult []*unstructured.Unstructured
			for _, n := range tc.existingActiveResources {
				resourceListResult = append(resourceListResult, newUnstructuredResource(ca, n))
			}
			for n, finishedTimestamp := range tc.existingFinishedResources {
				resource := newUnstructuredResource(ca, n)
				unstructured.SetNestedField(resource.Object, finishedTimestamp.Format(time.RFC3339), "status", "finishedAt")
				resourceListResult = append(resourceListResult, resource)
			}
			fakeResourceControl.listResult = resourceListResult

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: namespace,
					Name:      name,
				},
			})
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			sort.Strings(tc.deletedResources)
			sort.Strings(fakeResourceControl.deleteSlice)
			if !reflect.DeepEqual(tc.deletedResources, fakeResourceControl.deleteSlice) {
				t.Errorf("Expected deletion of resources %s, but found %s", strings.Join(tc.deletedResources, ","), strings.Join(fakeResourceControl.deleteSlice, ","))
			}
		})
	}
}

func TestTotalResourceLimit(t *testing.T) {
	testCases := map[string]struct {
		totalResourceLimit            int32
		historyLimit                  int32
		existingActiveResourceCount   int
		existingFinishedResourceCount int
		created                       bool
	}{
		"total number under totalResourceLimit": {
			5,
			3,
			2,
			2,
			true,
		},
		"active over totalResourceLimit": {
			5,
			3,
			6,
			0,
			false,
		},
		"active at totalResourceLimit and some finished": {
			5,
			3,
			5,
			3,
			false,
		},
		"active plus finished inside limit over totalResourceLimit": {
			5,
			3,
			3,
			3,
			false,
		},
		"active and finished over totalResourceLimit, but finished over historyLimit": {
			5,
			1,
			2,
			10,
			true,
		},
		"no active and no finished resources": {
			1,
			4,
			0,
			0,
			true,
		},
		"totalResourceLimit is zero": {
			0,
			4,
			0,
			0,
			false,
		},
	}

	for tn, tc := range testCases {
		t.Run(tn, func(t *testing.T) {
			reconciler, fakeCronAnythingControl, fakeResourceControl := createReconciler()

			ca := newFakeCronAnything(apiVersion, kind, namespace, name)
			ca.Spec.TotalResourceLimit = &tc.totalResourceLimit
			ca.Spec.FinishableStrategy = &cronanything.FinishableStrategy{
				Type: cronanything.FinishableStrategyTimestampField,
				TimestampField: &cronanything.TimestampFieldStrategy{
					FieldPath: `{.status.finishedAt}`,
				},
			}
			ca.Spec.Retention = &cronanything.ResourceRetention{
				ResourceTimestampStrategy: cronanything.ResourceTimestampStrategy{
					Type: cronanything.ResourceTimestampStrategyField,
					FieldResourceTimestampStrategy: &cronanything.FieldResourceTimestampStrategy{
						FieldPath: `{.status.finishedAt}`,
					},
				},
			}
			ca.Spec.Retention.HistoryCountLimit = &tc.historyLimit

			fakeCronAnythingControl.getCronAnything = ca

			var resourceListResult []*unstructured.Unstructured
			for i := 0; i < tc.existingActiveResourceCount; i++ {
				resourceListResult = append(resourceListResult, newUnstructuredResource(ca, fmt.Sprintf("activeResource%d", i)))
			}
			for i := 0; i < tc.existingFinishedResourceCount; i++ {
				resource := newUnstructuredResource(ca, fmt.Sprintf("finishedResource%d", i))
				unstructured.SetNestedField(resource.Object, baseTime.Format(time.RFC3339), "status", "finishedAt")
				resourceListResult = append(resourceListResult, resource)
			}
			fakeResourceControl.listResult = resourceListResult

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: namespace,
					Name:      name,
				},
			})
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			didCreate := fakeResourceControl.createCount > 0
			if didCreate != tc.created {
				t.Errorf("Expected %t, but found %t", tc.created, didCreate)
			}
		})
	}
}

func TestCreateResourceAndUpdateCronAnything(t *testing.T) {
	reconciler, fakeCronAnythingControl, fakeResourceControl := createReconciler()

	resourceTemplate := map[string]interface{}{
		"apiVersion": apiVersion,
		"kind":       kind,
		"spec": map[string]interface{}{
			"greeting":   "Hello World!",
			"greetCount": 42,
		},
	}
	resourceTemplateBytes, _ := json.Marshal(resourceTemplate)

	ca := &fakeCronAnything{
		ObjectMeta: metav1.ObjectMeta{
			CreationTimestamp: metav1.Time{
				Time: baseTime.Add(-60 * time.Second),
			},
			Name: "myCronAnything",
			UID:  "myUID",
		},
		Spec: cronanything.CronAnythingSpec{
			Schedule: defaultCronExpr,
			Template: runtime.RawExtension{
				Raw: resourceTemplateBytes,
			},
		},
	}
	fakeCronAnythingControl.getCronAnything = ca

	times, _, _ := getScheduleTimes(ca, baseTime)
	expectedTriggerTime := times[len(times)-1]

	_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		},
	})
	if err != nil {
		t.Errorf("%s: Unexpected error: %v", name, err)
	}

	createdResourceTemplate := fakeResourceControl.createTemplate.Object
	if !reflect.DeepEqual(createdResourceTemplate, fakeResourceControl.createTemplate.Object) {
		t.Error("Expected template and created resource to be equal, but they weren't")
	}

	lastScheduleTime := fakeCronAnythingControl.updateCronAnything.CronAnythingStatus().LastScheduleTime.Time
	if expectedTriggerTime != lastScheduleTime {
		t.Errorf("Expected %s, but got %s", expectedTriggerTime.Format(time.RFC3339), lastScheduleTime.Format(time.RFC3339))
	}
}

func TestGetResourceName(t *testing.T) {
	timestamp := toTimestamp(t, "2012-11-01T22:08:41+00:00")

	testCases := map[string]struct {
		cronAnything cronanything.CronAnything
		scheduleTime time.Time
		expectedName string
	}{
		"default name, default timestamp format": {
			cronAnything: newCronAnythingForResourceName("myresource", nil, nil),
			scheduleTime: timestamp,
			expectedName: "myresource-1351807721",
		},
		"custom name, default timestamp format": {
			cronAnything: newCronAnythingForResourceName("myresource", toPointer("anotherResource"), nil),
			scheduleTime: timestamp,
			expectedName: "anotherResource-1351807721",
		},
		"default name, custom timestamp format": {
			cronAnything: newCronAnythingForResourceName("myresource", nil, toPointer("20060102150405")),
			scheduleTime: timestamp,
			expectedName: "myresource-20121101220841",
		},
		"custom name, custom timestamp format": {
			cronAnything: newCronAnythingForResourceName("myresource", toPointer("anotherResource"), toPointer("2006-01-02-15-04-05")),
			scheduleTime: timestamp,
			expectedName: "anotherResource-2012-11-01-22-08-41",
		},
	}

	for tn, tc := range testCases {
		t.Run(tn, func(t *testing.T) {
			name := getResourceName(tc.cronAnything, tc.scheduleTime)
			if tc.expectedName != name {
				t.Errorf("expected %s, but got %s", tc.expectedName, name)
			}
		})
	}
}

func TestTriggerHistory(t *testing.T) {
	testCases := map[string]struct {
		CurrentTime            time.Time
		TriggerDeadline        *int64
		ConcurrencyPolicy      cronanything.ConcurrencyPolicy
		ExistingResourceCount  int
		CurrentStatus          cronanything.CronAnythingStatus
		CreateResourceError    error
		ReconcileFails         bool
		ExpectedTriggerHistory []cronanything.TriggerHistoryRecord
		ExpectedPendingTrigger *cronanything.PendingTrigger
	}{
		"Successful create added to history": {
			CurrentTime: time.Date(2018, time.April, 20, 4, 20, 01, 0, time.UTC),
			CurrentStatus: cronanything.CronAnythingStatus{
				LastScheduleTime: getMetaTimePointer(time.Date(2018, time.April, 20, 4, 19, 0, 0, time.UTC)),
			},
			ExpectedTriggerHistory: []cronanything.TriggerHistoryRecord{
				{
					ScheduleTime:      metav1.NewTime(time.Date(2018, time.April, 20, 4, 20, 0, 0, time.UTC)),
					CreationTimestamp: metav1.NewTime(time.Date(2018, time.April, 20, 4, 20, 01, 0, time.UTC)),
					Result:            cronanything.TriggerResultCreateSucceeded,
				},
			},
		},
		"Create fails first time": {
			CurrentTime: time.Date(2018, time.April, 20, 4, 20, 01, 0, time.UTC),
			CurrentStatus: cronanything.CronAnythingStatus{
				LastScheduleTime: getMetaTimePointer(time.Date(2018, time.April, 20, 4, 19, 0, 0, time.UTC)),
			},
			CreateResourceError: errors.New("this is a test error"),
			ReconcileFails:      true,
			ExpectedPendingTrigger: &cronanything.PendingTrigger{
				ScheduleTime: metav1.NewTime(time.Date(2018, time.April, 20, 4, 20, 0, 0, time.UTC)),
				Result:       cronanything.TriggerResultCreateFailed,
			},
		},
		"Create fails second time": {
			CurrentTime: time.Date(2018, time.April, 20, 4, 20, 05, 0, time.UTC),
			CurrentStatus: cronanything.CronAnythingStatus{
				LastScheduleTime: getMetaTimePointer(time.Date(2018, time.April, 20, 4, 19, 0, 0, time.UTC)),
				PendingTrigger: &cronanything.PendingTrigger{
					ScheduleTime: metav1.NewTime(time.Date(2018, time.April, 20, 4, 20, 00, 0, time.UTC)),
					Result:       cronanything.TriggerResultCreateFailed,
				},
			},
			CreateResourceError: errors.New("this is a test error"),
			ReconcileFails:      true,
			ExpectedPendingTrigger: &cronanything.PendingTrigger{
				ScheduleTime: metav1.NewTime(time.Date(2018, time.April, 20, 4, 20, 0, 0, time.UTC)),
				Result:       cronanything.TriggerResultCreateFailed,
			},
		},
		"New trigger time reached while create fails": {
			CurrentTime: time.Date(2018, time.April, 20, 4, 21, 01, 0, time.UTC),
			CurrentStatus: cronanything.CronAnythingStatus{
				LastScheduleTime: getMetaTimePointer(time.Date(2018, time.April, 20, 4, 19, 0, 0, time.UTC)),
				PendingTrigger: &cronanything.PendingTrigger{
					ScheduleTime: metav1.NewTime(time.Date(2018, time.April, 20, 4, 20, 00, 0, time.UTC)),
					Result:       cronanything.TriggerResultCreateFailed,
				},
			},
			CreateResourceError: errors.New("this is a test error"),
			ReconcileFails:      true,
			ExpectedTriggerHistory: []cronanything.TriggerHistoryRecord{
				{
					ScheduleTime:      metav1.NewTime(time.Date(2018, time.April, 20, 4, 20, 0, 0, time.UTC)),
					CreationTimestamp: metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 01, 0, time.UTC)),
					Result:            cronanything.TriggerResultCreateFailed,
				},
			},
			ExpectedPendingTrigger: &cronanything.PendingTrigger{
				ScheduleTime: metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 0, 0, time.UTC)),
				Result:       cronanything.TriggerResultCreateFailed,
			},
		},
		"New trigger time reached with create failed and new succeeds": {
			CurrentTime: time.Date(2018, time.April, 20, 4, 21, 01, 0, time.UTC),
			CurrentStatus: cronanything.CronAnythingStatus{
				LastScheduleTime: getMetaTimePointer(time.Date(2018, time.April, 20, 4, 19, 0, 0, time.UTC)),
				PendingTrigger: &cronanything.PendingTrigger{
					ScheduleTime: metav1.NewTime(time.Date(2018, time.April, 20, 4, 20, 00, 0, time.UTC)),
					Result:       cronanything.TriggerResultCreateFailed,
				},
			},
			ExpectedTriggerHistory: []cronanything.TriggerHistoryRecord{
				{
					ScheduleTime:      metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 0, 0, time.UTC)),
					CreationTimestamp: metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 01, 0, time.UTC)),
					Result:            cronanything.TriggerResultCreateSucceeded,
				},
				{
					ScheduleTime:      metav1.NewTime(time.Date(2018, time.April, 20, 4, 20, 0, 0, time.UTC)),
					CreationTimestamp: metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 01, 0, time.UTC)),
					Result:            cronanything.TriggerResultCreateFailed,
				},
			},
		},
		"Multiple triggers missed with no pending trigger": {
			CurrentTime: time.Date(2018, time.April, 20, 4, 21, 01, 0, time.UTC),
			CurrentStatus: cronanything.CronAnythingStatus{
				LastScheduleTime: getMetaTimePointer(time.Date(2018, time.April, 20, 4, 10, 0, 0, time.UTC)),
			},
			ExpectedTriggerHistory: []cronanything.TriggerHistoryRecord{
				{
					ScheduleTime:      metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 0, 0, time.UTC)),
					CreationTimestamp: metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 01, 0, time.UTC)),
					Result:            cronanything.TriggerResultCreateSucceeded,
				},
				{
					ScheduleTime:      metav1.NewTime(time.Date(2018, time.April, 20, 4, 20, 0, 0, time.UTC)),
					CreationTimestamp: metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 01, 0, time.UTC)),
					Result:            cronanything.TriggerResultMissed,
				},
			},
		},
		"Trigger deadline exceeded without pending trigger": {
			CurrentTime:     time.Date(2018, time.April, 20, 4, 21, 31, 0, time.UTC),
			TriggerDeadline: func(n int64) *int64 { return &n }(30),
			CurrentStatus: cronanything.CronAnythingStatus{
				LastScheduleTime: getMetaTimePointer(time.Date(2018, time.April, 20, 4, 20, 0, 0, time.UTC)),
			},
			ExpectedTriggerHistory: []cronanything.TriggerHistoryRecord{
				{
					ScheduleTime:      metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 0, 0, time.UTC)),
					CreationTimestamp: metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 31, 0, time.UTC)),
					Result:            cronanything.TriggerResultDeadlineExceeded,
				},
			},
		},
		"Trigger deadline exceeded with pending trigger": {
			CurrentTime:     time.Date(2018, time.April, 20, 4, 21, 31, 0, time.UTC),
			TriggerDeadline: func(n int64) *int64 { return &n }(30),
			CurrentStatus: cronanything.CronAnythingStatus{
				LastScheduleTime: getMetaTimePointer(time.Date(2018, time.April, 20, 4, 20, 0, 0, time.UTC)),
				PendingTrigger: &cronanything.PendingTrigger{
					ScheduleTime: metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 0, 0, time.UTC)),
					Result:       cronanything.TriggerResultCreateFailed,
				},
			},
			ExpectedTriggerHistory: []cronanything.TriggerHistoryRecord{
				{
					ScheduleTime:      metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 0, 0, time.UTC)),
					CreationTimestamp: metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 31, 0, time.UTC)),
					Result:            cronanything.TriggerResultCreateFailed,
				},
			},
		},
		"Forbid concurrent policy with existing unfinished resource": {
			CurrentTime:       time.Date(2018, time.April, 20, 4, 21, 1, 0, time.UTC),
			ConcurrencyPolicy: cronanything.ForbidConcurrent,
			CurrentStatus: cronanything.CronAnythingStatus{
				LastScheduleTime: getMetaTimePointer(time.Date(2018, time.April, 20, 4, 20, 0, 0, time.UTC)),
			},
			ExistingResourceCount: 1,
			ExpectedTriggerHistory: []cronanything.TriggerHistoryRecord{
				{
					ScheduleTime:      metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 0, 0, time.UTC)),
					CreationTimestamp: metav1.NewTime(time.Date(2018, time.April, 20, 4, 21, 01, 0, time.UTC)),
					Result:            cronanything.TriggerResultForbidConcurrent,
				},
			},
		},
	}

	for tn, tc := range testCases {
		t.Run(tn, func(t *testing.T) {
			reconciler, fakeCronAnythingControl, fakeResourceControl := createReconciler()
			reconciler.currentTime = func() time.Time { return tc.CurrentTime }

			ca := newFakeCronAnything(apiVersion, kind, namespace, name)
			ca.CreationTimestamp = metav1.NewTime(tc.CurrentTime.Add(-time.Hour))
			ca.Spec.Schedule = defaultCronExpr
			ca.Spec.TriggerDeadlineSeconds = tc.TriggerDeadline
			ca.Spec.ConcurrencyPolicy = tc.ConcurrencyPolicy
			ca.Status = tc.CurrentStatus

			fakeCronAnythingControl.getCronAnything = ca

			fakeResourceControl.listResult = createUnstructuredSlice(ca, tc.ExistingResourceCount)
			fakeResourceControl.createError = tc.CreateResourceError

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: namespace,
					Name:      name,
				},
			})
			if !tc.ReconcileFails && err != nil {
				t.Errorf("Expected reconcile to succeed, but it failed with error %v", err)
			}
			if tc.ReconcileFails && err == nil {
				t.Errorf("Expected reconcile to fail, but it didn't")
			}

			status := fakeCronAnythingControl.updateCronAnything.CronAnythingStatus()
			if !isTriggerHistoriesEqual(status.TriggerHistory, tc.ExpectedTriggerHistory) {
				t.Errorf("Expected %v in trigger history, but found %v", tc.ExpectedTriggerHistory, status.TriggerHistory)
			}

			if !reflect.DeepEqual(status.PendingTrigger, tc.ExpectedPendingTrigger) {
				t.Errorf("Expected %v as pending trigger, but found %v", tc.ExpectedPendingTrigger, status.PendingTrigger)
			}
		})
	}
}

func TestTriggerHistoryLength(t *testing.T) {
	existingRecordsStatus := cronanything.TriggerResultCreateSucceeded
	newRecordStatus := cronanything.TriggerResultCreateFailed

	testCases := map[string]struct {
		initialHistoryLength int
		finalHistoryLength   int
	}{
		"new record to empty history": {
			initialHistoryLength: 0,
			finalHistoryLength:   1,
		},
		"new record to full history": {
			initialHistoryLength: 10,
			finalHistoryLength:   10,
		},
	}

	for tn, tc := range testCases {
		t.Run(tn, func(t *testing.T) {
			var history []cronanything.TriggerHistoryRecord
			for i := 0; i < tc.initialHistoryLength; i++ {
				history = append(history, cronanything.TriggerHistoryRecord{
					ScheduleTime:      metav1.NewTime(time.Now()),
					CreationTimestamp: metav1.NewTime(time.Now()),
					Result:            existingRecordsStatus,
				})
			}
			status := &cronanything.CronAnythingStatus{
				TriggerHistory: history,
			}

			r := cronanything.TriggerHistoryRecord{
				ScheduleTime:      metav1.NewTime(time.Now()),
				CreationTimestamp: metav1.NewTime(time.Now()),
				Result:            newRecordStatus,
			}
			addToTriggerHistory(status, r)

			if got, want := len(status.TriggerHistory), tc.finalHistoryLength; got != want {
				t.Errorf("Expected trigger history to have %d record, but found %d", want, got)
			}

			headOfHistory := status.TriggerHistory[0]
			if headOfHistory.Result != newRecordStatus {
				t.Errorf("Expected new record to be first in the history, but it was't")
			}
		})
	}
}

func isTriggerHistoriesEqual(actualHistory []cronanything.TriggerHistoryRecord, expectedHistory []cronanything.TriggerHistoryRecord) bool {
	if len(actualHistory) != len(expectedHistory) {
		return false
	}
	for index, actual := range actualHistory {
		expected := expectedHistory[index]
		if expected.Result != actual.Result || !expected.ScheduleTime.Equal(&actual.ScheduleTime) || !expected.CreationTimestamp.Equal(&actual.CreationTimestamp) {
			return false
		}
	}
	return true
}

func createUnstructuredSlice(ca *fakeCronAnything, count int) []*unstructured.Unstructured {
	var unstructuredSlice []*unstructured.Unstructured
	for i := 0; i < count; i++ {
		unstructuredSlice = append(unstructuredSlice, newUnstructuredResource(ca, fmt.Sprintf("resource-%d", i)))
	}
	return unstructuredSlice
}

func newCronAnythingForResourceName(name string, resourceBaseName, resourceTimestampFormat *string) cronanything.CronAnything {
	ca := newFakeCronAnything("db.anthosapis.com/v1alpha1", "TestKind", name, "default")
	ca.Spec.ResourceBaseName = resourceBaseName
	ca.Spec.ResourceTimestampFormat = resourceTimestampFormat
	return ca
}

func toPointer(s string) *string {
	return &s
}

func toTimestamp(t *testing.T, timestampString string) time.Time {
	timestamp, err := time.Parse(time.RFC3339, timestampString)
	if err != nil {
		t.Fatal(err)
	}
	return timestamp
}

type fakeCronAnythingControl struct {
	getKey          client.ObjectKey
	getCronAnything cronanything.CronAnything
	getError        error

	updateCronAnything cronanything.CronAnything
	updateError        error
}

func (r *fakeCronAnythingControl) Get(key client.ObjectKey) (cronanything.CronAnything, error) {
	r.getKey = key
	return r.getCronAnything, r.getError
}

func (r *fakeCronAnythingControl) Update(ca cronanything.CronAnything) error {
	r.updateCronAnything = ca
	return r.updateError
}

type fakeResourceControl struct {
	createResource  schema.GroupVersionResource
	createNamespace string
	createTemplate  *unstructured.Unstructured
	createCount     int
	createError     error

	deleteSlice []string
	deleteError error

	listResult []*unstructured.Unstructured
	listError  error
}

func (r *fakeResourceControl) Delete(resource schema.GroupVersionResource, namespace, name string) error {
	r.deleteSlice = append(r.deleteSlice, name)
	return r.deleteError
}

func (r *fakeResourceControl) Create(resource schema.GroupVersionResource, namespace string, template *unstructured.Unstructured) error {
	r.createResource = resource
	r.createNamespace = namespace
	r.createTemplate = template
	r.createCount += 1
	return r.createError
}

func (r *fakeResourceControl) List(resource schema.GroupVersionResource, _ string) ([]*unstructured.Unstructured, error) {
	return r.listResult, r.listError
}

type fakeResourceResolver struct {
}

func (r *fakeResourceResolver) Start(interval time.Duration, stopCh <-chan struct{}, log logr.Logger) {
}

func (r *fakeResourceResolver) Resolve(gvk schema.GroupVersionKind) (schema.GroupVersionResource, bool) {
	return schema.GroupVersionResource{}, true
}
