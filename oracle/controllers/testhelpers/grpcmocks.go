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

package testhelpers

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/golang/protobuf/ptypes/empty"
	"google.golang.org/genproto/googleapis/longrunning"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	capb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
)

// FakeOperationStatus is an enum type for LRO statuses managed by FakeConfigAgentClient.
type FakeOperationStatus int32

const (
	//StatusUndefined undefined.
	StatusUndefined FakeOperationStatus = iota
	//StatusRunning running.
	StatusRunning
	//StatusDone done.
	StatusDone
	//StatusDoneWithError done with error.
	StatusDoneWithError
	//StatusNotFound not found.
	StatusNotFound
)

// FakeConfigAgentClient a client for capturing calls the various ConfigAgent api.
type FakeConfigAgentClient struct {
	verifyPhysicalBackupCalledCnt  int32
	physicalBackupCalledCnt        int32
	physicalRestoreCalledCnt       int32
	createDatabaseCalledCnt        int32
	createUsersCalledCnt           int32
	usersChangedCalledCnt          int32
	updateUsersCalledCnt           int32
	checkStatusCalledCnt           int32
	createCDBCalledCnt             int32
	bootstrapCDBCalledCnt          int32
	bootstrapDatabaseCalledCnt     int32
	bootstrapStandbyCalledCnt      int32
	bounceDatabaseCalledCnt        int32
	createListenerCalledCnt        int32
	listOperationsCalledCnt        int32
	getOperationCalledCnt          int32
	deleteOperationCalledCnt       int32
	dataPumpImportCalledCnt        int32
	dataPumpExportCalledCnt        int32
	setParameterCalledCnt          int32
	getParameterTypeValueCalledCnt int32
	recoverConfigFileCalledCnt     int32

	lock                         sync.Mutex
	fetchServiceImageMetaDataCnt int32
	asyncPhysicalBackup          bool
	asyncPhysicalRestore         bool
	nextGetOperationStatus       FakeOperationStatus
}

var (
	emptyConnCloseFunc = func() {}
)

// FakeClientFactory is a simple factory to create our FakeConfigAgentClient.
type FakeClientFactory struct {
	Caclient *FakeConfigAgentClient
}

// New returns a new fake ConfigAgent.
func (g *FakeClientFactory) New(context.Context, client.Reader, string, string) (capb.ConfigAgentClient, controllers.ConnCloseFunc, error) {
	if g.Caclient == nil {
		g.Reset()
	}
	return g.Caclient, emptyConnCloseFunc, nil
}

// Reset clears the inner ConfigAgent.
func (g *FakeClientFactory) Reset() {
	g.Caclient = &FakeConfigAgentClient{}
}

// Reset reset's the config agent's counters.
func (cli *FakeConfigAgentClient) Reset() {
	*cli = FakeConfigAgentClient{}
}

// CreateDatabase wrapper.
func (cli *FakeConfigAgentClient) CreateDatabase(context.Context, *capb.CreateDatabaseRequest, ...grpc.CallOption) (*capb.CreateDatabaseResponse, error) {
	atomic.AddInt32(&cli.createCDBCalledCnt, 1)
	return nil, nil
}

// CreateUsers wrapper.
func (cli *FakeConfigAgentClient) CreateUsers(context.Context, *capb.CreateUsersRequest, ...grpc.CallOption) (*capb.CreateUsersResponse, error) {
	atomic.AddInt32(&cli.createUsersCalledCnt, 1)
	return nil, nil
}

// UsersChanged wrapper.
func (cli *FakeConfigAgentClient) UsersChanged(context.Context, *capb.UsersChangedRequest, ...grpc.CallOption) (*capb.UsersChangedResponse, error) {
	atomic.AddInt32(&cli.usersChangedCalledCnt, 1)
	return nil, nil
}

// UpdateUsers wrapper.
func (cli *FakeConfigAgentClient) UpdateUsers(context.Context, *capb.UpdateUsersRequest, ...grpc.CallOption) (*capb.UpdateUsersResponse, error) {
	atomic.AddInt32(&cli.updateUsersCalledCnt, 1)
	return nil, nil
}

// VerifyPhysicalBackup wrapper.
func (cli *FakeConfigAgentClient) VerifyPhysicalBackup(ctx context.Context, in *capb.VerifyPhysicalBackupRequest, opts ...grpc.CallOption) (*capb.VerifyPhysicalBackupResponse, error) {
	atomic.AddInt32(&cli.verifyPhysicalBackupCalledCnt, 1)
	return &capb.VerifyPhysicalBackupResponse{}, nil
}

// PhysicalBackup wrapper.
func (cli *FakeConfigAgentClient) PhysicalBackup(context.Context, *capb.PhysicalBackupRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	atomic.AddInt32(&cli.physicalBackupCalledCnt, 1)
	return &longrunning.Operation{Done: !cli.asyncPhysicalBackup}, nil
}

// PhysicalRestore wrapper.
func (cli *FakeConfigAgentClient) PhysicalRestore(context.Context, *capb.PhysicalRestoreRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	atomic.AddInt32(&cli.physicalRestoreCalledCnt, 1)
	return &longrunning.Operation{Done: !cli.asyncPhysicalRestore}, nil
}

// CheckStatus wrapper.
func (cli *FakeConfigAgentClient) CheckStatus(context.Context, *capb.CheckStatusRequest, ...grpc.CallOption) (*capb.CheckStatusResponse, error) {
	atomic.AddInt32(&cli.checkStatusCalledCnt, 1)
	return nil, nil
}

// CreateCDB wrapper.
func (cli *FakeConfigAgentClient) CreateCDB(context.Context, *capb.CreateCDBRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	atomic.AddInt32(&cli.createCDBCalledCnt, 1)
	return nil, nil
}

// CreateListener wrapper.
func (cli *FakeConfigAgentClient) CreateListener(context.Context, *capb.CreateListenerRequest, ...grpc.CallOption) (*capb.CreateListenerResponse, error) {
	atomic.AddInt32(&cli.createListenerCalledCnt, 1)
	return nil, nil
}

// ListOperations wrapper.
func (cli *FakeConfigAgentClient) ListOperations(context.Context, *longrunning.ListOperationsRequest, ...grpc.CallOption) (*longrunning.ListOperationsResponse, error) {
	atomic.AddInt32(&cli.listOperationsCalledCnt, 1)
	return nil, nil
}

// GetOperation wrapper.
func (cli *FakeConfigAgentClient) GetOperation(context.Context, *longrunning.GetOperationRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	atomic.AddInt32(&cli.getOperationCalledCnt, 1)

	switch cli.NextGetOperationStatus() {
	case StatusDone:
		return &longrunning.Operation{Done: true}, nil

	case StatusDoneWithError:
		return &longrunning.Operation{
			Done: true,
			Result: &longrunning.Operation_Error{
				Error: &status.Status{Code: int32(codes.Unknown), Message: "Test Error"},
			},
		}, nil

	case StatusRunning:
		return &longrunning.Operation{}, nil

	case StatusNotFound:
		return nil, grpcstatus.Errorf(codes.NotFound, "")

	case StatusUndefined:
		panic("Misconfigured test, set up expected operation status")

	default:
		panic(fmt.Sprintf("unknown status: %v", cli.NextGetOperationStatus()))
	}
}

// DeleteOperation wrapper.
func (cli *FakeConfigAgentClient) DeleteOperation(context.Context, *longrunning.DeleteOperationRequest, ...grpc.CallOption) (*empty.Empty, error) {
	atomic.AddInt32(&cli.deleteOperationCalledCnt, 1)
	return nil, nil
}

// CreateCDBUser wrapper.
func (cli *FakeConfigAgentClient) CreateCDBUser(context.Context, *capb.CreateCDBUserRequest, ...grpc.CallOption) (*capb.CreateCDBUserResponse, error) {
	atomic.AddInt32(&cli.createListenerCalledCnt, 1)
	return nil, nil
}

// BootstrapDatabase wrapper.
func (cli *FakeConfigAgentClient) BootstrapDatabase(context.Context, *capb.BootstrapDatabaseRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	atomic.AddInt32(&cli.bootstrapDatabaseCalledCnt, 1)
	return nil, nil
}

// BootstrapStandby wrapper.
func (cli *FakeConfigAgentClient) BootstrapStandby(context.Context, *capb.BootstrapStandbyRequest, ...grpc.CallOption) (*capb.BootstrapStandbyResponse, error) {
	atomic.AddInt32(&cli.bootstrapStandbyCalledCnt, 1)
	return nil, nil
}

// DataPumpImport wrapper.
func (cli *FakeConfigAgentClient) DataPumpImport(context.Context, *capb.DataPumpImportRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	atomic.AddInt32(&cli.dataPumpImportCalledCnt, 1)
	return &longrunning.Operation{Done: false}, nil
}

// DataPumpExport wrapper.
func (cli *FakeConfigAgentClient) DataPumpExport(context.Context, *capb.DataPumpExportRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	atomic.AddInt32(&cli.dataPumpExportCalledCnt, 1)
	return nil, nil
}

// BounceDatabase wrapper.
func (cli *FakeConfigAgentClient) BounceDatabase(context.Context, *capb.BounceDatabaseRequest, ...grpc.CallOption) (*capb.BounceDatabaseResponse, error) {
	atomic.AddInt32(&cli.bounceDatabaseCalledCnt, 1)
	return nil, nil
}

// DataPumpImportCalledCnt returns call count.
func (cli *FakeConfigAgentClient) DataPumpImportCalledCnt() int {
	return int(atomic.LoadInt32(&cli.dataPumpImportCalledCnt))
}

// DataPumpExportCalledCnt returns call count.
func (cli *FakeConfigAgentClient) DataPumpExportCalledCnt() int {
	return int(atomic.LoadInt32(&cli.dataPumpExportCalledCnt))
}

// DeleteOperationCalledCnt returns call count.
func (cli *FakeConfigAgentClient) DeleteOperationCalledCnt() int {
	return int(atomic.LoadInt32(&cli.deleteOperationCalledCnt))
}

// VerifyPhysicalBackupCalledCnt returns call count.
func (cli *FakeConfigAgentClient) VerifyPhysicalBackupCalledCnt() int {
	return int(atomic.LoadInt32(&cli.verifyPhysicalBackupCalledCnt))
}

// PhysicalBackupCalledCnt returns call count.
func (cli *FakeConfigAgentClient) PhysicalBackupCalledCnt() int {
	return int(atomic.LoadInt32(&cli.physicalBackupCalledCnt))
}

// PhysicalRestoreCalledCnt returns call count.
func (cli *FakeConfigAgentClient) PhysicalRestoreCalledCnt() int {
	return int(atomic.LoadInt32(&cli.physicalRestoreCalledCnt))
}

// GetOperationCalledCnt returns call count.
func (cli *FakeConfigAgentClient) GetOperationCalledCnt() int {
	return int(atomic.LoadInt32(&cli.getOperationCalledCnt))
}

// SetParameter wrapper.
func (cli *FakeConfigAgentClient) SetParameter(context.Context, *capb.SetParameterRequest, ...grpc.CallOption) (*capb.SetParameterResponse, error) {
	atomic.AddInt32(&cli.setParameterCalledCnt, 1)
	return nil, nil
}

// GetParameterTypeValue wrapper.
func (cli *FakeConfigAgentClient) GetParameterTypeValue(context.Context, *capb.GetParameterTypeValueRequest, ...grpc.CallOption) (*capb.GetParameterTypeValueResponse, error) {
	atomic.AddInt32(&cli.getParameterTypeValueCalledCnt, 1)
	return nil, nil
}

// RecoverConfigFile wrapper.
func (cli *FakeConfigAgentClient) RecoverConfigFile(ctx context.Context, in *capb.RecoverConfigFileRequest, opts ...grpc.CallOption) (*capb.RecoverConfigFileResponse, error) {
	atomic.AddInt32(&cli.recoverConfigFileCalledCnt, 1)
	return nil, nil
}

func (cli *FakeConfigAgentClient) FetchServiceImageMetaData(ctx context.Context, in *capb.FetchServiceImageMetaDataRequest, opts ...grpc.CallOption) (*capb.FetchServiceImageMetaDataResponse, error) {
	atomic.AddInt32(&cli.fetchServiceImageMetaDataCnt, 1)
	return nil, nil
}

func (cli *FakeConfigAgentClient) SetNextGetOperationStatus(status FakeOperationStatus) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	cli.nextGetOperationStatus = status
}

func (cli *FakeConfigAgentClient) NextGetOperationStatus() FakeOperationStatus {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	return cli.nextGetOperationStatus
}

func (cli *FakeConfigAgentClient) SetAsyncPhysicalBackup(async bool) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	cli.asyncPhysicalBackup = async
}

func (cli *FakeConfigAgentClient) SetAsyncPhysicalRestore(async bool) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	cli.asyncPhysicalRestore = async
}
