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
type FakeOperationStatus int

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
	PhysicalBackupCalledCnt        int
	PhysicalRestoreCalledCnt       int
	CreateDatabaseCalledCnt        int
	CreateUsersCalledCnt           int
	UsersChangedCalledCnt          int
	UpdateUsersCalledCnt           int
	CheckStatusCalledCnt           int
	CreateCDBCalledCnt             int
	BootstrapCDBCalledCnt          int
	BootstrapDatabaseCalledCnt     int
	BootstrapStandbyCalledCnt      int
	BounceDatabaseCalledCnt        int
	CreateListenerCalledCnt        int
	ListOperationsCalledCnt        int
	GetOperationCalledCnt          int
	deleteOperationCalledCnt       int
	dataPumpImportCalledCnt        int
	dataPumpExportCalledCnt        int
	SetParameterCalledCnt          int
	GetParameterTypeValueCalledCnt int
	RecoverConfigFileCalledCnt     int
	AsyncPhysicalBackup            bool
	AsyncPhysicalRestore           bool
	FetchServiceImageMetaDataCnt   int
	NextGetOperationStatus         FakeOperationStatus
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
	cli.CreateCDBCalledCnt++
	return nil, nil
}

// CreateUsers wrapper.
func (cli *FakeConfigAgentClient) CreateUsers(context.Context, *capb.CreateUsersRequest, ...grpc.CallOption) (*capb.CreateUsersResponse, error) {
	cli.CreateUsersCalledCnt++
	return nil, nil
}

// UsersChanged wrapper.
func (cli *FakeConfigAgentClient) UsersChanged(context.Context, *capb.UsersChangedRequest, ...grpc.CallOption) (*capb.UsersChangedResponse, error) {
	cli.UsersChangedCalledCnt++
	return nil, nil
}

// UpdateUsers wrapper.
func (cli *FakeConfigAgentClient) UpdateUsers(context.Context, *capb.UpdateUsersRequest, ...grpc.CallOption) (*capb.UpdateUsersResponse, error) {
	cli.UpdateUsersCalledCnt++
	return nil, nil
}

// PhysicalBackup wrapper.
func (cli *FakeConfigAgentClient) PhysicalBackup(context.Context, *capb.PhysicalBackupRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	cli.PhysicalBackupCalledCnt++
	return &longrunning.Operation{Done: !cli.AsyncPhysicalBackup}, nil
}

// PhysicalRestore wrapper.
func (cli *FakeConfigAgentClient) PhysicalRestore(context.Context, *capb.PhysicalRestoreRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	cli.PhysicalRestoreCalledCnt++
	return &longrunning.Operation{Done: !cli.AsyncPhysicalRestore}, nil
}

// CheckStatus wrapper.
func (cli *FakeConfigAgentClient) CheckStatus(context.Context, *capb.CheckStatusRequest, ...grpc.CallOption) (*capb.CheckStatusResponse, error) {
	cli.CheckStatusCalledCnt++
	return nil, nil
}

// CreateCDB wrapper.
func (cli *FakeConfigAgentClient) CreateCDB(context.Context, *capb.CreateCDBRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	cli.CreateCDBCalledCnt++
	return nil, nil
}

// CreateListener wrapper.
func (cli *FakeConfigAgentClient) CreateListener(context.Context, *capb.CreateListenerRequest, ...grpc.CallOption) (*capb.CreateListenerResponse, error) {
	cli.CreateListenerCalledCnt++
	return nil, nil
}

// ListOperations wrapper.
func (cli *FakeConfigAgentClient) ListOperations(context.Context, *longrunning.ListOperationsRequest, ...grpc.CallOption) (*longrunning.ListOperationsResponse, error) {
	cli.ListOperationsCalledCnt++
	return nil, nil
}

// GetOperation wrapper.
func (cli *FakeConfigAgentClient) GetOperation(context.Context, *longrunning.GetOperationRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	cli.GetOperationCalledCnt++

	switch cli.NextGetOperationStatus {
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
		panic(fmt.Sprintf("unknown status: %v", cli.NextGetOperationStatus))
	}
}

// DeleteOperation wrapper.
func (cli *FakeConfigAgentClient) DeleteOperation(context.Context, *longrunning.DeleteOperationRequest, ...grpc.CallOption) (*empty.Empty, error) {
	cli.deleteOperationCalledCnt++
	return nil, nil
}

// CreateCDBUser wrapper.
func (cli *FakeConfigAgentClient) CreateCDBUser(context.Context, *capb.CreateCDBUserRequest, ...grpc.CallOption) (*capb.CreateCDBUserResponse, error) {
	cli.CreateListenerCalledCnt++
	return nil, nil
}

// BootstrapDatabase wrapper.
func (cli *FakeConfigAgentClient) BootstrapDatabase(context.Context, *capb.BootstrapDatabaseRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	cli.BootstrapDatabaseCalledCnt++
	return nil, nil
}

// BootstrapStandby wrapper.
func (cli *FakeConfigAgentClient) BootstrapStandby(context.Context, *capb.BootstrapStandbyRequest, ...grpc.CallOption) (*capb.BootstrapStandbyResponse, error) {
	cli.BootstrapStandbyCalledCnt++
	return nil, nil
}

// DataPumpImport wrapper.
func (cli *FakeConfigAgentClient) DataPumpImport(context.Context, *capb.DataPumpImportRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	cli.dataPumpImportCalledCnt++
	return &longrunning.Operation{Done: false}, nil
}

// DataPumpExport wrapper.
func (cli *FakeConfigAgentClient) DataPumpExport(context.Context, *capb.DataPumpExportRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
	cli.dataPumpExportCalledCnt++
	return nil, nil
}

// BounceDatabase wrapper.
func (cli *FakeConfigAgentClient) BounceDatabase(context.Context, *capb.BounceDatabaseRequest, ...grpc.CallOption) (*capb.BounceDatabaseResponse, error) {
	cli.BounceDatabaseCalledCnt++
	return nil, nil
}

// DataPumpImportCalledCnt return call count.
func (cli *FakeConfigAgentClient) DataPumpImportCalledCnt() int {
	return cli.dataPumpImportCalledCnt
}

// DataPumpExportCalledCnt return call count.
func (cli *FakeConfigAgentClient) DataPumpExportCalledCnt() int {
	return cli.dataPumpExportCalledCnt
}

// DeleteOperationCalledCnt return call count.
func (cli *FakeConfigAgentClient) DeleteOperationCalledCnt() int {
	return cli.deleteOperationCalledCnt
}

// SetParameter wrapper.
func (cli *FakeConfigAgentClient) SetParameter(context.Context, *capb.SetParameterRequest, ...grpc.CallOption) (*capb.SetParameterResponse, error) {
	cli.SetParameterCalledCnt++
	return nil, nil
}

// GetParameterTypeValue wrapper.
func (cli *FakeConfigAgentClient) GetParameterTypeValue(context.Context, *capb.GetParameterTypeValueRequest, ...grpc.CallOption) (*capb.GetParameterTypeValueResponse, error) {
	cli.GetParameterTypeValueCalledCnt++
	return nil, nil
}

// RecoverConfigFile wrapper.
func (cli *FakeConfigAgentClient) RecoverConfigFile(ctx context.Context, in *capb.RecoverConfigFileRequest, opts ...grpc.CallOption) (*capb.RecoverConfigFileResponse, error) {
	cli.RecoverConfigFileCalledCnt++
	return nil, nil
}

func (cli *FakeConfigAgentClient) FetchServiceImageMetaData(ctx context.Context, in *capb.FetchServiceImageMetaDataRequest, opts ...grpc.CallOption) (*capb.FetchServiceImageMetaDataResponse, error) {
	cli.FetchServiceImageMetaDataCnt++
	return nil, nil
}
