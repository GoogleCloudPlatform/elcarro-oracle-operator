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

package provision

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

func TestSetParametersHelper(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	ctx := context.Background()
	defer cleanup()
	testCDB := &oracleCDB{
		sourceCDBName:            "GCLOUD",
		cdbName:                  "TEST",
		version:                  "12.2",
		host:                     "testhost",
		uniqueName:               "TEST_generic",
		DBDomain:                 "gke",
		databaseParamSGATargetMB: 10000,
		databaseParamPGATargetMB: 2500,
	}
	bootstrapTask := &BootstrapTask{
		dbdClient: client,
		db:        testCDB,
	}
	wantSQLsCommon := []string{
		"alter system set audit_file_dest='/u02/app/oracle/admin/TEST/adump' scope=spfile",
		"alter system set audit_trail='db' scope=spfile",
		"alter system set control_files='/u02/app/oracle/oradata/TEST/control01.ctl' scope=spfile",
		"alter system set db_block_size=8192 scope=spfile",
		"alter system set db_domain='gke' scope=spfile",
		"alter system set db_name='TEST' scope=spfile",
		"alter system set db_unique_name='TEST_generic' scope=spfile",
		"alter system set db_recovery_file_dest_size=100G scope=spfile",
		"alter system set db_recovery_file_dest='/u03/app/oracle/fast_recovery_area/TEST' scope=spfile",
		"alter system set diagnostic_dest='/u02/app/oracle' scope=spfile",
		"alter system set dispatchers='(PROTOCOL=TCP) (SERVICE=TESTXDB)' scope=spfile",
		"alter system set enable_pluggable_database=TRUE scope=spfile",
		"alter system set filesystemio_options=SETALL scope=spfile",
		"alter system set local_listener='(DESCRIPTION=(ADDRESS=(PROTOCOL=ipc)(KEY=REGLSNR_6021)))' scope=spfile",
		"alter system set open_cursors=300 scope=spfile",
		"alter system set processes=300 scope=spfile",
		"alter system set remote_login_passwordfile='EXCLUSIVE' scope=spfile",
		"alter system set undo_tablespace='UNDOTBS1' scope=spfile",
		"alter system set log_archive_dest_1='LOCATION=USE_DB_RECOVERY_FILE_DEST VALID_FOR=(ALL_LOGFILES,ALL_ROLES) DB_UNIQUE_NAME=TEST_generic' scope=spfile",
		"alter system set log_archive_dest_state_1=enable scope=spfile",
		"alter system set log_archive_format='%t_%s_%r.arch' scope=spfile",
		"alter system set standby_file_management=AUTO scope=spfile",
		"alter system set common_user_prefix='gcsql$' scope=spfile",
	}
	wantSQLsUnseeded := []string{
		"alter system set local_listener='(DESCRIPTION=(ADDRESS=(PROTOCOL=ipc)(KEY=REGLSNR_6021)))' scope=spfile",
	}
	wantSQLsSeeded := []string{
		fmt.Sprintf("alter system set pga_aggregate_target=%dM scope=spfile", testCDB.GetDatabaseParamPGATargetMB()),
		fmt.Sprintf("alter system set sga_target=%dM scope=spfile", testCDB.GetDatabaseParamSGATargetMB()),
		fmt.Sprintf("alter system set compatible='%s.0' scope=spfile", testCDB.GetVersion()),
	}

	testcases := []struct {
		name     string
		isSeeded bool
		wantSQLs []string
	}{
		{
			name:     "Set parameters for seeded instance",
			isSeeded: true,
			wantSQLs: append(wantSQLsCommon, wantSQLsSeeded...),
		},
		{
			name:     "Set parameters for usseeded instance",
			isSeeded: false,
			wantSQLs: append(wantSQLsCommon, wantSQLsUnseeded...),
		},
	}

	for _, tc := range testcases {
		bootstrapTask.isSeeded = tc.isSeeded
		var gotSQLs []string
		dbdServer.fakeRunSQLPlus = func(ctx context.Context, request *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
			gotSQLs = append(gotSQLs, request.GetCommands()...)
			return &dbdpb.RunCMDResponse{}, nil
		}

		if err := bootstrapTask.setParametersHelper(ctx); err != nil {
			t.Fatalf("BootstrapTask.setParametersHelper got %v, want nil", err)
		}

		if diff := cmp.Diff(tc.wantSQLs, gotSQLs); diff != "" {
			t.Errorf("BootstrapTask.setParametersHelper called unexpected sqls: -want +got %v", diff)
		}
	}
}

type fakeServer struct {
	*dbdpb.UnimplementedDatabaseDaemonServer
	fakeRunSQLPlus func(context.Context, *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error)
}

func (f *fakeServer) RunSQLPlus(ctx context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
	if f.fakeRunSQLPlus == nil {
		return nil, errors.New("RunSQLPlus fake not found")
	}
	return f.fakeRunSQLPlus(ctx, req)
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
