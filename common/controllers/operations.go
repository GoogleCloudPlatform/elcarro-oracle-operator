// Copyright 2018 Google LLC
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
	"context"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	cronanything "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
)

type realResourceControl struct {
	dynClient dynamic.Interface
}

func (r *realResourceControl) Delete(resource schema.GroupVersionResource, namespace, name string) error {
	deleteForeground := metav1.DeletePropagationForeground
	return r.dynClient.Resource(resource).Namespace(namespace).Delete(context.TODO(), name, metav1.DeleteOptions{PropagationPolicy: &deleteForeground})
}

func (r *realResourceControl) Create(resource schema.GroupVersionResource, namespace string, template *unstructured.Unstructured) error {
	_, err := r.dynClient.Resource(resource).Namespace(namespace).Create(context.TODO(), template, metav1.CreateOptions{})
	return err
}

func (r *realResourceControl) List(resource schema.GroupVersionResource, cronAnythingName string) ([]*unstructured.Unstructured, error) {
	res, err := r.dynClient.Resource(resource).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{cronanything.CronAnythingCreatedByLabel: cronAnythingName}).String(),
	})
	if err != nil {
		return []*unstructured.Unstructured{}, err
	}
	list, err := meta.ExtractList(res)
	if err != nil {
		return []*unstructured.Unstructured{}, err
	}

	returnList := make([]*unstructured.Unstructured, 0)
	for _, obj := range list {
		unstructuredResource, _ := obj.(*unstructured.Unstructured)
		returnList = append(returnList, unstructuredResource)
	}
	return returnList, nil
}

// NewResourceResolver creates a resource resolver to find the corresponding
// group version resource for a given group version kind.
func NewResourceResolver(config *rest.Config) *realResourceResolver {
	dc := discovery.NewDiscoveryClientForConfigOrDie(config)
	return &realResourceResolver{
		dc: dc,
	}
}

type realResourceResolver struct {
	mu              sync.Mutex
	dc              *discovery.DiscoveryClient
	resourceMapping map[schema.GroupVersionKind]schema.GroupVersionResource
}

func (r *realResourceResolver) Start(refreshInterval time.Duration, stopCh <-chan struct{}, log logr.Logger) {
	go func() {

		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()

		for {
			r.refresh(log)

			select {
			case <-stopCh:
				return
			case <-ticker.C:
			}
		}
	}()
}

func (r *realResourceResolver) refresh(log logr.Logger) {
	_, resources, err := r.dc.ServerGroupsAndResources()
	if err != nil {
		log.Error(err, "Unable to fetch server resources")
		return
	}

	mapping := make(map[schema.GroupVersionKind]schema.GroupVersionResource)
	for _, apiResourceList := range resources {
		gv, err := schema.ParseGroupVersion(apiResourceList.GroupVersion)
		if err != nil {
			log.Error(err, "Error parsing group version", "groupVersion", apiResourceList.GroupVersion)
			continue
		}
		for _, apiResource := range apiResourceList.APIResources {
			gvk := gv.WithKind(apiResource.Kind)
			gvr := gv.WithResource(apiResource.Name)
			// temporary fix to avoid adding subResource. For example, backups and
			// backups/status shared the same gvk.
			if !strings.Contains(apiResource.Name, "/") {
				mapping[gvk] = gvr
			}
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.resourceMapping = mapping
}

func (r *realResourceResolver) Resolve(gvk schema.GroupVersionKind) (schema.GroupVersionResource, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, found := r.resourceMapping[gvk]
	return item, found
}
