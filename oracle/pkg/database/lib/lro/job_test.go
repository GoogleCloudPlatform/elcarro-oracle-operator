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
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

const (
	panicMsg        = "fakeJob panicked"
	fakeJobWaitTime = time.Second
)

type testEnv struct {
	resp        proto.Message
	shouldPanic bool
	err         error
	done        chan struct{}
}

func (t testEnv) fakeJob(context.Context) (proto.Message, error) {
	if t.done != nil {
		<-t.done
	}

	if t.shouldPanic {
		panic(panicMsg)
	}

	if t.err != nil {
		return nil, t.err
	}
	return t.resp, t.err
}

func TestCreateAndRunLROJobWithID(t *testing.T) {
	ctx := context.Background()
	lro := NewServer(context.Background())
	lroID := "TestCreateAndRunLROJob"

	jobFunc := func(context.Context) (proto.Message, error) {
		return nil, nil
	}

	if job, err := CreateAndRunLROJobWithID(ctx, lroID, "Test", lro, jobFunc); err != nil || job == nil {
		t.Errorf("CreateAndRunLROJobWithID failed to create LRO job with err=%v.", err)
	}

	if job, err := CreateAndRunLROJobWithID(ctx, lroID, "Test", lro, jobFunc); err != nil || job == nil {
		t.Errorf("CreateAndRunLROJobWithID failed to create LRO job with the same id with err=%v.", err)
	}
}

func TestCreateAndRunLROJobWithContext(t *testing.T) {
	lro := NewServer(context.Background())
	id := "TestCreateAndRunLROJob"

	jobFunc := func(context.Context) (proto.Message, error) {
		return nil, nil
	}

	md := metadata.New(
		map[string]string{
			"taskID": id,
		})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	if job, err := CreateAndRunLROJobWithContext(ctx, "Test", lro, jobFunc); err != nil || job == nil {
		t.Errorf("CreateAndRunLROJobWithContext failed to create LRO job with err=%v.", err)
	}

	if job, err := CreateAndRunLROJobWithContext(ctx, "Test", lro, jobFunc); err != nil || job == nil {
		t.Errorf("CreateAndRunLROJobWithContext failed to create LRO job with the same id with err=%v.", err)
	}
}

func TestCreateAndRunLROJobWithTimeout(t *testing.T) {
	ctx := context.TODO()
	lro := NewServer(ctx)

	jobFunc := func(jobCtx context.Context) (proto.Message, error) {
		if _, deadlineSet := jobCtx.Deadline(); !deadlineSet {
			t.Errorf("ctx.Deadline() = _, false, want deadline set")
		}
		return nil, nil
	}

	md := metadata.New(
		map[string]string{
			taskTimeOutMetadataTag: "3",
		})

	ctx = metadata.NewIncomingContext(ctx, md)

	_, _ = CreateAndRunLROJobWithContext(ctx, "Test", lro, jobFunc)
}

func TestCancelJob(t *testing.T) {
	tests := []struct {
		name              string
		returnImmediately bool
		wantErr           bool
	}{
		{
			name:    "CancelFinished",
			wantErr: false,
		},
		{
			name:              "CancelRunning",
			returnImmediately: false,
			wantErr:           false,
		},
	}
	ctx := context.Background()
	lro := NewServer(ctx)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv{}
			if !tc.returnImmediately {
				env.done = make(chan struct{})
			}
			fakeJob := &Job{call: env.fakeJob, lro: lro}
			fakeJob.start(ctx)
			if tc.returnImmediately {
				if err := fakeJob.Wait(fakeJobWaitTime); err != nil {
					t.Error("fakeJob timed out")
				}
			}
			if err := fakeJob.Cancel(); tc.wantErr != (err != nil) {
				t.Errorf("TestCancelJob(%v) failed: gotErr=%v,wantErr =%v", tc.name, err, tc.wantErr)
			}
			if !tc.returnImmediately {
				close(env.done)
			}
		})
	}
}

func TestDeleteJob(t *testing.T) {
	tests := []struct {
		name              string
		returnImmediately bool
		expectedError     bool
	}{
		{
			name:              "DeleteSuccess",
			returnImmediately: true,
			expectedError:     false,
		},
		{
			name:              "DeleteFail",
			returnImmediately: false,
			expectedError:     true,
		},
	}
	ctx := context.Background()
	lro := NewServer(ctx)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv{}
			if !tc.returnImmediately {
				env.done = make(chan struct{})
			}

			fakeJob := &Job{call: env.fakeJob, lro: lro}
			fakeJob.start(ctx)
			if tc.returnImmediately {
				if err := fakeJob.Wait(fakeJobWaitTime); err != nil {
					t.Error("fakeJob timed out")
				}
			}
			if err := fakeJob.Delete(); tc.expectedError != (err != nil) {
				t.Errorf("TestDeleteJob(%v) failed. expected error: %v.", tc.name, tc.expectedError)
			}

			if !tc.returnImmediately {
				close(env.done)
			}
		})
	}
}

func TestWaitJob(t *testing.T) {
	tests := []struct {
		name              string
		err               error
		returnImmediately bool
		expectedError     bool
	}{
		{
			name:              "WaitSuccess",
			err:               status.Error(codes.Unknown, "Fail"),
			returnImmediately: true,
			expectedError:     false,
		},
		{
			name:              "WaitTimeOut",
			err:               status.Error(codes.Unknown, "Fail"),
			returnImmediately: false,
			expectedError:     true,
		},
	}
	ctx := context.Background()
	lro := NewServer(ctx)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv{}
			if !tc.returnImmediately {
				env.done = make(chan struct{})
			}

			fakeJob := &Job{call: env.fakeJob, lro: lro}
			fakeJob.start(ctx)

			if err := fakeJob.Wait(fakeJobWaitTime); tc.expectedError != (err != nil) {
				t.Errorf("TestWaitJob(%v) failed. Wait expected error: %v.", tc.name, tc.expectedError)
			}

			if !tc.returnImmediately {
				close(env.done)
			}
		})
	}
}

func TestGetStatus(t *testing.T) {
	tests := []struct {
		name              string
		err               error
		returnImmediately bool
		expectedFinished  bool
		expectedError     bool
	}{
		{
			name:              "FinishedSuccess",
			err:               nil,
			returnImmediately: true,
			expectedFinished:  true,
			expectedError:     false,
		},
		{
			name:              "FinishedWithError",
			err:               status.Error(codes.Unknown, "Fail"),
			returnImmediately: true,
			expectedFinished:  true,
			expectedError:     true,
		},
		{
			name:              "NotFinished",
			err:               status.Error(codes.Unknown, "Fail"),
			returnImmediately: false,
			expectedFinished:  false,
			expectedError:     false,
		},
	}
	ctx := context.Background()
	lro := NewServer(ctx)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			envJobResponse := &dbdpb.FileExistsResponse{Exists: true}
			env := &testEnv{err: tc.err, resp: envJobResponse}
			if !tc.returnImmediately {
				env.done = make(chan struct{})
			}

			fakeJob := &Job{call: env.fakeJob, lro: lro}
			fakeJob.start(ctx)

			// Call GetStatus multiple times and expect to get the same result
			for i := 0; i < 2; i++ {
				if tc.returnImmediately {
					if err := fakeJob.Wait(fakeJobWaitTime); err != nil {
						t.Error("fakeJob timed out")
					}
				}
				finished, result, err := fakeJob.Status()

				if tc.expectedFinished != finished {
					t.Errorf("TestGetStatus(%v) failed. GetStatus returns finished=%v when %v is expected", tc.name, finished, tc.expectedFinished)
				}

				if tc.expectedError != (err != nil) {
					t.Errorf("TestGetStatus(%v) failed. GetStatus expected error: %v", tc.name, tc.expectedError)
				}

				if tc.expectedFinished && !tc.expectedError {
					if result == nil {
						t.Errorf("TestGetStatus(%v) failed. GetStatus returns no result when result is expected.", tc.name)
					} else {
						message, err := result.UnmarshalNew()
						if err != nil {
							t.Errorf("TestGetStatus(%v) failed. UnmarshalNew returned an error %v.", tc.name, err)
						}
						feResult := message.(*dbdpb.FileExistsResponse)
						if diff := cmp.Diff(envJobResponse, feResult, protocmp.Transform()); diff != "" {
							t.Errorf("response wrong -want +got: %v", diff)
						}

					}
				} else if result != nil {
					t.Errorf("TestGetStatus(%v) failed.  GetStatus returns result %v when it shouldn't.", tc.name, result)
				}
			}

			if !tc.returnImmediately {
				close(env.done)
			}
		})
	}
}

func TestCatchPanic(t *testing.T) {
	ctx := context.Background()
	lro := NewServer(ctx)
	env := &testEnv{shouldPanic: true, resp: &dbdpb.FileExistsResponse{Exists: true}}
	fakeJob := &Job{call: env.fakeJob, lro: lro}
	fakeJob.start(ctx)

	if err := fakeJob.Wait(fakeJobWaitTime); err != nil {
		t.Errorf("fakeJob.Wait returned error: %v", err)
	}

	if fakeJob.resp != nil {
		t.Errorf("fakeJob.resp = %v; want nil", fakeJob.resp)
	}
	if !strings.Contains(fakeJob.err.Error(), panicMsg) {
		t.Errorf("got error %s; want %s", fakeJob.err.Error(), panicMsg)
	}
}
