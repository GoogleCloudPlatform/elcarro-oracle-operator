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

	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	lropb "google.golang.org/genproto/googleapis/longrunning"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FakeOperationStatus is an enum type for LRO statuses.
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

// FakeDatabaseClient mocks DatabaseDaemon
type FakeDatabaseClient struct {
	getOperationCalledCnt             int32
	listOperationsCalledCnt           int32
	fetchServiceImageMetaDataCnt      int32
	deleteOperationCalledCnt          int32
	bounceDatabaseCalledCnt           int32
	recoverConfigFileCalledCnt        int32
	checkDatabaseStateCalledCnt       int32
	runSQLPlusCalledCnt               int32
	runSQLPlusFormattedCalledCnt      int32
	bootstrapStandbyCalledCnt         int32
	createCDBAsyncCalledCnt           int32
	createDirsCalledCnt               int32
	bootstrapDatabaseCalledCnt        int32
	bootstrapDatabaseAsyncCalledCnt   int32
	fileExistsCalledCnt               int32
	createListenerCalledCnt           int32
	createFileCalledCnt               int32
	downloadDirectoryFromGCSCalledCnt int32
	runRMANCalledCnt                  int32
	runRMANAsyncCalledCnt             int32
	readDirCalledCnt                  int32
	physicalRestoreAsyncCalledCnt     int32
	asyncPhysicalBackup               bool
	asyncPhysicalRestore              bool
	deleteDirCalledCnt                int32
	dataPumpImportAsyncCalledCnt      int32
	dataPumpExportAsyncCalledCnt      int32
	runDataGuardCalledCnt             int32
	tnspingCalledCnt                  int32
	nidCalledCnt                      int32
	createPasswordFileCalledCnt       int32
	applyDataPatchAsyncCalledCnt      int32

	GotRMANAsyncRequest *dbdpb.RunRMANAsyncRequest

	lock                   sync.Mutex
	nextGetOperationStatus FakeOperationStatus

	asyncBootstrapDatabase bool

	methodToResp  map[string](interface{})
	methodToError map[string]error
}

func (cli *FakeDatabaseClient) SetDnfsState(ctx context.Context, in *dbdpb.SetDnfsStateRequest, opts ...grpc.CallOption) (*dbdpb.SetDnfsStateResponse, error) {
	panic("implement me")
}

// CreateDir RPC call to create a directory named path, along with any
// necessary parents.
func (cli *FakeDatabaseClient) CreateDirs(ctx context.Context, in *dbdpb.CreateDirsRequest, opts ...grpc.CallOption) (*dbdpb.CreateDirsResponse, error) {
	atomic.AddInt32(&cli.createDirsCalledCnt, 1)
	return nil, nil
}

// ReadDir RPC call to read the directory named by path and returns Fileinfos
// for the path and children.
func (cli *FakeDatabaseClient) ReadDir(ctx context.Context, in *dbdpb.ReadDirRequest, opts ...grpc.CallOption) (*dbdpb.ReadDirResponse, error) {
	atomic.AddInt32(&cli.readDirCalledCnt, 1)
	modTime := &timestamppb.Timestamp{
		Seconds: 10,
		Nanos:   10,
	}
	currPath := &dbdpb.ReadDirResponse_FileInfo{}
	subPath := &dbdpb.ReadDirResponse_FileInfo{ModTime: modTime, Name: "nnsnf"}
	subPath2 := &dbdpb.ReadDirResponse_FileInfo{ModTime: modTime, Name: "ncnnf"}

	return &dbdpb.ReadDirResponse{CurrPath: currPath, SubPaths: []*dbdpb.ReadDirResponse_FileInfo{subPath, subPath2}}, nil
}

// DeleteDir RPC to call remove path.
func (cli *FakeDatabaseClient) DeleteDir(ctx context.Context, in *dbdpb.DeleteDirRequest, opts ...grpc.CallOption) (*dbdpb.DeleteDirResponse, error) {
	atomic.AddInt32(&cli.deleteDirCalledCnt, 1)
	return nil, nil
}

// BounceDatabase RPC call to start/stop a database.
func (cli *FakeDatabaseClient) BounceDatabase(ctx context.Context, in *dbdpb.BounceDatabaseRequest, opts ...grpc.CallOption) (*dbdpb.BounceDatabaseResponse, error) {
	atomic.AddInt32(&cli.bounceDatabaseCalledCnt, 1)
	return nil, nil
}

// BounceListener RPC call to start/stop a listener.
func (cli *FakeDatabaseClient) BounceListener(ctx context.Context, in *dbdpb.BounceListenerRequest, opts ...grpc.CallOption) (*dbdpb.BounceListenerResponse, error) {
	panic("implement me")
}

// CheckDatabaseState RPC call verifies the database is running.
func (cli *FakeDatabaseClient) CheckDatabaseState(ctx context.Context, in *dbdpb.CheckDatabaseStateRequest, opts ...grpc.CallOption) (*dbdpb.CheckDatabaseStateResponse, error) {
	atomic.AddInt32(&cli.checkDatabaseStateCalledCnt, 1)
	return nil, nil
}

// RunSQLPlus RPC call executes Oracle's sqlplus utility.
func (cli *FakeDatabaseClient) RunSQLPlus(ctx context.Context, in *dbdpb.RunSQLPlusCMDRequest, opts ...grpc.CallOption) (*dbdpb.RunCMDResponse, error) {
	atomic.AddInt32(&cli.runSQLPlusCalledCnt, 1)
	return nil, nil
}

// RunSQLPlusFormatted RPC is similar to RunSQLPlus, but for queries.
func (cli *FakeDatabaseClient) RunSQLPlusFormatted(ctx context.Context, in *dbdpb.RunSQLPlusCMDRequest, opts ...grpc.CallOption) (*dbdpb.RunCMDResponse, error) {
	atomic.AddInt32(&cli.runSQLPlusFormattedCalledCnt, 1)
	method := "RunSQLPlusFormatted"
	resp, err := cli.getMethodRespErr(method)
	if resp != nil {
		return resp.(*dbdpb.RunCMDResponse), nil
	}
	return nil, err
}

// RunSQLPlusFormattedCalledCnt return call count.
func (cli *FakeDatabaseClient) RunSQLPlusFormattedCalledCnt() int {
	return int(atomic.LoadInt32(&cli.runSQLPlusFormattedCalledCnt))
}

// KnownPDBs RPC call returns a list of known PDBs.
func (cli *FakeDatabaseClient) KnownPDBs(ctx context.Context, in *dbdpb.KnownPDBsRequest, opts ...grpc.CallOption) (*dbdpb.KnownPDBsResponse, error) {
	panic("implement me")
}

// RunRMAN RPC call executes Oracle's rman utility.
func (cli *FakeDatabaseClient) RunRMAN(ctx context.Context, in *dbdpb.RunRMANRequest, opts ...grpc.CallOption) (*dbdpb.RunRMANResponse, error) {
	atomic.AddInt32(&cli.runRMANCalledCnt, 1)
	method := "RunRMAN"
	resp, _ := cli.getMethodRespErr(method)
	if resp != nil {
		return resp.(*dbdpb.RunRMANResponse), nil
	}
	return nil, nil
}

// RunRMANAsync RPC call executes Oracle's rman utility asynchronously.
func (cli *FakeDatabaseClient) RunRMANAsync(ctx context.Context, in *dbdpb.RunRMANAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error) {
	atomic.AddInt32(&cli.runRMANAsyncCalledCnt, 1)
	cli.GotRMANAsyncRequest = in
	_, err := cli.getMethodRespErr("RunRMANAsync")
	return &lropb.Operation{Done: !cli.asyncPhysicalBackup}, err
}

// NID changes a database id and/or database name.
func (cli *FakeDatabaseClient) NID(ctx context.Context, in *dbdpb.NIDRequest, opts ...grpc.CallOption) (*dbdpb.NIDResponse, error) {
	atomic.AddInt32(&cli.nidCalledCnt, 1)
	return &dbdpb.NIDResponse{}, nil
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
	atomic.AddInt32(&cli.createPasswordFileCalledCnt, 1)
	return &dbdpb.CreatePasswordFileResponse{}, nil
}

// SetListenerRegistration sets a static listener registration and restarts
// the listener.
func (cli *FakeDatabaseClient) SetListenerRegistration(ctx context.Context, in *dbdpb.SetListenerRegistrationRequest, opts ...grpc.CallOption) (*dbdpb.BounceListenerResponse, error) {
	panic("implement me")
}

// BootstrapStandby performs bootstrap tasks that have to be done by dbdaemon.
func (cli *FakeDatabaseClient) BootstrapStandby(ctx context.Context, in *dbdpb.BootstrapStandbyRequest, opts ...grpc.CallOption) (*dbdpb.BootstrapStandbyResponse, error) {
	atomic.AddInt32(&cli.bootstrapStandbyCalledCnt, 1)
	return nil, nil
}

// CreateCDBAsync creates a database instance asynchronously.
func (cli *FakeDatabaseClient) CreateCDBAsync(ctx context.Context, in *dbdpb.CreateCDBAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error) {
	atomic.AddInt32(&cli.createCDBAsyncCalledCnt, 1)
	return nil, nil
}

// BootstrapDatabaseAsync bootstraps seeded database asynchronously.
func (cli *FakeDatabaseClient) BootstrapDatabaseAsync(ctx context.Context, in *dbdpb.BootstrapDatabaseAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error) {
	atomic.AddInt32(&cli.bootstrapDatabaseAsyncCalledCnt, 1)
	return &lropb.Operation{Done: !cli.asyncBootstrapDatabase}, nil
}

// CreateListener creates a database listener.
func (cli *FakeDatabaseClient) CreateListener(ctx context.Context, in *dbdpb.CreateListenerRequest, opts ...grpc.CallOption) (*dbdpb.CreateListenerResponse, error) {
	atomic.AddInt32(&cli.createListenerCalledCnt, 1)
	return &dbdpb.CreateListenerResponse{}, nil
}

// FileExists runs a simple check to confirm whether a requested file
// exists in a database container or not.
// An example of where FileExists is used is a check on
// the provisioning_successful file, but any file (nor a dir) can be
// checked via this RPC call.
func (cli *FakeDatabaseClient) FileExists(ctx context.Context, in *dbdpb.FileExistsRequest, opts ...grpc.CallOption) (*dbdpb.FileExistsResponse, error) {
	atomic.AddInt32(&cli.fileExistsCalledCnt, 1)
	method := "FileExists"
	resp, err := cli.getMethodRespErr(method)
	if resp != nil {
		return resp.(*dbdpb.FileExistsResponse), err
	} else {
		return &dbdpb.FileExistsResponse{Exists: false}, err
	}
}

// PhysicalRestoreAsync runs RMAN and SQL queries in sequence to restore
// a database from an RMAN backup.
func (cli *FakeDatabaseClient) PhysicalRestoreAsync(ctx context.Context, in *dbdpb.PhysicalRestoreAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error) {
	atomic.AddInt32(&cli.physicalRestoreAsyncCalledCnt, 1)
	_, err := cli.getMethodRespErr("PhysicalRestoreAsync")
	return &lropb.Operation{Done: !cli.asyncPhysicalRestore}, err
}

// DataPumpImportAsync imports data from a .dmp file to an existing PDB.
func (cli *FakeDatabaseClient) DataPumpImportAsync(ctx context.Context, in *dbdpb.DataPumpImportAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error) {
	atomic.AddInt32(&cli.dataPumpImportAsyncCalledCnt, 1)
	return &lropb.Operation{Done: false}, nil
}

// DataPumpExportAsync exports data to a .dmp file using expdp
func (cli *FakeDatabaseClient) DataPumpExportAsync(ctx context.Context, in *dbdpb.DataPumpExportAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error) {
	atomic.AddInt32(&cli.dataPumpExportAsyncCalledCnt, 1)
	return &lropb.Operation{Done: false}, nil
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
	atomic.AddInt32(&cli.deleteOperationCalledCnt, 1)
	return nil, nil
}

// RecoverConfigFile creates a binary pfile from the backed up spfile
func (cli *FakeDatabaseClient) RecoverConfigFile(ctx context.Context, in *dbdpb.RecoverConfigFileRequest, opts ...grpc.CallOption) (*dbdpb.RecoverConfigFileResponse, error) {
	atomic.AddInt32(&cli.recoverConfigFileCalledCnt, 1)
	return nil, nil
}

// DownloadDirectoryFromGCS downloads a directory from GCS bucket to local
// path.
func (cli *FakeDatabaseClient) DownloadDirectoryFromGCS(ctx context.Context, in *dbdpb.DownloadDirectoryFromGCSRequest, opts ...grpc.CallOption) (*dbdpb.DownloadDirectoryFromGCSResponse, error) {
	atomic.AddInt32(&cli.downloadDirectoryFromGCSCalledCnt, 1)
	method := "DownloadDirectoryFromGCS"
	resp, err := cli.getMethodRespErr(method)
	if resp != nil {
		return resp.(*dbdpb.DownloadDirectoryFromGCSResponse), err
	} else {
		return nil, err
	}
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

func (cli *FakeDatabaseClient) RunDataGuard(ctx context.Context, req *dbdpb.RunDataGuardRequest, opts ...grpc.CallOption) (*dbdpb.RunDataGuardResponse, error) {
	atomic.AddInt32(&cli.runDataGuardCalledCnt, 1)
	return &dbdpb.RunDataGuardResponse{}, nil
}

func (cli *FakeDatabaseClient) TNSPing(ctx context.Context, req *dbdpb.TNSPingRequest, opts ...grpc.CallOption) (*dbdpb.TNSPingResponse, error) {
	atomic.AddInt32(&cli.tnspingCalledCnt, 1)
	return &dbdpb.TNSPingResponse{}, nil
}

// CreateFile creates file based on file path and content.
func (cli *FakeDatabaseClient) CreateFile(ctx context.Context, in *dbdpb.CreateFileRequest, opts ...grpc.CallOption) (*dbdpb.CreateFileResponse, error) {
	atomic.AddInt32(&cli.createFileCalledCnt, 1)
	return &dbdpb.CreateFileResponse{}, nil
}

// BootstrapDatabase bootstraps seeded database by executing init_oracle
func (cli *FakeDatabaseClient) BootstrapDatabase(ctx context.Context, in *dbdpb.BootstrapDatabaseRequest, opts ...grpc.CallOption) (*dbdpb.BootstrapDatabaseResponse, error) {
	atomic.AddInt32(&cli.bootstrapDatabaseCalledCnt, 1)
	return &dbdpb.BootstrapDatabaseResponse{}, nil
}

var (
	emptyConnCloseFunc = func() {}
)

// FakeDatabaseClientFactory is a simple factory to create our FakeDatabaseClient.
type FakeDatabaseClientFactory struct {
	lock     sync.Mutex
	Dbclient *FakeDatabaseClient
}

// New returns a new fake DatabaseClient.
func (g *FakeDatabaseClientFactory) New(context.Context, client.Reader, string, string) (dbdpb.DatabaseDaemonClient, func() error, error) {
	g.lock.Lock()
	defer g.lock.Unlock()
	if g.Dbclient == nil {
		g.Dbclient = &FakeDatabaseClient{}
	}
	return g.Dbclient, func() error { return nil }, nil
}

func (g *FakeDatabaseClientFactory) Reset() {
	g.lock.Lock()
	defer g.lock.Unlock()
	g.Dbclient = &FakeDatabaseClient{}
}

// Reset reset's the database client's counters.
func (cli *FakeDatabaseClient) Reset() {
	*cli = FakeDatabaseClient{}
}

// GetOperation gets the latest state of a long-running operation. Clients can
// use this method to poll the operation result.
func (cli *FakeDatabaseClient) GetOperation(context.Context, *lropb.GetOperationRequest, ...grpc.CallOption) (*lropb.Operation, error) {
	atomic.AddInt32(&cli.getOperationCalledCnt, 1)

	switch cli.NextGetOperationStatus() {
	case StatusDone:
		return &lropb.Operation{Done: true}, nil

	case StatusDoneWithError:
		return &lropb.Operation{
			Done: true,
			Result: &lropb.Operation_Error{
				Error: &status.Status{Code: int32(codes.Unknown), Message: "Test Error"},
			},
		}, nil

	case StatusRunning:
		return &lropb.Operation{}, nil

	case StatusNotFound:
		return nil, grpcstatus.Errorf(codes.NotFound, "")

	case StatusUndefined:
		panic("Misconfigured test, set up expected operation status")

	default:
		panic(fmt.Sprintf("unknown status: %v", cli.NextGetOperationStatus()))
	}
}

// BootstrapDatabaseAsyncCalledCnt returns call count.
func (cli *FakeDatabaseClient) BootstrapDatabaseAsyncCalledCnt() int {
	return int(atomic.LoadInt32(&cli.bootstrapDatabaseAsyncCalledCnt))
}

// RMANAsyncCalledCnt returns call count.
func (cli *FakeDatabaseClient) RunRMANCalledCnt() int {
	return int(atomic.LoadInt32(&cli.runRMANCalledCnt))
}

// RMANAsyncCalledCnt returns call count.
func (cli *FakeDatabaseClient) RunRMANAsyncCalledCnt() int {
	return int(atomic.LoadInt32(&cli.runRMANAsyncCalledCnt))
}

// RMANAsyncCalledCnt returns call count.
func (cli *FakeDatabaseClient) PhysicalRestoreAsyncCalledCnt() int {
	return int(atomic.LoadInt32(&cli.physicalRestoreAsyncCalledCnt))
}

// DataPumpImportAsyncCalledCnt returns call count for DataPumpImportAsync.
func (cli *FakeDatabaseClient) DataPumpImportAsyncCalledCnt() int {
	return int(atomic.LoadInt32(&cli.dataPumpImportAsyncCalledCnt))
}

// DataPumpExportAsyncCalledCnt returns call count for DataPumpExportAsync.
func (cli *FakeDatabaseClient) DataPumpExportAsyncCalledCnt() int {
	return int(atomic.LoadInt32(&cli.dataPumpExportAsyncCalledCnt))
}

// DeleteOperationCalledCnt returns call count.
func (cli *FakeDatabaseClient) DeleteOperationCalledCnt() int {
	return int(atomic.LoadInt32(&cli.deleteOperationCalledCnt))
}

// GetOperationCalledCnt returns call count.
func (cli *FakeDatabaseClient) GetOperationCalledCnt() int {
	return int(atomic.LoadInt32(&cli.getOperationCalledCnt))
}

// GetDownloadDirectoryFromGCSCnt returns call count.
func (cli *FakeDatabaseClient) GetDownloadDirectoryFromGCSCnt() int {
	return int(atomic.LoadInt32(&cli.downloadDirectoryFromGCSCalledCnt))
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

func (cli *FakeDatabaseClient) SetAsyncPhysicalBackup(async bool) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	cli.asyncPhysicalBackup = async
}

func (cli *FakeDatabaseClient) SetAsyncPhysicalRestore(async bool) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	cli.asyncPhysicalRestore = async
}

func (cli *FakeDatabaseClient) SetAsyncBootstrapDatabase(async bool) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	cli.asyncBootstrapDatabase = async
}

func (cli *FakeDatabaseClient) SetMethodToResp(method string, resp interface{}) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	if cli.methodToResp == nil {
		cli.methodToResp = make(map[string]interface{})
	}
	cli.methodToResp[method] = resp
}

func (cli *FakeDatabaseClient) SetMethodToError(method string, err error) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	if cli.methodToError == nil {
		cli.methodToError = make(map[string]error)
	}
	cli.methodToError[method] = err
}

func (cli *FakeDatabaseClient) RemoveMethodToError(method string) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
	if cli.methodToError == nil {
		return
	}
	_, ok := cli.methodToError[method]
	if ok {
		delete(cli.methodToError, method)
	}
}

func (cli *FakeDatabaseClient) getMethodRespErr(method string) (interface{}, error) {
	cli.lock.Lock()
	defer cli.lock.Unlock()
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
