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
	"google.golang.org/protobuf/types/known/emptypb"

	lropb "google.golang.org/genproto/googleapis/longrunning"
	grpcstatus "google.golang.org/grpc/status"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	capb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"

	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
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
	bootstrapDatabaseCalledCnt     int32
	bootstrapStandbyCalledCnt      int32
	bounceDatabaseCalledCnt        int32
	createListenerCalledCnt        int32
	getOperationCalledCnt          int32
	deleteOperationCalledCnt       int32
	dataPumpImportCalledCnt        int32
	dataPumpExportCalledCnt        int32
	setParameterCalledCnt          int32
	getParameterTypeValueCalledCnt int32
	recoverConfigFileCalledCnt     int32

	lock                         sync.Mutex
	fetchServiceImageMetaDataCnt int32
	asyncBootstrapDatabase       bool
	asyncPhysicalBackup          bool
	asyncPhysicalRestore         bool
	methodToResp                 map[string](interface{})
	methodToError                map[string]error
	nextGetOperationStatus       FakeOperationStatus

	GotPhysicalBackupReq *capb.PhysicalBackupRequest
}

// FakeDatabaseClient mocks DatabaseDaemon
type FakeDatabaseClient struct {
	getOperationCalledCnt        int32
	listOperationsCalledCnt      int32
	fetchServiceImageMetaDataCnt int32

	lock                   sync.Mutex
	nextGetOperationStatus FakeOperationStatus

	methodToResp map[string](interface{})
}

// CreateDir RPC call to create a directory named path, along with any
// necessary parents.
func (cli *FakeDatabaseClient) CreateDirs(ctx context.Context, in *dbdpb.CreateDirsRequest, opts ...grpc.CallOption) (*dbdpb.CreateDirsResponse, error) {
	panic("implement me")
}

// ReadDir RPC call to read the directory named by path and returns Fileinfos
// for the path and children.
func (cli *FakeDatabaseClient) ReadDir(ctx context.Context, in *dbdpb.ReadDirRequest, opts ...grpc.CallOption) (*dbdpb.ReadDirResponse, error) {
	panic("implement me")
}

// DeleteDir RPC to call remove path.
func (cli *FakeDatabaseClient) DeleteDir(ctx context.Context, in *dbdpb.DeleteDirRequest, opts ...grpc.CallOption) (*dbdpb.DeleteDirResponse, error) {
	panic("implement me")
}

// BounceDatabase RPC call to start/stop a database.
func (cli *FakeDatabaseClient) BounceDatabase(ctx context.Context, in *dbdpb.BounceDatabaseRequest, opts ...grpc.CallOption) (*dbdpb.BounceDatabaseResponse, error) {
	panic("implement me")
}

// BounceListener RPC call to start/stop a listener.
func (cli *FakeDatabaseClient) BounceListener(ctx context.Context, in *dbdpb.BounceListenerRequest, opts ...grpc.CallOption) (*dbdpb.BounceListenerResponse, error) {
	panic("implement me")
}

// CheckDatabaseState RPC call verifies the database is running.
func (cli *FakeDatabaseClient) CheckDatabaseState(ctx context.Context, in *dbdpb.CheckDatabaseStateRequest, opts ...grpc.CallOption) (*dbdpb.CheckDatabaseStateResponse, error) {
	panic("implement me")
}

// RunSQLPlus RPC call executes Oracle's sqlplus utility.
func (cli *FakeDatabaseClient) RunSQLPlus(ctx context.Context, in *dbdpb.RunSQLPlusCMDRequest, opts ...grpc.CallOption) (*dbdpb.RunCMDResponse, error) {
	panic("implement me")
}

// RunSQLPlusFormatted RPC is similar to RunSQLPlus, but for queries.
func (cli *FakeDatabaseClient) RunSQLPlusFormatted(ctx context.Context, in *dbdpb.RunSQLPlusCMDRequest, opts ...grpc.CallOption) (*dbdpb.RunCMDResponse, error) {
	panic("implement me")
}

// KnownPDBs RPC call returns a list of known PDBs.
func (cli *FakeDatabaseClient) KnownPDBs(ctx context.Context, in *dbdpb.KnownPDBsRequest, opts ...grpc.CallOption) (*dbdpb.KnownPDBsResponse, error) {
	panic("implement me")
}

// RunRMAN RPC call executes Oracle's rman utility.
func (cli *FakeDatabaseClient) RunRMAN(ctx context.Context, in *dbdpb.RunRMANRequest, opts ...grpc.CallOption) (*dbdpb.RunRMANResponse, error) {
	panic("implement me")
}

// RunRMANAsync RPC call executes Oracle's rman utility asynchronously.
func (cli *FakeDatabaseClient) RunRMANAsync(ctx context.Context, in *dbdpb.RunRMANAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error) {
	panic("implement me")
}

// NID changes a database id and/or database name.
func (cli *FakeDatabaseClient) NID(ctx context.Context, in *dbdpb.NIDRequest, opts ...grpc.CallOption) (*dbdpb.NIDResponse, error) {
	panic("implement me")
}

// GetDatabaseType returns database type(eg. ORACLE_12_2_ENTERPRISE_NONCDB)
func (cli *FakeDatabaseClient) GetDatabaseType(ctx context.Context, in *dbdpb.GetDatabaseTypeRequest, opts ...grpc.CallOption) (*dbdpb.GetDatabaseTypeResponse, error) {
	panic("implement me")
}

// GetDatabaseName returns database name.
func (cli *FakeDatabaseClient) GetDatabaseName(ctx context.Context, in *dbdpb.GetDatabaseNameRequest, opts ...grpc.CallOption) (*dbdpb.GetDatabaseNameResponse, error) {
	panic("implement me")
}

// CreatePasswordFile creates a password file for the database.
func (cli *FakeDatabaseClient) CreatePasswordFile(ctx context.Context, in *dbdpb.CreatePasswordFileRequest, opts ...grpc.CallOption) (*dbdpb.CreatePasswordFileResponse, error) {
	panic("implement me")
}

// SetListenerRegistration sets a static listener registration and restarts
// the listener.
func (cli *FakeDatabaseClient) SetListenerRegistration(ctx context.Context, in *dbdpb.SetListenerRegistrationRequest, opts ...grpc.CallOption) (*dbdpb.BounceListenerResponse, error) {
	panic("implement me")
}

// BootstrapStandby performs bootstrap tasks that have to be done by dbdaemon.
func (cli *FakeDatabaseClient) BootstrapStandby(ctx context.Context, in *dbdpb.BootstrapStandbyRequest, opts ...grpc.CallOption) (*dbdpb.BootstrapStandbyResponse, error) {
	panic("implement me")
}

// CreateCDBAsync creates a database instance asynchronously.
func (cli *FakeDatabaseClient) CreateCDBAsync(ctx context.Context, in *dbdpb.CreateCDBAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error) {
	panic("implement me")
}

// BootstrapDatabaseAsync bootstraps seeded database asynchronously.
func (cli *FakeDatabaseClient) BootstrapDatabaseAsync(ctx context.Context, in *dbdpb.BootstrapDatabaseAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error) {
	panic("implement me")
}

// CreateListener creates a database listener.
func (cli *FakeDatabaseClient) CreateListener(ctx context.Context, in *dbdpb.CreateListenerRequest, opts ...grpc.CallOption) (*dbdpb.CreateListenerResponse, error) {
	panic("implement me")
}

// FileExists runs a simple check to confirm whether a requested file
// exists in a database container or not.
// An example of where FileExists is used is a check on
// the provisioning_successful file, but any file (nor a dir) can be
// checked via this RPC call.
func (cli *FakeDatabaseClient) FileExists(ctx context.Context, in *dbdpb.FileExistsRequest, opts ...grpc.CallOption) (*dbdpb.FileExistsResponse, error) {
	panic("implement me")
}

// PhysicalRestoreAsync runs RMAN and SQL queries in sequence to restore
// a database from an RMAN backup.
func (cli *FakeDatabaseClient) PhysicalRestoreAsync(ctx context.Context, in *dbdpb.PhysicalRestoreAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error) {
	panic("implement me")
}

// DataPumpImportAsync imports data from a .dmp file to an existing PDB.
func (cli *FakeDatabaseClient) DataPumpImportAsync(ctx context.Context, in *dbdpb.DataPumpImportAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error) {
	panic("implement me")
}

// DataPumpExportAsync exports data to a .dmp file using expdp
func (cli *FakeDatabaseClient) DataPumpExportAsync(ctx context.Context, in *dbdpb.DataPumpExportAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error) {
	panic("implement me")
}

// ListOperations lists operations that match the specified filter in the
// request.
func (cli *FakeDatabaseClient) ListOperations(ctx context.Context, in *lropb.ListOperationsRequest, opts ...grpc.CallOption) (*lropb.ListOperationsResponse, error) {
	atomic.AddInt32(&cli.listOperationsCalledCnt, 1)
	return nil, nil
}

// DeleteOperation deletes a long-running operation. This method indicates
// that the client is no longer interested in the operation result. It does
// not cancel the operation.
func (cli *FakeDatabaseClient) DeleteOperation(ctx context.Context, in *lropb.DeleteOperationRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	panic("implement me")
}

// RecoverConfigFile creates a binary pfile from the backed up spfile
func (cli *FakeDatabaseClient) RecoverConfigFile(ctx context.Context, in *dbdpb.RecoverConfigFileRequest, opts ...grpc.CallOption) (*dbdpb.RecoverConfigFileResponse, error) {
	panic("implement me")
}

// DownloadDirectoryFromGCS downloads a directory from GCS bucket to local
// path.
func (cli *FakeDatabaseClient) DownloadDirectoryFromGCS(ctx context.Context, in *dbdpb.DownloadDirectoryFromGCSRequest, opts ...grpc.CallOption) (*dbdpb.DownloadDirectoryFromGCSResponse, error) {
	panic("implement me")
}

// FetchServiceImageMetaData returns the service image metadata.
func (cli *FakeDatabaseClient) FetchServiceImageMetaData(ctx context.Context, in *dbdpb.FetchServiceImageMetaDataRequest, opts ...grpc.CallOption) (*dbdpb.FetchServiceImageMetaDataResponse, error) {
	atomic.AddInt32(&cli.fetchServiceImageMetaDataCnt, 1)
	if cli.methodToResp == nil {
		return nil, nil
	}
	method := "FetchServiceImageMetaData"
	if resp, ok := cli.methodToResp[method]; ok {
		return resp.(*dbdpb.FetchServiceImageMetaDataResponse), nil
	}
	return nil, nil

}

// CreateFile creates file based on file path and content.
func (cli *FakeDatabaseClient) CreateFile(ctx context.Context, in *dbdpb.CreateFileRequest, opts ...grpc.CallOption) (*dbdpb.CreateFileResponse, error) {
	panic("implement me")
}

// BootstrapDatabase bootstraps seeded database by executing init_oracle
func (cli *FakeDatabaseClient) BootstrapDatabase(ctx context.Context, in *dbdpb.BootstrapDatabaseRequest, opts ...grpc.CallOption) (*dbdpb.BootstrapDatabaseResponse, error) {
	panic("implement me")
}

var (
	emptyConnCloseFunc = func() {}
)

// FakeClientFactory is a simple factory to create our FakeConfigAgentClient.
type FakeClientFactory struct {
	Caclient *FakeConfigAgentClient
}

// FakeClientFactory is a simple factory to create our FakeConfigAgentClient.
type FakeDatabaseClientFactory struct {
	Dbclient *FakeDatabaseClient
}

// New returns a new fake ConfigAgent.
func (g *FakeClientFactory) New(context.Context, client.Reader, string, string) (capb.ConfigAgentClient, controllers.ConnCloseFunc, error) {
	if g.Caclient == nil {
		g.Reset()
	}
	return g.Caclient, emptyConnCloseFunc, nil
}

// New returns a new fake ConfigAgent.
func (g *FakeDatabaseClientFactory) New(context.Context, string) (dbdpb.DatabaseDaemonClient, func() error, error) {
	if g.Dbclient == nil {
		g.Reset()
	}
	return g.Dbclient, func() error { return nil }, nil
}

// Reset clears the inner ConfigAgent.
func (g *FakeClientFactory) Reset() {
	g.Caclient = &FakeConfigAgentClient{}
}

// Reset clears the inner ConfigAgent.
func (g *FakeDatabaseClientFactory) Reset() {
	g.Dbclient = &FakeDatabaseClient{}
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
	resp, err := cli.getMethodRespErr("VerifyPhysicalBackup")
	if resp == nil {
		return &capb.VerifyPhysicalBackupResponse{}, err
	}
	return resp.(*capb.VerifyPhysicalBackupResponse), err
}

// PhysicalBackup wrapper.
func (cli *FakeConfigAgentClient) PhysicalBackup(ctx context.Context, req *capb.PhysicalBackupRequest, opts ...grpc.CallOption) (*longrunning.Operation, error) {
	atomic.AddInt32(&cli.physicalBackupCalledCnt, 1)
	cli.GotPhysicalBackupReq = req
	_, err := cli.getMethodRespErr("PhysicalBackup")
	return &longrunning.Operation{Done: !cli.asyncPhysicalBackup}, err
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

// GetOperation gets the latest state of a long-running operation. Clients can
// use this method to poll the operation result.
func (cli *FakeDatabaseClient) GetOperation(context.Context, *longrunning.GetOperationRequest, ...grpc.CallOption) (*longrunning.Operation, error) {
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
	return &lropb.Operation{Done: !cli.asyncBootstrapDatabase}, nil
}

// BootstrapDatabaseCalledCnt returns call count.
func (cli *FakeConfigAgentClient) BootstrapDatabaseCalledCnt() int {
	return int(atomic.LoadInt32(&cli.bootstrapDatabaseCalledCnt))
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
func (cli *FakeDatabaseClient) GetOperationCalledCnt() int {
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

// Set the next operation's status
func (cli *FakeDatabaseClient) SetNextGetOperationStatus(status FakeOperationStatus) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	cli.nextGetOperationStatus = status
}

// Return the next operation's status
func (cli *FakeDatabaseClient) NextGetOperationStatus() FakeOperationStatus {
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

func (cli *FakeConfigAgentClient) SetAsyncBootstrapDatabase(async bool) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	cli.asyncBootstrapDatabase = async
}

func (cli *FakeConfigAgentClient) SetMethodToResp(method string, resp interface{}) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	if cli.methodToResp == nil {
		cli.methodToResp = make(map[string]interface{})
	}
	cli.methodToResp[method] = resp
}

func (cli *FakeDatabaseClient) SetMethodToResp(method string, resp interface{}) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	if cli.methodToResp == nil {
		cli.methodToResp = make(map[string]interface{})
	}
	cli.methodToResp[method] = resp
}

func (cli *FakeConfigAgentClient) SetMethodToError(method string, err error) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	if cli.methodToError == nil {
		cli.methodToError = make(map[string]error)
	}
	cli.methodToError[method] = err
}

func (cli *FakeConfigAgentClient) getMethodRespErr(method string) (interface{}, error) {
	var err error
	var resp interface{}
	if cli.methodToResp != nil {
		if _, ok := cli.methodToResp[method]; ok {
			resp = cli.methodToResp[method]
		}
	}
	if cli.methodToError != nil {
		if _, ok := cli.methodToError[method]; ok {
			err = cli.methodToError[method]
		}
	}
	return resp, err
}
