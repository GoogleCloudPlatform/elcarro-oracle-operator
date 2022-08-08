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

package standby

import (
	"context"
	"errors"
	"net"
	"testing"

	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

type fakeServer struct {
	*dbdpb.UnimplementedDatabaseDaemonServer
	fakeRunDataGuard              func(context.Context, *dbdpb.RunDataGuardRequest) (*dbdpb.RunDataGuardResponse, error)
	fakeFetchServiceImageMetaData func(ctx context.Context, req *dbdpb.FetchServiceImageMetaDataRequest) (*dbdpb.FetchServiceImageMetaDataResponse, error)
	fakeDownloadDirectoryFromGCS  func(ctx context.Context, req *dbdpb.DownloadDirectoryFromGCSRequest) (*dbdpb.DownloadDirectoryFromGCSResponse, error)
	fakeRunSQLPlusFormatted       func(ctx context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error)
	fakeRunSQLPlus                func(ctx context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error)
	fakeCreateListener            func(ctx context.Context, req *dbdpb.CreateListenerRequest) (*dbdpb.CreateListenerResponse, error)
	fakeFileExists                func(ctx context.Context, req *dbdpb.FileExistsRequest) (*dbdpb.FileExistsResponse, error)
	fakeBounceDatabase            func(ctx context.Context, req *dbdpb.BounceDatabaseRequest) (*dbdpb.BounceDatabaseResponse, error)
	fakeCreateFile                func(ctx context.Context, req *dbdpb.CreateFileRequest) (*dbdpb.CreateFileResponse, error)
}

func (f *fakeServer) RunDataGuard(ctx context.Context, req *dbdpb.RunDataGuardRequest) (*dbdpb.RunDataGuardResponse, error) {
	if f.fakeRunDataGuard == nil {
		return nil, errors.New("RunDataGuard fake not found")
	}
	return f.fakeRunDataGuard(ctx, req)
}

func (f *fakeServer) FetchServiceImageMetaData(ctx context.Context, req *dbdpb.FetchServiceImageMetaDataRequest) (*dbdpb.FetchServiceImageMetaDataResponse, error) {
	if f.fakeFetchServiceImageMetaData == nil {
		return nil, errors.New("FetchServiceImageMetaData fake not found")
	}
	return f.fakeFetchServiceImageMetaData(ctx, req)
}

func (f *fakeServer) DownloadDirectoryFromGCS(ctx context.Context, req *dbdpb.DownloadDirectoryFromGCSRequest) (*dbdpb.DownloadDirectoryFromGCSResponse, error) {
	if f.fakeDownloadDirectoryFromGCS == nil {
		return nil, errors.New("DownloadDirectoryFromGCS fake not found")
	}
	return f.fakeDownloadDirectoryFromGCS(ctx, req)
}

func (f *fakeServer) RunSQLPlusFormatted(ctx context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
	if f.fakeRunSQLPlusFormatted == nil {
		return nil, errors.New("RunSQLPlusFormatted fake not found")
	}
	return f.fakeRunSQLPlusFormatted(ctx, req)
}

func (f *fakeServer) RunSQLPlus(ctx context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
	if f.fakeRunSQLPlus == nil {
		return nil, errors.New("RunSQLPlus fake not found")
	}
	return f.fakeRunSQLPlus(ctx, req)
}

func (f *fakeServer) CreateListener(ctx context.Context, req *dbdpb.CreateListenerRequest) (*dbdpb.CreateListenerResponse, error) {
	if f.fakeCreateListener == nil {
		return nil, errors.New("CreateListener fake not found")
	}
	return f.fakeCreateListener(ctx, req)
}

func (f *fakeServer) FileExists(ctx context.Context, req *dbdpb.FileExistsRequest) (*dbdpb.FileExistsResponse, error) {
	if f.fakeFileExists == nil {
		return nil, errors.New("FileExists fake not found")
	}
	return f.fakeFileExists(ctx, req)
}

func (f *fakeServer) BounceDatabase(ctx context.Context, req *dbdpb.BounceDatabaseRequest) (*dbdpb.BounceDatabaseResponse, error) {
	if f.fakeBounceDatabase == nil {
		return nil, errors.New("BounceDatabase fake not found")
	}
	return f.fakeBounceDatabase(ctx, req)
}

func (f *fakeServer) CreateFile(ctx context.Context, req *dbdpb.CreateFileRequest) (*dbdpb.CreateFileResponse, error) {
	if f.fakeCreateFile == nil {
		return nil, errors.New("CreateFile fake not found")
	}
	return f.fakeCreateFile(ctx, req)
}

func newFakeDatabaseDaemonClient(t *testing.T, server *fakeServer) (dbdpb.DatabaseDaemonClient, func()) {
	t.Helper()
	grpcSvr := grpc.NewServer()

	dbdpb.RegisterDatabaseDaemonServer(grpcSvr, server)
	lis := bufconn.Listen(2 * 1024 * 1024)
	go grpcSvr.Serve(lis)

	dbdConn, err := grpc.Dial("test",
		grpc.WithInsecure(),
		grpc.WithContextDialer(
			func(ctx context.Context, s string) (conn net.Conn, err error) {
				return lis.Dial()
			}),
	)
	if err != nil {
		t.Fatalf("failed to dial to dbDaemon: %v", err)
	}
	return dbdpb.NewDatabaseDaemonClient(dbdConn), func() {
		dbdConn.Close()
		grpcSvr.GracefulStop()
	}
}

type fakeSecretAccessor struct {
	fakeGet func(context.Context) (string, error)
}

func (f *fakeSecretAccessor) Get(ctx context.Context) (string, error) {
	if f.fakeGet == nil {
		return "", errors.New("Get fake not found")
	}
	return f.fakeGet(ctx)
}
