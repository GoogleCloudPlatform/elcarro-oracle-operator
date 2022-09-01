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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/robfig/cron"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/jsonpath"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cronanything "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
)

var controllerKind = schema.GroupVersion{Group: "db.anthosapis.com", Version: "v1alpha1"}.WithKind("CronAnything")

type resourceResolver interface {
	Start(refreshInterval time.Duration, stopCh <-chan struct{}, log logr.Logger)
	Resolve(gvk schema.GroupVersionKind) (schema.GroupVersionResource, bool)
}

// cronAnythingControl provides methods for getting and updating CronAnything resources.
type cronAnythingControl interface {
	Get(key client.ObjectKey) (cronanything.CronAnything, error)
	Update(ca cronanything.CronAnything) error
	Create(ns string, name string, cas cronanything.CronAnythingSpec, owner cronanything.BackupSchedule) error
}

// resourceControl provides methods for creating, deleting and listing any resource.
type resourceControl interface {
	Delete(resource schema.GroupVersionResource, namespace, name string) error
	Create(resource schema.GroupVersionResource, namespace string, template *unstructured.Unstructured) error
	List(resource schema.GroupVersionResource, cronAnythingName string) ([]*unstructured.Unstructured, error)
}

// NewCronAnythingReconciler returns a new CronAnything Reconciler.
func NewCronAnythingReconciler(mgr manager.Manager, log logr.Logger, cronAnythingControl cronAnythingControl) (*ReconcileCronAnything, error) {
	recorder := mgr.GetEventRecorderFor("cronanything-controller")

	dynClient, err := dynamic.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, err
	}

	resolver := NewResourceResolver(mgr.GetConfig())
	stopCh := make(chan struct{})
	resolver.Start(30*time.Second, stopCh, log)

	r := &ReconcileCronAnything{
		Log:                 log,
		cronanythingControl: cronAnythingControl,
		scheme:              mgr.GetScheme(),
		resourceResolver:    resolver,
		resourceControl: &realResourceControl{
			dynClient: dynClient,
		},
		eventRecorder: recorder,
		nextTrigger:   make(map[string]time.Time),
		currentTime:   time.Now,
	}

	return r, nil
}

var _ reconcile.Reconciler = &ReconcileCronAnything{}

// ReconcileCronAnything reconciles a CronAnything object.
type ReconcileCronAnything struct {
	Log                 logr.Logger
	cronanythingControl cronAnythingControl
	scheme              *runtime.Scheme
	resourceControl     resourceControl
	resourceResolver    resourceResolver
	eventRecorder       record.EventRecorder

	nextTrigger      map[string]time.Time
	nextTriggerMutex sync.Mutex

	currentTime func() time.Time
}

// Reconcile loop for CronAnything. The CronAnything controller does not watch child reosources.
// To make sure the reconcile loop are triggered when a cron expression triggers, the controller
// uses RequeueAfter.
func (r *ReconcileCronAnything) Reconcile(_ context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := r.Log.WithValues("cronanything-controller", request.NamespacedName)
	instance, err := r.cronanythingControl.Get(request.NamespacedName)
	if err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	canonicalName := fmt.Sprintf("%s/%s", instance.GetNamespace(), instance.GetName())
	now := r.currentTime()

	if instance.GetDeletionTimestamp() != nil {
		log.Info("Not creating resource because it is being deleted", "caName", canonicalName)
		return reconcile.Result{}, nil
	}

	crt, err := templateToUnstructured(instance)
	if err != nil {
		return reconcile.Result{}, err
	}

	cgvr, found := r.resourceResolver.Resolve(crt.GroupVersionKind())
	if !found {
		return reconcile.Result{}, fmt.Errorf("unable to resolve child resource for %s", canonicalName)
	}

	// Look up all child resources of the cronanything resource. These are needed to make sure the
	// controller adheres to the concurrency policy, to clean up completed resources as specified
	// in the historyLimit and control the total number of child resources as specified in totalResourceLimit
	childResources, err := r.getChildResources(instance, crt, cgvr)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Cleanup finished resources. Do this before checking the total resource usage to make sure
	// finished resources that should be deleted does not count against the total limit.
	childResources = r.cleanupHistory(instance, childResources, cgvr, now)

	// Just return without doing any work if it is suspended.
	if instance.CronAnythingSpec().Suspend != nil && *instance.CronAnythingSpec().Suspend {
		return reconcile.Result{}, nil
	}

	unmetScheduleTimes, nextScheduleTime, err := getScheduleTimes(instance, now.Add(1*time.Second))
	if err != nil {
		return reconcile.Result{}, err
	}

	if len(unmetScheduleTimes) == 0 {
		log.Info("No unmet trigger times", "caName", canonicalName)
		return r.updateTriggerTimes(canonicalName, nextScheduleTime), nil
	}

	scheduleTime := unmetScheduleTimes[len(unmetScheduleTimes)-1]
	droppedSchedules := unmetScheduleTimes[:len(unmetScheduleTimes)-1]

	if len(droppedSchedules) > 0 {
		log.Info("Dropping unmet triggers", "caName", canonicalName, "count", len(droppedSchedules))
		latestDropped := droppedSchedules[len(droppedSchedules)-1]

		historyRecord := cronanything.TriggerHistoryRecord{
			ScheduleTime:      metav1.NewTime(latestDropped),
			CreationTimestamp: metav1.NewTime(now),
		}

		if len(droppedSchedules) == 1 && instance.CronAnythingStatus().PendingTrigger != nil && instance.CronAnythingStatus().PendingTrigger.ScheduleTime.Equal(getMetaTimePointer(latestDropped)) {
			// If we get here it means we have one dropped trigger and also we have a trigger that
			// we haven't been able to complete. Use the info from the pending trigger to report this
			// in the trigger history.
			historyRecord.Result = instance.CronAnythingStatus().PendingTrigger.Result
		} else {
			historyRecord.Result = cronanything.TriggerResultMissed
		}

		err := r.updateCronAnythingStatus(instance.GetName(), instance.GetNamespace(), func(freshStatus *cronanything.CronAnythingStatus) {
			updateLastScheduleTime(freshStatus, latestDropped)
			freshStatus.PendingTrigger = nil
			addToTriggerHistory(freshStatus, historyRecord)
		})
		if err != nil {
			// Since we haven't done anything yet, we can safely just return the error here and let the controller
			// retry.
			return reconcile.Result{}, err
		}

	}

	log.Info("Unmet trigger time", "caName", canonicalName, "scheduledTime", scheduleTime.Format(time.RFC3339))

	if instance.CronAnythingSpec().TriggerDeadlineSeconds != nil {
		triggerDeadline := time.Duration(*instance.CronAnythingSpec().TriggerDeadlineSeconds)
		if scheduleTime.Add(triggerDeadline * time.Second).Before(now) {
			log.Info("Trigger deadline exceeded", "caName", canonicalName, "triggerTime", canonicalName, "scheduleTime", scheduleTime.Format(time.RFC3339))
			err = r.updateCronAnythingStatus(instance.GetName(), instance.GetNamespace(), func(freshStatus *cronanything.CronAnythingStatus) {
				updateLastScheduleTime(freshStatus, scheduleTime)

				historyRecord := cronanything.TriggerHistoryRecord{
					ScheduleTime:      metav1.NewTime(scheduleTime),
					CreationTimestamp: metav1.NewTime(now),
				}

				if freshStatus.PendingTrigger != nil && freshStatus.PendingTrigger.ScheduleTime.Equal(getMetaTimePointer(scheduleTime)) {
					historyRecord.Result = freshStatus.PendingTrigger.Result
				} else {
					historyRecord.Result = cronanything.TriggerResultDeadlineExceeded
				}
				addToTriggerHistory(freshStatus, historyRecord)

				freshStatus.PendingTrigger = nil
			})
			if err != nil {
				return reconcile.Result{}, err
			}
			return r.updateTriggerTimes(canonicalName, nextScheduleTime), nil
		}
	}

	activeChildResources := findActiveChildResources(instance, childResources, log)
	log.Info("Found active child resources", "caName", canonicalName, "numActiveChildResources", len(activeChildResources))
	if len(activeChildResources) > 0 {
		switch instance.CronAnythingSpec().ConcurrencyPolicy {
		case cronanything.ForbidConcurrent:
			log.Info("Found existing active resource, so no new scheduled due to ForbidConcurrent policy", "caName", canonicalName)
			err = r.updateCronAnythingStatus(instance.GetName(), instance.GetNamespace(), func(freshStatus *cronanything.CronAnythingStatus) {
				updateLastScheduleTime(freshStatus, scheduleTime)

				freshStatus.PendingTrigger = nil

				addToTriggerHistory(freshStatus, cronanything.TriggerHistoryRecord{
					ScheduleTime:      metav1.NewTime(scheduleTime),
					CreationTimestamp: metav1.NewTime(now),
					Result:            cronanything.TriggerResultForbidConcurrent,
				})
			})
			if err != nil {
				return reconcile.Result{}, err
			}
			return r.updateTriggerTimes(canonicalName, nextScheduleTime), nil
		case cronanything.ReplaceConcurrent:
			// All currently active resources should be replaced. We do this by deleting them.
			for _, activeResource := range activeChildResources {
				// No need to delete resource if it already in the process of being deleted.
				if activeResource.GetDeletionTimestamp() != nil {
					continue
				}
				log.Info("Deleting resource due to ReplaceConcurrent policy", "caName", canonicalName, "activeResource", activeResource.GetName())
				err = r.resourceControl.Delete(cgvr, activeResource.GetNamespace(), activeResource.GetName())
				if err != nil {
					r.eventRecorder.Eventf(instance, v1.EventTypeWarning, "FailedDeleteForReplace", "Error deleting resource %s: %v", activeResource.GetName(), err)
					return reconcile.Result{}, err
				}
				r.eventRecorder.Eventf(instance, v1.EventTypeNormal, "DeletedForReplace", "Resource %s deleted due to Replace policy", activeResource.GetName())
			}
			// Returning here. Next iteration the resources will (hopefully) have been deleted and a new object can
			// be created. If the deletion is not completed, it will have to wait longer.
			return reconcile.Result{
				RequeueAfter: 1 * time.Second,
			}, nil
		}
	}

	if instance.CronAnythingSpec().TotalResourceLimit != nil && int32(len(childResources)) >= *instance.CronAnythingSpec().TotalResourceLimit {
		log.Info("Resource limit info", "caName", canonicalName, "limit", *instance.CronAnythingSpec().TotalResourceLimit, "numChildResources", len(childResources))
		r.eventRecorder.Eventf(instance, v1.EventTypeWarning, "ResourceLimitReached", "Limit of %d resources has been reached", len(childResources))
		log.Info("Resource limit has been reached. No new resource can be created", "caName", canonicalName)
		err = r.updateCronAnythingStatus(instance.GetName(), instance.GetNamespace(), func(freshStatus *cronanything.CronAnythingStatus) {
			updateLastScheduleTime(freshStatus, scheduleTime)

			freshStatus.PendingTrigger = nil

			addToTriggerHistory(freshStatus, cronanything.TriggerHistoryRecord{
				ScheduleTime:      metav1.NewTime(scheduleTime),
				CreationTimestamp: metav1.NewTime(now),
				Result:            cronanything.TriggerResultResourceLimitReached,
			})
		})

		if err != nil {
			return reconcile.Result{}, err
		}
		return r.updateTriggerTimes(canonicalName, nextScheduleTime), nil
	}

	name := getResourceName(instance, scheduleTime)

	crt.SetName(name)
	labels := crt.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[cronanything.CronAnythingCreatedByLabel] = instance.GetName()
	labels[cronanything.CronAnythingScheduleTimeLabel] = strconv.FormatInt(scheduleTime.Unix(), 10)
	crt.SetLabels(labels)
	if instance.CronAnythingSpec().CascadeDelete != nil && *instance.CronAnythingSpec().CascadeDelete {
		crt.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(instance, controllerKind)})
	}

	err = r.resourceControl.Create(cgvr, instance.GetNamespace(), crt)
	if err != nil {
		statusErr := r.updateCronAnythingStatus(instance.GetName(), instance.GetNamespace(), func(freshStatus *cronanything.CronAnythingStatus) {
			freshStatus.PendingTrigger = &cronanything.PendingTrigger{
				ScheduleTime: metav1.NewTime(scheduleTime),
				Result:       cronanything.TriggerResultCreateFailed,
			}

		})
		if statusErr != nil {
			log.Error(statusErr, "Failed to update status for CronAnything after failed create attempt", "caName", canonicalName)
		}
		r.eventRecorder.Eventf(instance, v1.EventTypeWarning, "FailedCreate", "Error creating job: %v", err)
		return reconcile.Result{}, err
	}
	log.Info("Created new resource", "caName", canonicalName, "childName", name)
	r.eventRecorder.Eventf(instance, v1.EventTypeNormal, "SuccessfulCreate", "Created resource %s", name)

	err = r.updateCronAnythingStatus(instance.GetName(), instance.GetNamespace(), func(freshStatus *cronanything.CronAnythingStatus) {
		updateLastScheduleTime(freshStatus, scheduleTime)

		freshStatus.PendingTrigger = nil

		addToTriggerHistory(freshStatus, cronanything.TriggerHistoryRecord{
			ScheduleTime:      metav1.NewTime(scheduleTime),
			CreationTimestamp: metav1.NewTime(now),
			Result:            cronanything.TriggerResultCreateSucceeded,
		})
	})
	if err != nil {
		return reconcile.Result{}, err
	}

	return r.updateTriggerTimes(canonicalName, nextScheduleTime), nil
}

func updateLastScheduleTime(status *cronanything.CronAnythingStatus, scheduleTime time.Time) {
	if status.LastScheduleTime == nil || status.LastScheduleTime.Time.Before(scheduleTime) {
		status.LastScheduleTime = &metav1.Time{Time: scheduleTime}
	}
}

func addToTriggerHistory(status *cronanything.CronAnythingStatus, record cronanything.TriggerHistoryRecord) {
	status.TriggerHistory = append([]cronanything.TriggerHistoryRecord{record}, status.TriggerHistory...)

	if len(status.TriggerHistory) > cronanything.TriggerHistoryMaxLength {
		status.TriggerHistory = status.TriggerHistory[:cronanything.TriggerHistoryMaxLength]
	}
}

func (r *ReconcileCronAnything) updateCronAnythingStatus(name, namespace string, updateFunc func(*cronanything.CronAnythingStatus)) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		key := types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		}
		instance, err := r.cronanythingControl.Get(key)
		if err != nil {
			return err
		}

		updateFunc(instance.CronAnythingStatus())
		return r.cronanythingControl.Update(instance)
	})
}

// updateTriggerTimes updates the local map of trigger times.
// This map requires syncronized access, so a lock is acquired on the nextTriggerMutex.
func (r *ReconcileCronAnything) updateTriggerTimes(canonicalName string, nextScheduleTime time.Time) reconcile.Result {
	r.nextTriggerMutex.Lock()
	defer r.nextTriggerMutex.Unlock()
	triggerTime, found := r.nextTrigger[canonicalName]
	if found && triggerTime.Equal(nextScheduleTime) {
		r.Log.Info("Next trigger already queued", "caName", canonicalName, "nextScheduleTime", nextScheduleTime.Format(time.RFC3339))
		return reconcile.Result{}
	}
	r.nextTrigger[canonicalName] = nextScheduleTime
	r.Log.Info("Next trigger time queued", "caName", canonicalName, "nextScheduleTime", nextScheduleTime.Format(time.RFC3339))
	return reconcile.Result{
		RequeueAfter: nextScheduleTime.Sub(r.currentTime()),
	}
}

// byTimestamp allows for sorting a slice of unstructured based on timestamp.
type byTimestamp struct {
	items          []*unstructured.Unstructured
	timestampCache map[types.UID]time.Time
}

func newByTimestamp(ca cronanything.CronAnything, items []*unstructured.Unstructured) byTimestamp {
	cache := make(map[types.UID]time.Time)
	for _, item := range items {
		finishedTime, err := getResourceTimestamp(ca, item)
		if err == nil {
			cache[item.GetUID()] = finishedTime
		}
	}

	return byTimestamp{
		items:          items,
		timestampCache: cache,
	}
}

func (b byTimestamp) Len() int      { return len(b.items) }
func (b byTimestamp) Swap(i, j int) { b.items[i], b.items[j] = b.items[j], b.items[i] }
func (b byTimestamp) Less(i, j int) bool {
	iResource := b.items[i]
	jResource := b.items[j]
	iTimestamp, iHasTimestamp := b.timestampCache[iResource.GetUID()]
	jTimestamp, jHasTimestamp := b.timestampCache[jResource.GetUID()]

	if !iHasTimestamp && !jHasTimestamp {
		iCreationTime := iResource.GetCreationTimestamp()
		jCreationTime := jResource.GetCreationTimestamp()
		return jCreationTime.Before(&iCreationTime)
	}
	if !iHasTimestamp && jHasTimestamp {
		return false
	}
	if iHasTimestamp && !jHasTimestamp {
		return true
	}
	return jTimestamp.Before(iTimestamp)
}

// cleanupHistory cleans up finished resources if the count is higher than the threshold provided in the
// historyLimit field.
func (r *ReconcileCronAnything) cleanupHistory(ca cronanything.CronAnything, childResources []*unstructured.Unstructured, resource schema.GroupVersionResource, now time.Time) []*unstructured.Unstructured {
	if ca.CronAnythingSpec().Retention == nil {
		return childResources
	}
	historyTimeLimit := ca.CronAnythingSpec().Retention.HistoryTimeLimitSeconds
	historyCountLimit := ca.CronAnythingSpec().Retention.HistoryCountLimit
	if historyTimeLimit == nil && historyCountLimit == nil {
		return childResources
	}

	sort.Sort(newByTimestamp(ca, childResources))

	keeperCount := int32(0)
	var toBeDeleted []*unstructured.Unstructured
	for _, child := range childResources {
		finished, err := isFinished(ca, child)
		if err != nil {
			r.Log.Error(err, "Error checking if resource is finished", "childName", child.GetName())
			continue
		}
		if !finished {
			continue
		}

		timestamp, err := getResourceTimestamp(ca, child)
		if err != nil {
			r.Log.Error(err, "Error looking up finish time on resource", "childName", child.GetName())
			continue
		}

		if historyTimeLimit != nil && timestamp.Before(now) && now.Sub(timestamp).Seconds() > float64(*historyTimeLimit) {
			toBeDeleted = append(toBeDeleted, child)
			continue
		}

		if historyCountLimit != nil && keeperCount >= *historyCountLimit {
			toBeDeleted = append(toBeDeleted, child)
			continue
		}
		keeperCount++
	}

	return r.deleteInactiveResources(ca, toBeDeleted, childResources, resource)
}

// deleteInactiveResources deletes all child resources in the toBeDeleted slice and returns an updated slice of child resources
// that no longer includes any resources that has been successfully deleted.
func (r *ReconcileCronAnything) deleteInactiveResources(ca cronanything.CronAnything, toBeDeleted []*unstructured.Unstructured, childResources []*unstructured.Unstructured, resource schema.GroupVersionResource) []*unstructured.Unstructured {
	deleted := make(map[string]bool)
	for _, child := range toBeDeleted {
		if child.GetDeletionTimestamp() != nil {
			continue
		}
		r.Log.Info("Deleting inactive resource", "childName", child.GetName())
		err := r.resourceControl.Delete(resource, child.GetNamespace(), child.GetName())
		if err != nil {
			r.eventRecorder.Eventf(ca, v1.EventTypeWarning, "FailedDeleteFinished", "Error deleting finished resource: %v", err)
			r.Log.Error(err, "Error deleting finished resource", "childName", child.GetName())
		} else {
			deleted[child.GetName()] = true
		}
	}

	var remaining []*unstructured.Unstructured
	for _, resource := range childResources {
		if _, isDeleted := deleted[resource.GetName()]; !isDeleted {
			remaining = append(remaining, resource)
		}
	}

	return remaining
}

// Filters the list of resources to only the resources that have not finished.
func findActiveChildResources(ca cronanything.CronAnything, childResources []*unstructured.Unstructured, log logr.Logger) []*unstructured.Unstructured {
	return filterChildResources(ca, childResources, func(ca cronanything.CronAnything, resource *unstructured.Unstructured) (bool, error) {
		finished, err := isFinished(ca, resource)
		return !finished, err
	}, log)
}

type filterFunc func(ca cronanything.CronAnything, resource *unstructured.Unstructured) (bool, error)

func filterChildResources(ca cronanything.CronAnything, childResources []*unstructured.Unstructured, include filterFunc, log logr.Logger) []*unstructured.Unstructured {
	var filtered []*unstructured.Unstructured
	for _, resource := range childResources {
		shouldInclude, err := include(ca, resource)
		if err != nil {
			log.Error(err, "Unable to invoke include function")
		}
		if shouldInclude {
			filtered = append(filtered, resource)
		}
	}
	return filtered
}

// isFinished checks whether the provided resource has finished. It is considered finished if the
// the field .status.finishedAt exists and contains a valid timestamp.
func isFinished(ca cronanything.CronAnything, resource *unstructured.Unstructured) (bool, error) {
	if ca.CronAnythingSpec().FinishableStrategy == nil {
		return false, nil
	}
	switch ca.CronAnythingSpec().FinishableStrategy.Type {
	case cronanything.FinishableStrategyTimestampField:
		strategy := ca.CronAnythingSpec().FinishableStrategy.TimestampField
		if strategy == nil {
			return false, fmt.Errorf("FinishableStrategy type is timestampField, but TimestampField property is nil")
		}
		_, isFinished, err := getTimestamp(strategy.FieldPath, resource)
		return isFinished, err
	case cronanything.FinishableStrategyStringField:
		strategy := ca.CronAnythingSpec().FinishableStrategy.StringField
		if strategy == nil {
			return false, fmt.Errorf("FinishableStrategy type is stringField, but StringField property is nil")
		}
		value, err := getFieldValue(strategy.FieldPath, resource)
		if err != nil {
			return false, err
		}

		for _, v := range strategy.FinishedValues {
			if v == value {
				return true, nil
			}
		}
		return false, nil
	default:
		// If no strategy matches, we assume the resource is not finishable.
		return false, nil
	}
}

func getResourceTimestamp(ca cronanything.CronAnything, resource *unstructured.Unstructured) (time.Time, error) {
	switch ca.CronAnythingSpec().Retention.ResourceTimestampStrategy.Type {
	case cronanything.ResourceTimestampStrategyField:
		strategy := ca.CronAnythingSpec().Retention.ResourceTimestampStrategy.FieldResourceTimestampStrategy
		timestamp, _, err := getTimestamp(strategy.FieldPath, resource)
		if err != nil {
			return time.Time{}, err
		}
		return timestamp, nil
	}
	return time.Time{}, fmt.Errorf("can't find timestamp for resource %s", resource.GetName())
}

// getTimestamp returns the finish time for the given resource, a bool value that tells whether the resource has finished and
// an error in the event something goes wrong. The location of the finished field is taken from the CronAnything
// resource or the default .status.finishedAt .
func getTimestamp(timestampFieldPath string, resource *unstructured.Unstructured) (time.Time, bool, error) {
	value, err := getFieldValue(timestampFieldPath, resource)
	if err != nil {
		return time.Time{}, false, err
	}
	if len(value) == 0 {
		return time.Time{}, false, nil
	}
	var timestamp metav1.Time
	err = timestamp.UnmarshalQueryParameter(value)
	if err != nil {
		return time.Time{}, false, err
	}
	return timestamp.Time, true, nil
}

func getFieldValue(fieldPath string, resource *unstructured.Unstructured) (string, error) {
	jp := jsonpath.New("parser").AllowMissingKeys(true)
	err := jp.Parse(fieldPath)
	if err != nil {
		return "", err
	}
	buf := new(bytes.Buffer)
	err = jp.Execute(buf, resource.Object)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// getChildResources lists out all the child resources of the given cronanything resource. This means all resources
// that have the apiVersion and kind specified by the template and that have an owner
// reference that points to the cronanything resource.
func (r *ReconcileCronAnything) getChildResources(ca cronanything.CronAnything, template *unstructured.Unstructured, resource schema.GroupVersionResource) ([]*unstructured.Unstructured, error) {
	return r.resourceControl.List(resource, ca.GetName())
}

// getResourceName returns for the resource created by the given cronanything resource at the given time.
func getResourceName(ca cronanything.CronAnything, scheduleTime time.Time) string {
	baseName := ca.GetName()
	if ca.CronAnythingSpec().ResourceBaseName != nil && len(*ca.CronAnythingSpec().ResourceBaseName) > 0 {
		baseName = *ca.CronAnythingSpec().ResourceBaseName
	}

	timestamp := fmt.Sprintf("%d", scheduleTime.Unix())
	if ca.CronAnythingSpec().ResourceTimestampFormat != nil && len(*ca.CronAnythingSpec().ResourceTimestampFormat) > 0 {
		timestamp = scheduleTime.Format(*ca.CronAnythingSpec().ResourceTimestampFormat)
	}
	return fmt.Sprintf("%s-%s", baseName, timestamp)
}

// getScheduleTimes returns the next schedule time for the cronexpression and the given time. It also returns a slice with
// all previously schedule times based on the .Status.LastScheduleTime field in CronAnything.
func getScheduleTimes(ca cronanything.CronAnything, now time.Time) ([]time.Time, time.Time, error) {
	schedule, err := cron.ParseStandard(ca.CronAnythingSpec().Schedule)
	if err != nil {
		// Allow Full Cron formats if its invalid as a standard cron format.
		var err2 error
		schedule, err2 = cron.Parse(ca.CronAnythingSpec().Schedule)
		if err2 != nil {
			return nil, time.Time{}, fmt.Errorf("unable to parse schedule string: %v", err)
		}
		err = nil
	}

	var scheduleTimes []time.Time
	lastScheduleTime := ca.CronAnythingStatus().LastScheduleTime
	var startSchedTime time.Time
	if lastScheduleTime == nil {
		startSchedTime = ca.GetCreationTimestamp().Time
	} else {
		startSchedTime = lastScheduleTime.Time
	}
	for t := schedule.Next(startSchedTime); t.Before(now); t = schedule.Next(t) {
		scheduleTimes = append(scheduleTimes, t)
	}

	next := schedule.Next(now)

	return scheduleTimes, next, nil
}

// templateToUnstructured takes the raw extension template from the cronanything
// resource and turns it into an unstructured that can be used to create new resources.
func templateToUnstructured(ca cronanything.CronAnything) (*unstructured.Unstructured, error) {
	template := make(map[string]interface{})
	err := json.Unmarshal(ca.CronAnythingSpec().Template.Raw, &template)
	if err != nil {
		return nil, fmt.Errorf("unable to parse template: %v", err)
	}

	return &unstructured.Unstructured{
		Object: template,
	}, nil
}

func getMetaTimePointer(t time.Time) *metav1.Time {
	metaTime := metav1.NewTime(t)
	return &metaTime
}
