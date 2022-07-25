// Copyright 2022 Google LLC
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

// Package task implements tasks struct and its utility methods.
package task

import (
	"context"

	"k8s.io/klog/v2"
)

type task interface {
	GetName() string
	Call(ctx context.Context) error
}

// Tasks describes a composite task which contains a slice of tasks.
type Tasks struct {
	name  string
	tasks []task
}

// AddTask appends a new task to the end of the task slice.
func (t *Tasks) AddTask(name string, callFun func(context.Context) error) {
	t.tasks = append(t.tasks, &simpleTask{
		name:    name,
		callFun: callFun,
	})
}

// GetTaskNames returns all sub task names owned by tasks.
func (t *Tasks) GetTaskNames() []string {
	var names []string
	for _, task := range t.tasks {
		names = append(names, task.GetName())
	}
	return names
}

// simpleTask describes a simple task which should be testable.
type simpleTask struct {
	name    string
	callFun func(ctx context.Context) error
}

// GetName returns the name of this simple task.
func (task *simpleTask) GetName() string {
	return task.name
}

// Call triggers this simple task. It returns not-nil error if failed.
func (task *simpleTask) Call(ctx context.Context) error {
	return task.callFun(ctx)
}

// NewTasks returns a new tasks.
func NewTasks(ctx context.Context, name string) *Tasks {
	return &Tasks{
		name: name,
	}
}

// Do runs tasks sequentially and writes log messages to show running status.
// If i-th task failed, the method will return error directly(tasks after i will not be executed).
func Do(ctx context.Context, tasks *Tasks) error {
	klog.Infof("running %s", tasks.name)
	for _, sub := range tasks.tasks {
		klog.Infof("running %s:%s", tasks.name, sub.GetName())
		if err := sub.Call(ctx); err != nil {
			klog.Errorf("%s:%s failed with %v", tasks.name, sub.GetName(), err)
			return err
		}
		klog.Infof("%s:%s done", tasks.name, sub.GetName())
	}
	klog.Infof("%s done", tasks.name)
	return nil
}
