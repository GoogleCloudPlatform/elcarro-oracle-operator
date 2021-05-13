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

// Package detach provides basic blocks for running goroutines
// with a detached context.
package detach

import (
	"context"
	"sync"
	"time"
)

type ctx struct {
	done   <-chan struct{}
	closed <-chan struct{}
}

func (c ctx) Value(key interface{}) interface{}       { return nil }
func (c ctx) Deadline() (deadline time.Time, ok bool) { return }
func (c ctx) Done() <-chan struct{}                   { return c.done }

func (c ctx) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

// A Task describes the status of a detached function call started by calling Go.
type Task struct {
	cancel     chan<- struct{}
	cancelOnce *sync.Once
	closed     <-chan struct{}
}

// Go starts a new goroutine with a detached context and
// returns a Task that can be used to cancel and/or wait for the function to
// return.
func Go(f func(context.Context)) Task {
	closeChan := make(chan struct{})
	cancelChan := make(chan struct{})
	dCtx := ctx{
		closed: closeChan,
		done:   cancelChan,
	}

	go func() {
		defer close(closeChan)
		f(dCtx)
	}()

	return Task{cancelChan, new(sync.Once), closeChan}
}

// Cancel cancels a Task.
// After the first call, subsequent calls to Cancel do nothing.
func (t Task) Cancel() {
	t.cancelOnce.Do(func() { close(t.cancel) })
}

// Finished returns a channel that is closed on the task function completion.
func (t Task) Finished() <-chan struct{} { return t.closed }
