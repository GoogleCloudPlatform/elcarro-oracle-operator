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

package configagent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

var (
	gsmRefNoChange = &pb.GsmSecretReference{
		ProjectId:   "test-project",
		SecretId:    "test-secret",
		Version:     "1",
		LastVersion: fmt.Sprintf(gsmSecretStr, "test-project", "test-secret", "1"),
	}

	gsmRefWithChange = &pb.GsmSecretReference{
		ProjectId:   "test-project",
		SecretId:    "test-secret",
		Version:     "2",
		LastVersion: fmt.Sprintf(gsmSecretStr, "test-project", "test-secret", "1"),
	}

	sampleSqlToResp = map[string][]string{
		`alter session set container="MYDB";select username from dba_users where ORACLE_MAINTAINED='N' and INHERITED='NO'`: {
			`{"USERNAME": "GPDB_ADMIN"}`,
			`{"USERNAME": "SUPERUSER"}`,
			`{"USERNAME": "SCOTT"}`,
			`{"USERNAME": "PROBERUSER"}`,
		},
		`alter session set container="MYDB";select privilege from dba_sys_privs where grantee='SUPERUSER'`: {},
		`alter session set container="MYDB";select privilege from dba_sys_privs where grantee='SCOTT'`: {
			`{"PRIVILEGE": "UNLIMITED TABLESPACE"}`,
		},
		`alter session set container="MYDB";select privilege from dba_sys_privs where grantee='PROBERUSER'`: {
			`{"PRIVILEGE": "CREATE SESSION"}`,
		},
		`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='SUPERUSER'`: {
			`{"GRANTED_ROLE": "DBA"}`,
		},
		`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='SCOTT'`: {
			`{"GRANTED_ROLE": "RESOURCE"}`,
			`{"GRANTED_ROLE": "CONNECT"}`,
		},
		`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='PROBERUSER'`: {},
	}
)

type fakeServer struct {
	*dbdpb.UnimplementedDatabaseDaemonServer
	fakeRunSQLPlus          func(context.Context, *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error)
	fakeRunSQLPlusFormatted func(context.Context, *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error)
}

func (f *fakeServer) RunSQLPlus(ctx context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
	if f.fakeRunSQLPlus == nil {
		return nil, errors.New("RunSQLPlus fake not found")
	}
	return f.fakeRunSQLPlus(ctx, req)
}

func (f *fakeServer) RunSQLPlusFormatted(ctx context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
	if f.fakeRunSQLPlusFormatted == nil {
		return nil, errors.New("RunSQLPlusFormatted fake not found")
	}
	return f.fakeRunSQLPlusFormatted(ctx, req)
}
func (f *fakeServer) CheckDatabaseState(context.Context, *dbdpb.CheckDatabaseStateRequest) (*dbdpb.CheckDatabaseStateResponse, error) {
	return &dbdpb.CheckDatabaseStateResponse{}, nil
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
