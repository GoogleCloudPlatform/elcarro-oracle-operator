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

package lro

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	log "k8s.io/klog/v2"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/database/lib/detach"
)

const (
	// TaskIDMetadataTag is the tag for the task ID in gRPC metadata.
	TaskIDMetadataTag = "taskID"

	taskTimeOutMetadataTag = "taskTimeoutSec"

	// JobStartIndicator is a log line we use to identify when the job has been started.
	JobStartIndicator = "CreateAndRunLROJobWithID: Create and run LRO job with id %v"
)

const (
	completionStatusOK    = "OK"
	completionStatusError = "Error"
)

// Job represents a long running operation and its metadata.
type Job struct {
	id   string
	name string

	resp *anypb.Any
	err  error

	lro *Server

	call func(ctx context.Context) (proto.Message, error)
	task *detach.Task
}

// Cancel cancels the job.
func (j *Job) Cancel() error {
	log.Infof("Cancel: job [%s] is cancelled", j.id)
	j.task.Cancel()
	return nil
}

// Delete deletes the job.
// It's not implemented yet.
func (j *Job) Delete() error {
	if j.IsDone() {
		return nil
	}
	return status.Errorf(codes.Aborted, "Can't delete job with ID %q while it's still running", j.id)
}

// Status gets the current status of the job, returning error or response on completion.
func (j *Job) Status() (bool, *anypb.Any, error) {
	if !j.IsDone() {
		return false, nil, nil
	}

	if j.err != nil {
		log.Errorf("Job %v failed with error=%v", j.id, j.err)
		return true, nil, j.err
	}

	return true, j.resp, nil
}

// Wait waits for the job to complete or timeout.
func (j *Job) Wait(timeout time.Duration) error {
	var err error
	select {
	case <-j.task.Finished():
		log.Infof("Job with ID %q has finished", j.id)
		err = nil
	case <-time.After(timeout):
		log.Infof("Job with ID %q has timed out", j.id)
		err = status.Errorf(codes.DeadlineExceeded, "LRO job with ID %q didn't complete in time", j.id)
	}

	return err
}

// catchPanic catches the panic to prevent the program from being shut down, and properly handles
// the state of the job.
func catchPanic(j *Job, f func(context.Context)) func(context.Context) {
	return func(ctx context.Context) {
		defer func() {
			if r := recover(); r != nil {
				e := fmt.Errorf("caught panic in agent execution. Panic Message: %v", r)
				log.Error(e)
				j.err = e

				j.lro.EndOperation(j.id, completionStatusError)
			}
		}()
		f(ctx)
	}
}

func taskTimeout(context context.Context) (time.Duration, error) {
	md, ok := metadata.FromIncomingContext(context)
	if !ok {
		return 0, fmt.Errorf("context has no timeout info")
	}

	data := md.Get(taskTimeOutMetadataTag)
	if len(data) == 0 || data[0] == "" {
		return 0, fmt.Errorf("fails to parse out the timeout info")
	}

	if len(data) > 1 {
		log.Warningf("taskTimeout: More than one task id in the metadata %v", data)
	}

	return time.ParseDuration(fmt.Sprintf("%ss", data[0]))
}

// start uses detach.Go to start an async job.
func (j *Job) start(ctx context.Context) {
	log.Infof("Start job with ID %s", j.id)
	timeOutDuration, _ := taskTimeout(ctx)
	task := detach.Go(catchPanic(j, func(jobCtx context.Context) {
		var resp proto.Message
		if timeOutDuration > 0 {
			var cancel context.CancelFunc
			jobCtx, cancel = context.WithTimeout(jobCtx, timeOutDuration)
			defer cancel()
		}

		resp, j.err = j.call(jobCtx)
		if resp == nil {
			j.resp = nil
		} else if any, ok := resp.(*anypb.Any); ok {
			j.resp = any
		} else {
			any := &anypb.Any{}
			if err := any.MarshalFrom(resp); err != nil {
				j.err = status.Errorf(codes.Internal, "Failed to marshal response to any: %v", err)
			}
			j.resp = any
		}
		if j.err == nil {
			j.lro.EndOperation(j.id, completionStatusOK)
		} else {
			j.lro.EndOperation(j.id, completionStatusError)
		}
	}))
	j.task = &task
}

// IsDone returns whether the job is done.
func (j *Job) IsDone() bool {
	select {
	case <-j.task.Finished():
		return true
	default:
		return false
	}
}

// ID returns the ID of the job.
func (j *Job) ID() string {
	return j.id
}

// Name returns the name of the job.
func (j *Job) Name() string {
	return j.name
}

// CreateJobID creates a new job id based on uuid.
func CreateJobID() string {
	return "Job" + "_" + uuid.New().String()
}

func addAndStartJob(ctx context.Context, lro *Server, job *Job) (*Job, error) {
	if err := lro.AddJob(job.id, job); err != nil {
		if status.Code(err) == codes.AlreadyExists {
			log.Warningf("LRO with job id %q already exists", job.id)
			return job, nil
		}

		return nil, fmt.Errorf("failed to add job for id=%v: %w", job.id, err)
	}
	log.Infof(JobStartIndicator, job.id)
	job.start(ctx)
	return job, nil

}

// CreateAndRunLROJobWithID creates an LRO job that can be cancelled.
// The method passed in is the main part of the call,
// adding into the job set and then starting it.
// It uses the given  lro job id as the id.
var CreateAndRunLROJobWithID = func(ctx context.Context, id, name string, lro *Server, call func(ctx context.Context) (proto.Message, error)) (*Job, error) {
	if id == "" {
		id = CreateJobID()
	}
	job := &Job{
		id:   id,
		name: name,
		call: call,
		lro:  lro,
	}

	return addAndStartJob(ctx, lro, job)
}

// CreateAndRunLROJobWithContext creates an LRO job that can be cancelled.
// The method passed in is the main part of the call,
// adding into the job set and then starting it.
// It pulls the job id from grpc context.
func CreateAndRunLROJobWithContext(ctx context.Context, name string, lro *Server, call func(ctx context.Context) (proto.Message, error)) (*Job, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return CreateAndRunLROJobWithID(ctx, "", name, lro, call)
	}
	var id string
	data := md.Get(TaskIDMetadataTag)
	if len(data) > 0 && data[0] != "" {
		if len(data) > 1 {
			log.Warningf("More than one task id in the metadata %v", data)
		}
		id = data[0]
	}
	return CreateAndRunLROJobWithID(ctx, id, name, lro, call)
}
