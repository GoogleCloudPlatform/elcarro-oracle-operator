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

// Package lro contains an implementation of
// https://pkg.go.dev/google.golang.org/genproto/googleapis/longrunning#OperationsServer
package lro

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	opspb "google.golang.org/genproto/googleapis/longrunning"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	log "k8s.io/klog/v2"
)

const (
	defaultPageSize int = 10
	// DefaultWaitOperationTimeOut is the timeout for WaitOperation.
	DefaultWaitOperationTimeOut = 1 * time.Hour

	ttlAfterDelete   = 10 * time.Minute
	ttlAfterComplete = 12 * time.Hour

	jobCleanupInterval = time.Minute
)

type job interface {
	// Cancel errors if the job is not cancelable.
	Cancel() error
	// Delete is called on job deletion to clean up resources held by the job.
	Delete() error
	// done, result, error.  This is done in one call to be thread safe.
	Status() (bool, *anypb.Any, error)
	// Waits until the task is done: result, error.  This should use wait groups or something else to do an async wait.
	Wait(timeout time.Duration) error
	// IsDone returns if the job has completed.
	IsDone() bool
	// Name returns the job name for metrics/logging purposes.
	Name() string
}

type ttlJob struct {
	job          job
	startTime    time.Time
	completeTime time.Time

	mu         sync.Mutex
	deleteTime time.Time
}

// Server is a gRPC based operation server which
// implements google/longrunning/operations.proto .
type Server struct {
	mu   sync.Mutex
	jobs map[string]*ttlJob
}

// GetOperation gets the status of the LRO operation.
// It is the implementation of GetOperation in
// google/longrunning/operations.proto.
func (s *Server) GetOperation(_ context.Context, request *opspb.GetOperationRequest) (*opspb.Operation, error) {
	job, err := s.validateAndGetOperation(request.GetName())
	if err != nil {
		return nil, err
	}

	jobID := request.GetName()
	resp := GetOperationData(jobID, job.job)

	return resp, nil
}

// CancelOperation cancels a long running operation.
// It is the implementation of CancelOperation
// in google/longrunning/operations.proto.
func (s *Server) CancelOperation(_ context.Context, request *opspb.CancelOperationRequest) (*emptypb.Empty, error) {
	job, err := s.validateAndGetOperation(request.GetName())
	if err != nil {
		return nil, err
	}

	return &emptypb.Empty{}, job.job.Cancel()
}

// ListOperations is part of google/longrunning/operations.proto.
// It is not implemented fully yet.
func (s *Server) ListOperations(_ context.Context, request *opspb.ListOperationsRequest) (*opspb.ListOperationsResponse, error) {
	pageSize := int(request.GetPageSize())
	if pageSize == 0 {
		pageSize = defaultPageSize
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Zip through the jobs
	var operations []*opspb.Operation
	var nextID string
	for _, id := range sortedMapKeys(s.jobs) {
		// Skip until the index is past the next page token id.
		if request.GetPageToken() == "" || request.GetPageToken() <= id {
			if len(operations) >= pageSize {
				nextID = id
				break
			}
			job := s.jobs[id]
			operations = append(operations, GetOperationData(id, job.job))
		}
	}
	return &opspb.ListOperationsResponse{Operations: operations, NextPageToken: nextID}, nil
}

// DeleteOperation is part of google/longrunning/operations.proto.
func (s *Server) DeleteOperation(_ context.Context, request *opspb.DeleteOperationRequest) (*emptypb.Empty, error) {
	job, err := s.validateAndGetOperation(request.GetName())
	if err != nil {
		return nil, err
	}

	job.mu.Lock()
	defer job.mu.Unlock()
	job.deleteTime = time.Now()

	return &emptypb.Empty{}, nil
}

// WaitOperation is part of google/longrunning/operations.proto.
func (s *Server) WaitOperation(_ context.Context, request *opspb.WaitOperationRequest) (*opspb.Operation, error) {
	job, err := s.validateAndGetOperation(request.GetName())
	if err != nil {
		return nil, err
	}

	duration := DefaultWaitOperationTimeOut
	if timeout := request.GetTimeout(); timeout != nil {
		err = timeout.CheckValid()
		if err != nil {
			return nil, grpcstatus.Errorf(codes.InvalidArgument, "Invalid timeout %v for WaitOperation", timeout)
		}
		duration = timeout.AsDuration()
	}

	j := job.job
	// Wait for the operation to finish and then return the result.
	if err := j.Wait(duration); err != nil {
		// Error on the wait itself.
		log.Infof("WaitOperation: failed to wait for job %v error=%v", request.GetName(), err)
		return nil, err
	}

	return GetOperationData(request.GetName(), j), nil
}

// DeleteExpiredJobs deletes the jobs that are considered as expired.
func (s *Server) DeleteExpiredJobs(ttlAfterDelete time.Duration, ttlAfterComplete time.Duration) {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	for id, j := range s.jobs {
		shouldDelete := false

		// Check if Delete has been explicitly called for this job
		if isDeletedJobExpired(j, now, ttlAfterDelete) {
			shouldDelete = true
		}

		// Check if the jobs has completed for some time
		if j.completeTime.IsZero() && j.job.IsDone() {
			j.completeTime = now
		}

		if !j.completeTime.IsZero() && now.Sub(j.completeTime) > ttlAfterComplete {
			shouldDelete = true
		}

		if shouldDelete {
			delete(s.jobs, id)
			if err := j.job.Delete(); err != nil {
				log.Warning("Job %v deletion returned an error: %v", id, err)
			} else {
				log.Infof("Job %v has been deleted.", id)
			}
		}
	}
}

func (s *Server) validateAndGetOperation(operationID string) (*ttlJob, error) {
	if operationID == "" {
		return nil, grpcstatus.Error(codes.InvalidArgument, "bad request: empty operation ID")
	}

	job, ok := s.getJob(operationID)
	if !ok {
		return nil, grpcstatus.Errorf(codes.NotFound, "LRO with ID %q NOT found", operationID)
	}
	return job, nil
}

// AddJob adds a job into the server to be tracked.
func (s *Server) AddJob(id string, job job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; ok {
		log.Warningf("Job %v already exists", id)
		return grpcstatus.Errorf(codes.AlreadyExists, "LRO with ID %q already exists", id)
	}

	// Start the operation if we know it doesn't exist.
	s.startOperation(job.Name())
	s.jobs[id] = &ttlJob{job: job, startTime: time.Now()}
	return nil
}

func (s *Server) getJob(id string) (*ttlJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	return j, ok
}

func (s *Server) deleteJob(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
	log.Infof("Job %v has been deleted.", id)
}

func cleanup(ctx context.Context, lro *Server) {
	log.Info("Starting cleanup goroutine.")
	tick := time.NewTicker(jobCleanupInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			lro.DeleteExpiredJobs(ttlAfterDelete, ttlAfterComplete)
		}
	}
}

// NewServer returns Long running operation server.
func NewServer(ctx context.Context) *Server {
	lro := &Server{
		jobs: make(map[string]*ttlJob),
	}
	go cleanup(ctx, lro)
	return lro
}

// EndOperation records the result of the operation.
func (s *Server) EndOperation(id string, status string) {
	if job, ok := s.getJob(id); ok {
		log.Infof("EndOperation: job %v status %v", job.job.Name(), status)
	}
}

// WaitAndUnmarshalResult waits until the operation with the opName finishes,
// and either populates the result or the error.
func (s *Server) WaitAndUnmarshalResult(ctx context.Context, opName string, targetProto proto.Message) error {
	op, err := s.WaitOperation(ctx, &opspb.WaitOperationRequest{Name: opName})
	if err != nil {
		return fmt.Errorf("WaitOperation returns error: %v", err)
	}
	if op.GetError() != nil {
		return errors.New(op.GetError().GetMessage())
	}
	if op.GetResponse() == nil || targetProto == nil {
		return nil
	}
	return op.GetResponse().UnmarshalTo(targetProto)
}

func (s *Server) startOperation(name string) {
	log.Infof("startOperation: job %v", name)
}

// GetOperationData fills in the operation data for this specific job.
func GetOperationData(id string, j job) *opspb.Operation {
	done, result, e := j.Status()
	return BuildOperation(id, done, result, e)
}

// BuildOperation builds the operation response for this specific grpcstatus.
func BuildOperation(id string, done bool, result *anypb.Any, e error) *opspb.Operation {
	// Nothing to return at all.
	if result == nil && e == nil {
		return &opspb.Operation{Done: done, Name: id}
	}
	// Can return partial results
	if e != nil {
		if st, ok := grpcstatus.FromError(e); ok {
			return &opspb.Operation{Done: done, Name: id, Result: &opspb.Operation_Error{
				Error: st.Proto(),
			}}
		}

		return &opspb.Operation{Done: done, Name: id, Result: &opspb.Operation_Error{
			Error: &status.Status{
				Code:    int32(codes.Unknown),
				Message: e.Error(),
			},
		}}
	}
	return &opspb.Operation{Done: done, Name: id, Result: &opspb.Operation_Response{
		Response: result,
	}}
}

// sortedMapKeys is used in ListOperation to make sure everything is in order.
func sortedMapKeys(m map[string]*ttlJob) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func isDeletedJobExpired(job *ttlJob, now time.Time, ttl time.Duration) bool {
	job.mu.Lock()
	defer job.mu.Unlock()

	return !job.deleteTime.IsZero() && now.Sub(job.deleteTime) > ttl
}
