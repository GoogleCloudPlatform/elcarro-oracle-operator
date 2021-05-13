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
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	lrspb "google.golang.org/genproto/googleapis/longrunning"
	opspb "google.golang.org/genproto/googleapis/longrunning"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/anypb"
	log "k8s.io/klog/v2"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestGetOperationDataPreserveErrorCode(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		expectedCode int32
	}{
		{
			name:         "grpc status",
			err:          status.Error(codes.Unimplemented, "Fail"),
			expectedCode: 12,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {

			f := &fakeJob{e: tc.err}

			err := GetOperationData("", f).GetError()
			if err.Code != tc.expectedCode {
				t.Errorf("Error code: Want %d; Got %d", tc.expectedCode, err.Code)
				t.Errorf("Error %v", err)
			}
		})
	}
}

func TestGetOperation(t *testing.T) {
	marshalledResponse, err := anypb.New(&lrspb.OperationInfo{ResponseType: "frog"})
	if err != nil {
		log.Errorf("Failed to marshal operation %v", err)
		return
	}
	tests := []struct {
		name             string
		id               string
		getStatusError   error
		response         *lrspb.OperationInfo // Using this class just to be different from the others.
		expectedResponse *lrspb.Operation
		expectedError    bool
		done             bool
		numGetStatus     int
	}{
		{
			name:           "Undone GetOperation error status",
			id:             "frog",
			getStatusError: status.Error(codes.Unknown, "Fail"),
			numGetStatus:   1,
			response:       &lrspb.OperationInfo{},
			expectedResponse: &lrspb.Operation{
				Name: "frog",
				Done: false,
				Result: &opspb.Operation_Error{
					Error: status.Convert(status.Error(codes.Unknown, "Fail")).Proto(),
				},
			},
		},
		{
			name:           "Done GetOperation error status",
			id:             "frog",
			getStatusError: status.Error(codes.Unknown, "Fail"),
			response:       &lrspb.OperationInfo{ResponseType: "frog"},
			numGetStatus:   1,
			done:           true,
			expectedResponse: &lrspb.Operation{
				Name: "frog",
				Done: true,
				Result: &opspb.Operation_Error{
					Error: status.Convert(status.Error(codes.Unknown, "Fail")).Proto(),
				},
			},
		},
		{
			name:         "Undone GetOperation",
			response:     &lrspb.OperationInfo{ResponseType: "frog"},
			id:           "frog",
			numGetStatus: 1,
			expectedResponse: &lrspb.Operation{
				Name: "frog",
				Done: false,
				Result: &opspb.Operation_Response{
					Response: marshalledResponse,
				},
			},
		},
		{
			name:         "Done GetOperation",
			response:     &lrspb.OperationInfo{ResponseType: "frog"},
			id:           "frog",
			numGetStatus: 1,
			done:         true,
			expectedResponse: &lrspb.Operation{
				Name: "frog",
				Done: true,
				Result: &opspb.Operation_Response{
					Response: marshalledResponse,
				},
			},
		},
		{
			name:           "GetOperation no job",
			id:             "",
			response:       &lrspb.OperationInfo{ResponseType: "frog"},
			getStatusError: status.Error(codes.Unknown, "Fail"),
			numGetStatus:   1,
			expectedError:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {

			lro := NewServer(context.Background())
			var f *fakeJob
			if tc.id != "" {
				res, err := anypb.New(tc.response)
				if err != nil {
					t.Fatalf("error marshaling response=%v", err)
				}
				f = &fakeJob{e: tc.getStatusError, response: res, done: tc.done}
				_ = lro.AddJob(tc.id, f)
			}

			response, err := lro.GetOperation(context.Background(), &lrspb.GetOperationRequest{Name: tc.id})

			if tc.expectedError != (err != nil) {
				t.Errorf("error return %v expected=%v", err, tc.expectedError)
			}

			if f != nil {
				if tc.numGetStatus != f.numGetStatus {
					t.Errorf("getStatus wrong attempts: %v, expected %v", f.numGetStatus, tc.numGetStatus)
				}
			}

			if diff := cmp.Diff(tc.expectedResponse, response, protocmp.Transform()); diff != "" {
				t.Errorf("response wrong -want +got: %v", diff)
			}
		})
	}
}

func TestCancelOperation(t *testing.T) {
	tests := []struct {
		name          string
		id            string
		cancelError   error
		expectedError bool
		numCancels    int
	}{
		{
			name:          "CancelOperation error status",
			id:            "frog",
			cancelError:   status.Error(codes.Unknown, "Fail"),
			numCancels:    1,
			expectedError: true,
		},
		{
			name:          "CancelOperation success",
			id:            "frog",
			numCancels:    1,
			expectedError: false,
		},
		{
			name:          "CancelOperation missing",
			id:            "",
			numCancels:    0,
			expectedError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {

			lro := NewServer(context.Background())
			var f *fakeJob
			if tc.id != "" {
				res, err := anypb.New(&lrspb.OperationInfo{ResponseType: "frog"})
				if err != nil {
					t.Fatalf("error marshalling response %v", err)
				}
				f = &fakeJob{e: tc.cancelError, response: res, done: true}
				_ = lro.AddJob(tc.id, f)
			}

			_, err := lro.CancelOperation(context.Background(), &lrspb.CancelOperationRequest{Name: tc.id})

			if tc.expectedError != (err != nil) {
				t.Errorf("error got %v expected error=%v", err, tc.expectedError)
			}

			if f != nil {
				if tc.numCancels != f.numCancels {
					t.Errorf("failed. Response wrong: Was %v, expected %v", f.numCancels, tc.numCancels)
				}
			}
		})
	}
}

func TestEndOperation(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		status []string
	}{
		{
			name:   "single",
			id:     "frog",
			status: []string{"OK"},
		},
		{
			name:   "multiple",
			id:     "frog",
			status: []string{"OK", "Error", "OK"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lro := NewServer(context.Background())
			res, err := anypb.New(&lrspb.OperationInfo{ResponseType: "frog"})
			if err != nil {
				t.Fatalf("error marshalling response %v", err)
			}
			f := &fakeJob{name: tc.name, response: res, done: true}
			_ = lro.AddJob(tc.id, f)

			for _, val := range tc.status {
				lro.EndOperation(tc.id, val)
			}
		})
	}
}

func TestDeleteOperation(t *testing.T) {
	tests := []struct {
		name          string
		id            string
		expectedError bool
	}{
		{
			name:          "DeleteOperation success",
			id:            "frog",
			expectedError: false,
		},
		{
			name:          "DeleteOperation missing",
			id:            "",
			expectedError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {

			lro := NewServer(context.Background())
			var f *fakeJob
			if tc.id != "" {
				res, err := anypb.New(&lrspb.OperationInfo{ResponseType: "frog"})
				if err != nil {
					t.Fatalf("error marshaling response=%v", err)
				}

				f = &fakeJob{response: res}
				_ = lro.AddJob(tc.id, f)
				if _, ok := lro.getJob(tc.id); !ok {
					t.Errorf("job not added correctly")
				}
			}

			_, err := lro.DeleteOperation(context.Background(), &lrspb.DeleteOperationRequest{Name: tc.id})

			if tc.expectedError != (err != nil) {
				t.Errorf("got error %v expected=%v", err, tc.expectedError)
			}

			if f != nil {
				if f.numDeletes != 0 {
					t.Errorf("num deletes wrong: Was %v, expected 0", f.numDeletes)
				}
			}
			_, ok := lro.getJob(tc.id)
			if !tc.expectedError && !ok {
				t.Errorf("job should still exist %v", tc.id)
			}
		})
	}
}

func TestAddDuplicateJob(t *testing.T) {
	lro := NewServer(context.Background())
	newF := &fakeJob{}
	if err := lro.AddJob("frog", newF); err != nil {
		t.Errorf("AddJob(Duplicate) failed - AddJob got err: %v, expected no error", err)
	}
	if err := lro.AddJob("frog", newF); err == nil {
		t.Errorf("AddJob(Duplicate) failed - AddJob got no error: expected error")
	}
}

func TestWaitOperation(t *testing.T) {
	marshalledResponse, err := anypb.New(&lrspb.OperationInfo{ResponseType: "frog"})
	if err != nil {
		log.Errorf("Failed to marshal response %v", err)
		return
	}
	tests := []struct {
		name             string
		id               string
		waitError        error
		expectedError    bool
		numWaits         int
		expectedResponse *lrspb.Operation
	}{
		{
			name:             "error status",
			id:               "frog",
			waitError:        status.Error(codes.Unknown, "Fail"),
			numWaits:         1,
			expectedError:    true,
			expectedResponse: nil,
		},
		{
			name:          "success",
			id:            "frog",
			numWaits:      1,
			expectedError: false,
			expectedResponse: &lrspb.Operation{
				Name: "frog",
				Done: false,
				Result: &opspb.Operation_Response{
					Response: marshalledResponse,
				},
			},
		},
		{
			name:             "missing",
			id:               "",
			numWaits:         0,
			expectedError:    true,
			expectedResponse: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lro := NewServer(context.Background())
			var f *fakeJob
			if tc.id != "" {
				res, err := anypb.New(&lrspb.OperationInfo{ResponseType: "frog"})
				if err != nil {
					t.Fatalf("error marshaling response=%v", err)
				}

				f = &fakeJob{waitError: tc.waitError, response: res}
				_ = lro.AddJob(tc.id, f)
				if _, ok := lro.getJob(tc.id); !ok {
					t.Errorf("job not added correctly")
				}
			}

			response, err := lro.WaitOperation(context.Background(), &lrspb.WaitOperationRequest{Name: tc.id})

			if tc.expectedError != (err != nil) {
				t.Fatalf("errors got %v expected=%v", err, tc.expectedError)
			}

			if f != nil && tc.numWaits != f.numWaits {
				t.Errorf("wait wrong attempts: %v, expected %v", f.numWaits, tc.numWaits)
			}

			if diff := cmp.Diff(tc.expectedResponse, response, protocmp.Transform()); diff != "" {
				t.Errorf("response wrong -want +got: %v", diff)
			}
		})
	}
}

type ListJobStruct struct {
	id            string
	done          bool
	responseError error
}

func TestListOperation(t *testing.T) {
	marshalledResponse, err := anypb.New(&lrspb.OperationInfo{ResponseType: "frog"})
	if err != nil {
		log.Errorf("Failed to marshal operation %v", err)
		return
	}
	tests := []struct {
		name             string
		pageSize         int32
		pageToken        string
		jobs             []ListJobStruct
		expectedError    bool
		expectedResponse *lrspb.ListOperationsResponse
	}{
		{
			name: "ListOperation one",
			jobs: []ListJobStruct{
				{
					id:   "frog",
					done: true,
				},
			},
			expectedError: false,
			expectedResponse: &lrspb.ListOperationsResponse{
				Operations: []*lrspb.Operation{
					{
						Name: "frog",
						Done: true,
						Result: &lrspb.Operation_Response{
							Response: marshalledResponse,
						},
					},
				},
			},
		},
		{
			name:             "ListOperation empty",
			expectedError:    false,
			expectedResponse: &lrspb.ListOperationsResponse{},
		},
		{
			name:     "ListOperation limited",
			pageSize: 2,
			jobs: []ListJobStruct{
				{
					id:   "frog",
					done: true,
				},
				{
					id: "1",
				},
				{
					id: "2",
				},
			},
			expectedError: false,
			expectedResponse: &lrspb.ListOperationsResponse{
				NextPageToken: "frog",
				Operations: []*lrspb.Operation{
					{
						Name: "1",
						Done: false,
						Result: &lrspb.Operation_Response{
							Response: marshalledResponse,
						},
					},
					{
						Name: "2",
						Done: false,
						Result: &lrspb.Operation_Response{
							Response: marshalledResponse,
						},
					},
				},
			},
		},
		{
			name:      "ListOperation nextPage",
			pageSize:  2,
			pageToken: "frog",
			jobs: []ListJobStruct{
				{
					id:   "frog",
					done: true,
				},
				{
					id: "1",
				},
				{
					id: "2",
				},
			},
			expectedError: false,
			expectedResponse: &lrspb.ListOperationsResponse{
				NextPageToken: "",
				Operations: []*lrspb.Operation{
					{
						Name: "frog",
						Done: true,
						Result: &lrspb.Operation_Response{
							Response: marshalledResponse,
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {

			lro := NewServer(context.Background())
			for _, j := range tc.jobs {
				res, err := anypb.New(&lrspb.OperationInfo{ResponseType: "frog"})
				if err != nil {
					t.Errorf("error marshaling response=%v", err)
				}

				newF := &fakeJob{e: j.responseError, response: res, done: j.done}
				_ = lro.AddJob(j.id, newF)
			}

			response, err := lro.ListOperations(context.Background(), &lrspb.ListOperationsRequest{PageSize: tc.pageSize, PageToken: tc.pageToken})

			if tc.expectedError != (err != nil) {
				t.Errorf("error got %v, expected error=%v", err, tc.expectedError)
			}

			if diff := cmp.Diff(tc.expectedResponse, response, protocmp.Transform()); diff != "" {
				t.Errorf("response wrong -want +got: %v", diff)
			}
		})
	}
}

func TestDeleteExpiredJobs(t *testing.T) {
	tests := []struct {
		name         string
		completeTime time.Time
		deleteTime   time.Time
		jobDone      bool
		wantDelete   bool
	}{
		{
			name:       "Delete expired job with delete issued",
			deleteTime: time.Now().Add(-10 * time.Minute),
			wantDelete: true,
		},
		{
			name:       "Keep job with delete issued",
			deleteTime: time.Now().Add(time.Second),
		},
		{
			name:         "Delete completed job",
			completeTime: time.Now().Add(-10 * time.Minute),
			wantDelete:   true,
		},
		{
			name:         "Keep completed job",
			completeTime: time.Now().Add(time.Second),
			jobDone:      true,
		},
		{
			name:    "Keep completed job with completeTime updated",
			jobDone: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {

			lro := NewServer(context.Background())
			fj := &fakeJob{done: tc.jobDone}

			if err := lro.AddJob(tc.name, fj); err != nil {
				t.Errorf("lro.AddJob failed with err=%v", err)
			}

			job, _ := lro.getJob(tc.name)
			job.completeTime = tc.completeTime
			job.deleteTime = tc.deleteTime

			lro.DeleteExpiredJobs(5*time.Minute, 5*time.Minute)

			job, ok := lro.getJob(tc.name)
			if tc.wantDelete == ok {
				t.Errorf("wantDelete=%v, deleted=%v", tc.wantDelete, !ok)
			}

			if job != nil {
				if !tc.completeTime.IsZero() && job.completeTime != tc.completeTime {
					t.Errorf("completeTime has been updated while it shouldn't.")
				}

				if tc.jobDone && job.completeTime.IsZero() {
					t.Errorf("completeTime is empty.")
				}
			}
		})
	}
}

func TestMutexes(t *testing.T) {
	numLoops := 1000
	lro := &Server{
		jobs: make(map[string]*ttlJob),
	}

	done := make(chan bool)

	go func() {
		for range time.Tick(10 * time.Millisecond) {
			lro.DeleteExpiredJobs(0, time.Minute)
			select {
			case <-done:
				lro.DeleteExpiredJobs(0, time.Minute)
				close(done)
				return
			default:
				continue
			}
		}
	}()

	// Run a bunch of go routines to do a lot of stuff.
	var wg sync.WaitGroup
	wg.Add(numLoops)
	for i := 0; i < numLoops; i++ {
		go func() {
			id := uuid.New().String()
			defer wg.Done()
			newF := &fakeJob{}
			err := lro.AddJob(id, newF)
			if err != nil {
				t.Errorf("Mutexes(Main) AddJob(%v) error=%v", id, err)
			}
			if _, err := lro.CancelOperation(context.Background(), &opspb.CancelOperationRequest{Name: id}); err != nil {
				t.Errorf("Mutexes(Main) CancelOperation(%v) error=%v", id, err)
			}
			if _, err = lro.GetOperation(context.Background(), &opspb.GetOperationRequest{Name: id}); err != nil {
				t.Errorf("Mutexes(Main) GetOperation(%v) error=%v", id, err)
			}
			if _, err = lro.DeleteOperation(context.Background(), &opspb.DeleteOperationRequest{Name: id}); err != nil {
				t.Errorf("Mutexes(Main) DeleteOperation(%v) error=%v", id, err)
			}
		}()
	}

	wg.Wait()
	done <- true

	<-done

	if len(lro.jobs) != 0 {
		t.Errorf("Mutexex(Main) jobs still exist, should be empty")
	}
}

type fakeJob struct {
	name         string
	e            error
	waitError    error
	done         bool
	response     *anypb.Any
	numCancels   int
	numDeletes   int
	numGetStatus int
	numWaits     int
}

func (f *fakeJob) Cancel() error {
	f.numCancels++
	return f.e
}

func (f *fakeJob) Delete() error {
	f.numDeletes++
	return f.e
}

func (f *fakeJob) Wait(time.Duration) error {
	f.numWaits++
	return f.waitError
}

// done, result, error.  This is done in one call to be thread safe.
func (f *fakeJob) Status() (bool, *anypb.Any, error) {
	f.numGetStatus++
	return f.done, f.response, f.e
}

func (f *fakeJob) IsDone() bool {
	return f.done
}

func (f *fakeJob) Name() string {
	return f.name
}
