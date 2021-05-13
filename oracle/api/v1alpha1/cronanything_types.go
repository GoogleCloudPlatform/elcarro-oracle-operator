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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	// CronAnythingCreatedByLabel is the name of the label used by CronAnything to
	// denote the entity which created the resource.
	CronAnythingCreatedByLabel = "oracle.db.anthosapis.com/created-by"

	// CronAnythingScheduleTimeLabel is the name of the label used by CronAnything
	// to denote the schedule time.
	CronAnythingScheduleTimeLabel = "oracle.db.anthosapis.com/schedule-time"

	// TriggerHistoryMaxLength defines the maximum number of trigger history to
	// keep track of by CronAnything.
	TriggerHistoryMaxLength = 10
)

// CronAnythingSpec defines the desired state of CronAnything.
type CronAnythingSpec struct {
	// Schedule defines a time-based schedule, e.g., a standard cron schedule such
	// as “@every 10m”. This field is mandatory and mutable. If it is changed,
	// resources will simply be created at the new interval from then on.
	Schedule string `json:"schedule"`

	// TriggerDeadlineSeconds defines Deadline in seconds for creating the
	// resource if it missed the scheduled time. If no deadline is provided, the
	// resource will be created no matter how far after the scheduled time.
	// If multiple triggers were missed, only the last will be triggered and only
	// one resource will be created. This field is mutable and changing it
	// will affect the creation of new resources from that point in time.
	// +optional
	TriggerDeadlineSeconds *int64 `json:"triggerDeadlineSeconds,omitempty"`

	// ConcurrencyPolicy specifies how to treat concurrent resources if the
	// resource provides a status path that exposes completion.
	// The default policy if not provided is to allow a new resource to be created
	// even if an active resource already exists.
	// If the resource doesn’t have an active/completed status, the only supported
	// concurrency policy is to allow creating new resources.
	// This field is mutable. If the policy is changed to a more stringent policy
	// while multiple resources are active, it will not delete any existing
	// resources. The exception is if a creation of a new resource is triggered
	// and the policy has been changed to Replace. If multiple resources are
	// active, they will all be deleted and replaced by a new resource.
	// +optional
	ConcurrencyPolicy ConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

	// Suspend tells the controller to suspend creation of additional resources.
	// The default value is false. This field is mutable. It will not affect any
	// existing resources, but only affect creation of additional resources.
	// +optional
	Suspend *bool `json:"suspend,omitempty"`

	// FinishableStrategy defines how the CronAnything controller an decide if a
	// resource has completed.
	// Some resources will do some work after they have been created and at some
	// point be finished. Jobs are the most common example.
	// If no strategy is defined, it is assumed that the resources never finish.
	// +optional
	FinishableStrategy *FinishableStrategy `json:"finishableStrategy,omitempty"`

	// Template is a template of a resource type for which instances are to
	// be created on the given schedule.
	// This field is mandatory and it must contain a valid template for an
	// existing apiVersion and kind in the cluster.
	// It is immutable, so if the template needs to change, the whole CronAnything
	// resource should be replaced.
	Template runtime.RawExtension `json:"template"`

	// TotalResourceLimit specifies the total number of children allowed for a
	// particular CronAnything resource. If this limit is reached, no additional
	// resources will be created.
	// This limit is mostly meant to avoid runaway creation of resources that
	// could bring down the cluster. Both finished and unfinished resources count
	// against this limit.
	// This field is mutable. If it is changed to a lower value than the existing
	// number of resources, none of the existing resources will be deleted as a
	// result, but no additional resources will be created until the number of
	// child resources goes below the limit.
	// The field is optional with a default value of 100.
	// +optional
	TotalResourceLimit *int32 `json:"totalResourceLimit,omitempty"`

	// Retention defines the retention policy for resources created by
	// CronAnything. If no retention policy is defined, CronAnything will never
	// delete resources, so cleanup must be handled through some other process.
	// +optional
	Retention *ResourceRetention `json:"retention,omitempty"`

	// CascadeDelete tells CronAnything to set up owner references from the
	// created resources to the CronAnything resource. This means that if the
	// CronAnything resource is deleted, all resources created by it will also be
	// deleted. This is an optional field that defaults to false.
	// +optional
	CascadeDelete *bool `json:"cascadeDelete,omitempty"`

	// ResourceBaseName specifies the base name for the resources created by
	// CronAnything, which will be named using the format
	// <ResourceBaseName>-<Timestamp>. This field is optional, and the default
	// is to use the name of the CronAnything resource as the ResourceBaseName.
	// +optional
	ResourceBaseName *string `json:"resourceBaseName,omitempty"`

	// ResourceTimestampFormat defines the format of the timestamp in the name of
	// Resources created by CronAnything <ResourceBaseName>-<Timestamp>.
	// This field is optional, and the default is to format the timestamp as unix
	// time. If provided, it must be compatible with time.Format in golang.
	// +optional
	ResourceTimestampFormat *string `json:"resourceTimestampFormat,omitempty"`
}

// ResourceRetention specifies the retention policy for resources.
type ResourceRetention struct {
	// The number of completed resources to keep before deleting them. This
	// only affects finishable resources and the default value is 3.
	// This field is mutable and if it is changed to a number lower than
	// the current number of finished resources, the oldest ones will
	// eventually be deleted until the number of finished resources matches
	// the limit.
	// +optional
	HistoryCountLimit *int32 `json:"historyCountLimit,omitempty"`

	// The time since completion that a resource is kept before deletion. This
	// only affects finishable resources. This does not have any default value and
	// if it is not provided, HistoryCountLimit will be used to prune completed
	// resources.
	// If both HistoryCountLimit and  HistoryTimeLimitSeconds are set, it is treated
	// as an OR operation.
	// +optional
	HistoryTimeLimitSeconds *uint64 `json:"historyTimeLimitSeconds,omitempty"`

	// ResourceTimestampStrategy specifies how the CronAnything controller
	// can find the age of a resource. This is needed to support retention.
	ResourceTimestampStrategy ResourceTimestampStrategy `json:"resourceTimestampStrategy"`
}

// FinishableStrategyType specifies the type of the field which tells whether
// a resource is finished.
type FinishableStrategyType string

const (
	// FinishableStrategyTimestampField specifies deriving whether a resource is
	// finished from a timestamp field.
	FinishableStrategyTimestampField FinishableStrategyType = "TimestampField"

	// FinishableStrategyStringField specifies deriving whether a resource is
	// finished from a string field.
	FinishableStrategyStringField FinishableStrategyType = "StringField"
)

// FinishableStrategy specifies how the CronAnything controller can decide
// whether a created resource has completed. This is needed for any concurrency
// policies other than AllowConcurrent.
type FinishableStrategy struct {

	// Type tells which strategy should be used.
	Type FinishableStrategyType `json:"type"`

	// TimestampField contains the details for how the CronAnything controller
	// can find the timestamp field on the resource in order to decide if the
	// resource has completed.
	// +optional
	TimestampField *TimestampFieldStrategy `json:"timestampField,omitempty"`

	// StringField contains the details for how the CronAnything controller
	// can find the string field on the resource needed to decide if the resource
	// has completed. It also lists the values that mean the resource has completed.
	// +optional
	StringField *StringFieldStrategy `json:"stringField,omitempty"`
}

// TimestampFieldStrategy defines how the CronAnything controller can find
// a field on the resource that contains a timestamp. The contract here is that
// if the field contains a valid timestamp the resource is considered finished.
type TimestampFieldStrategy struct {

	// The path to the field on the resource that contains the timestamp.
	FieldPath string `json:"fieldPath"`
}

// StringFieldStrategy defines how the CronAnything controller can find and
// use the value of a field on the resource to decide if it has finished.
type StringFieldStrategy struct {

	// The path to the field on the resource that contains a string value.
	FieldPath string `json:"fieldPath"`

	// The values of the field that means the resource has completed.
	FinishedValues []string `json:"finishedValues"`
}

// ResourceTimestampStrategyType specifies the strategy to use for getting
// the resource timestamp.
type ResourceTimestampStrategyType string

const (
	// ResourceTimestampStrategyField specifies getting the timestamp for the
	// resource from a field on the resource.
	ResourceTimestampStrategyField ResourceTimestampStrategyType = "Field"
)

// ResourceTimestampStrategy specifies how the CronAnything controller can find
// the timestamp on the resource that will again decide the order in which
// resources are deleted based on the retention policy.
type ResourceTimestampStrategy struct {

	// Type tells which strategy should be used.
	Type ResourceTimestampStrategyType `json:"type"`

	// FieldResourceTimestampStrategy specifies how the CronAnything controller
	// can find the timestamp for the resource from a field.
	// +optional
	FieldResourceTimestampStrategy *FieldResourceTimestampStrategy `json:"field,omitempty"`
}

// FieldResourceTimestampStrategy defines how the CronAnything controller can
// find the timestamp for a resource.
type FieldResourceTimestampStrategy struct {

	// The path to the field on the resource that contains the timestamp.
	FieldPath string `json:"fieldPath"`
}

// ConcurrencyPolicy specifies the policy to use for concurrency control.
type ConcurrencyPolicy string

const (
	// AllowConcurrent policy specifies allowing creation of new resources
	// regardless of how many other currently active resources exist.
	AllowConcurrent ConcurrencyPolicy = "Allow"

	// ForbidConcurrent policy specifies not allowing creation of a new resource
	// if any existing resources are active.
	ForbidConcurrent ConcurrencyPolicy = "Forbid"

	// ReplaceConcurrent policy specifies deleting any existing, active resources
	// before creating a new one.
	ReplaceConcurrent ConcurrencyPolicy = "Replace"
)

// CronAnythingStatus defines the observed state of CronAnything.
type CronAnythingStatus struct {

	// LastScheduleTime keeps track of the scheduled time for the last
	// successfully completed creation of a resource.
	// This is used by the controller to determine when the next resource creation
	// should happen. If creation of a resource is delayed for any reason but
	// eventually does happen, this value will still be updated to the time when
	// it was originally scheduled to happen.
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`

	// TriggerHistory keeps track of the status for the last 10 triggers. This
	// allows users of CronAnything to see whether any triggers failed. It is
	// important to know that this only keeps track of whether a trigger was
	// successfully executed (as in creating the given resource), not whether the
	// created resource was itself successful. For this information, any users
	// of CronAnything should observe the resources created.
	// +optional
	TriggerHistory []TriggerHistoryRecord `json:"triggerHistory,omitempty"`

	// PendingTrigger keeps track of any triggers that are past their trigger time,
	// but for some reason have not been completed yet. This is typically a result
	// of the create operation failing.
	// +optional
	PendingTrigger *PendingTrigger `json:"pendingTrigger,omitempty"`
}

// PendingTrigger keeps information about triggers that should have been
// completed, but due to some kind of error, is still pending. They will
// typically remain in this state until either the issue has been resolved and
// the resource in question can be created, the triggerDeadlineSeconds is
// reached and we stop trying, or the next trigger time is reached at which time
// we consider the previous trigger as failed.
type PendingTrigger struct {

	// ScheduleTime is the time when this trigger was scheduled to be executed.
	ScheduleTime metav1.Time `json:"scheduleTime"`

	// Result tells why this trigger is in the pending state, i.e. what prevented
	// it from completing successfully.
	Result TriggerResult `json:"result"`
}

// TriggerHistoryRecord contains information about the result of a trigger. It
// can either have completed successfully, and if it did not, the record will
// provide information about what is the cause of the failure.
type TriggerHistoryRecord struct {

	// ScheduleTime is the time when this trigger was scheduled to be executed.
	ScheduleTime metav1.Time `json:"scheduleTime"`

	// CreationTimestamp is the time when this record was created. This is thus
	// also the time at which the final result of the trigger was decided.
	CreationTimestamp metav1.Time `json:"creationTimestamp"`

	// Result contains the outcome of a trigger. It can either be CreateSucceeded,
	// which means the given resource was created as intended, or it can be one
	// of several error messages.
	Result TriggerResult `json:"result"`
}

// TriggerResult specifies the result of a trigger.
type TriggerResult string

const (
	// TriggerResultMissed means the trigger was not able to complete until the
	// next trigger fired. Thus the trigger missed its window for being executed.
	TriggerResultMissed TriggerResult = "MissedSchedule"

	// TriggerResultCreateFailed means the create operation for a resource failed.
	// This itself doesn't cause the trigger to fail, but this status will be
	// reported if failing create operations are the reason a trigger misses its
	// window for being executed.
	TriggerResultCreateFailed TriggerResult = "CreateFailed"

	// TriggerResultCreateSucceeded means the trigger was successful.
	TriggerResultCreateSucceeded TriggerResult = "CreateSucceeded"

	// TriggerResultResourceLimitReached means the trigger could not be completed
	// as the resource limit was reached and it is not possible to create
	// additional resources.
	TriggerResultResourceLimitReached TriggerResult = "ResourceLimitReached"

	// TriggerResultForbidConcurrent means the trigger could not be completed as
	// there is already an unfinished resource and the concurrency policy forbid
	// any concurrently running resources.
	TriggerResultForbidConcurrent TriggerResult = "ForbidConcurrent"

	// TriggerResultDeadlineExceeded means the trigger could not be completed as
	// the deadline for how delayed a trigger can be was reached.
	TriggerResultDeadlineExceeded TriggerResult = "DeadlineExceeded"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CronAnything is the Schema for the cronanythings API.
// +k8s:openapi-gen=true
type CronAnything struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CronAnythingSpec   `json:"spec,omitempty"`
	Status CronAnythingStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CronAnythingList contains a list of CronAnything.
type CronAnythingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CronAnything `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CronAnything{}, &CronAnythingList{})
}
