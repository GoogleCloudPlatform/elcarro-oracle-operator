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

package dbdaemonproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/godror/godror"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

type fakeOsUtil struct {
	fakeRunCommand func(bin string, params []string) error
}

func (fake *fakeOsUtil) runCommand(bin string, params []string) error {
	if fake.fakeRunCommand == nil {
		return errors.New("fake impl not provided")
	}
	return fake.fakeRunCommand(bin, params)
}

type fakeDatabase struct {
	fakeExecContext func(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	fakeClose       func() error
}

func (fake *fakeDatabase) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	if fake.fakeExecContext == nil {
		return nil, errors.New("fake impl not provided")
	}
	return fake.fakeExecContext(ctx, query, args)
}

func (fake *fakeDatabase) Close() error {
	if fake.fakeClose == nil {
		return errors.New("fake impl not provided")
	}
	return fake.fakeClose()
}

type fakeConn struct {
	fakeStartup  func(godror.StartupMode) error
	fakeShutdown func(godror.ShutdownMode) error
}

func (fake *fakeConn) Startup(mode godror.StartupMode) error {
	if fake.fakeStartup == nil {
		return errors.New("fake impl not provided")
	}
	return fake.fakeStartup(mode)
}

func (fake *fakeConn) Shutdown(mode godror.ShutdownMode) error {
	if fake.fakeShutdown == nil {
		return errors.New("fake impl not provided")
	}
	return fake.fakeShutdown(mode)
}

func TestBounceListener(t *testing.T) {
	ctx := context.Background()
	client, cleanup := newFakeDatabaseDaemonProxyClient(t,
		&Server{
			databaseSid: &syncState{val: "MYDB"},
			osUtil: &fakeOsUtil{
				fakeRunCommand: func(bin string, params []string) error {
					return nil
				},
			},
		},
	)
	defer cleanup()
	testCases := []struct {
		name    string
		request *dbdpb.BounceListenerRequest
		want    *dbdpb.BounceListenerResponse
	}{
		{
			name: "valid listener start request",
			request: &dbdpb.BounceListenerRequest{
				ListenerName: "fakeListenerName",
				TnsAdmin:     "fakeTnsAdmin",
				Operation:    dbdpb.BounceListenerRequest_START,
			},
			want: &dbdpb.BounceListenerResponse{
				ListenerState: dbdpb.ListenerState_UP,
				ErrorMsg:      nil,
			},
		},
		{
			name: "valid listener stop request",
			request: &dbdpb.BounceListenerRequest{
				ListenerName: "fakeListenerName",
				TnsAdmin:     "fakeTnsAdmin",
				Operation:    dbdpb.BounceListenerRequest_STOP,
			},
			want: &dbdpb.BounceListenerResponse{
				ListenerState: dbdpb.ListenerState_DOWN,
				ErrorMsg:      nil,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {

			got, err := client.BounceListener(ctx, tc.request)
			if err != nil {
				t.Fatalf("BounceListener(ctx) failed: %v", err)
			}
			if diff := cmp.Diff(tc.want.ListenerState, got.ListenerState); diff != "" {
				t.Errorf("BounceListener got unexpected response: -want +got %v", diff)
			}
		})
	}
}

func TestBounceListenerErrors(t *testing.T) {
	ctx := context.Background()
	server := &Server{
		databaseSid: &syncState{val: "MYDB"},
	}
	client, cleanup := newFakeDatabaseDaemonProxyClient(t, server)
	defer cleanup()
	testCases := []struct {
		name    string
		util    osUtil
		request *dbdpb.BounceListenerRequest
	}{
		{
			name: "invalid listener wrong operation request",
			util: &fakeOsUtil{
				fakeRunCommand: func(bin string, params []string) error {
					return nil
				},
			},
			request: &dbdpb.BounceListenerRequest{
				ListenerName: "fakeListenerName",
				TnsAdmin:     "fakeTnsAdmin",
				Operation:    dbdpb.BounceListenerRequest_UNKNOWN,
			},
		},
		{
			name: "failed to run lsnrctl",
			util: &fakeOsUtil{
				fakeRunCommand: func(bin string, params []string) error {
					return errors.New("fake error")
				},
			},
			request: &dbdpb.BounceListenerRequest{
				ListenerName: "fakeListenerName",
				TnsAdmin:     "fakeTnsAdmin",
				Operation:    dbdpb.BounceListenerRequest_START,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server.osUtil = tc.util
			if _, err := client.BounceListener(ctx, tc.request); err == nil {
				t.Fatalf("BounceListener(ctx) succeeded, want not-nil error")
			}
		})
	}
}

func TestBounceDatabase(t *testing.T) {
	db1 := &fakeDatabase{
		fakeExecContext: func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
			return nil, nil
		},
		fakeClose: func() error {
			return nil
		},
	}
	db2 := &fakeDatabase{
		fakeClose: func() error {
			return nil
		},
	}

	c := &fakeConn{
		fakeStartup: func(mode godror.StartupMode) error {
			return nil
		},
	}

	sqlOpenBak := sqlOpen
	godrorDriverConnBak := godrorDriverConn
	defer func() {
		sqlOpen = sqlOpenBak
		godrorDriverConn = godrorDriverConnBak
	}()

	sqlOpen = func(driverName, dataSourceName string) (database, error) {
		switch dataSourceName {
		case "oracle://?sysdba=1&prelim=1":
			return db1, nil
		case "oracle://?sysdba=1":
			return db2, nil
		default:
			return nil, fmt.Errorf("failed to find mock db for %s", dataSourceName)
		}
	}
	godrorDriverConn = func(ctx context.Context, ex godror.Execer) (conn, error) {
		return c, nil
	}

	ctx := context.Background()
	server := &Server{
		databaseSid: &syncState{val: "MYDB"},
	}
	client, cleanup := newFakeDatabaseDaemonProxyClient(t, server)
	defer cleanup()
	testCases := []struct {
		name        string
		request     *dbdpb.BounceDatabaseRequest
		wantSQLs    []string
		execContext func(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
		shutdown    func(mode godror.ShutdownMode) error
	}{
		{
			name: "valid bounce CDB: startup (normal)",
			request: &dbdpb.BounceDatabaseRequest{
				DatabaseName: "GCLOUD",
				Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
				Option:       "",
			},
			wantSQLs: []string{
				"alter database mount",
				"alter database open",
			},
			execContext: func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
				return nil, nil
			},
		},
		{
			name: "valid bounce CDB: startup (nomount)",
			request: &dbdpb.BounceDatabaseRequest{
				DatabaseName: "GCLOUD",
				Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
				Option:       "nomount",
			},
			execContext: func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
				return nil, nil
			},
		},
		{
			name: "valid bounce CDB: startup (force_nomount)",
			request: &dbdpb.BounceDatabaseRequest{
				DatabaseName: "GCLOUD",
				Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
				Option:       "force_nomount",
			},
			execContext: func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
				return nil, nil
			},
		},
		{
			name: "valid bounce CDB: startup (mount)",
			request: &dbdpb.BounceDatabaseRequest{
				DatabaseName: "GCLOUD",
				Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
				Option:       "mount",
			},
			wantSQLs: []string{
				"alter database mount",
			},
			execContext: func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
				return nil, nil
			},
		},
		{
			name: "valid bounce CDB: startup (open)",
			request: &dbdpb.BounceDatabaseRequest{
				DatabaseName: "GCLOUD",
				Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
				Option:       "open",
			},
			wantSQLs: []string{
				"alter database mount",
				"alter database open",
			},
			execContext: func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
				return nil, nil
			},
		},
		{
			name: "valid bounce CDB: shutdown (normal)",
			request: &dbdpb.BounceDatabaseRequest{
				DatabaseName: "GCLOUD",
				Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
				Option:       "",
			},
			wantSQLs: []string{
				"alter database close normal",
				"alter database dismount",
			},
			execContext: func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
				return nil, nil
			},
		},
		{
			name: "valid bounce CDB: shutdown (normal) ignore database is already closed",
			request: &dbdpb.BounceDatabaseRequest{
				DatabaseName: "GCLOUD",
				Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
				Option:       "",
			},
			wantSQLs: []string{
				"alter database close normal",
				"alter database dismount",
			},
			execContext: func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
				return nil, errors.New("ORA-01507: already closed")
			},
		},
		{
			name: "valid bounce CDB: shutdown (normal) ignore oracle not available",
			request: &dbdpb.BounceDatabaseRequest{
				DatabaseName: "GCLOUD",
				Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
				Option:       "",
			},
			execContext: func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
				return nil, nil
			},
			shutdown: func(mode godror.ShutdownMode) error {
				return errors.New("ORA-01034: ORACLE not available")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var gotSQLs []string
			db2.fakeExecContext = func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
				gotSQLs = append(gotSQLs, query)
				return tc.execContext(ctx, query, args)
			}
			c.fakeShutdown = func(mode godror.ShutdownMode) error {
				return nil
			}
			if tc.shutdown != nil {
				c.fakeShutdown = tc.shutdown
			}
			if _, err := client.BounceDatabase(ctx, tc.request); err != nil {
				t.Errorf("BounceDatabase(ctx, %v) failed: %v", tc.request, err)
			}
			if diff := cmp.Diff(tc.wantSQLs, gotSQLs); diff != "" {
				t.Errorf("BounceDatabase got unexpected SQLs: -want +got %v", diff)
			}
		})
	}
}

func TestBounceDatabaseErrors(t *testing.T) {
	db1 := &fakeDatabase{
		fakeExecContext: func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
			return nil, nil
		},
		fakeClose: func() error {
			return nil
		},
	}
	db2 := &fakeDatabase{
		fakeClose: func() error {
			return nil
		},
	}

	c := &fakeConn{}
	sqlOpenBak := sqlOpen
	godrorDriverConnBak := godrorDriverConn
	defer func() {
		sqlOpen = sqlOpenBak
		godrorDriverConn = godrorDriverConnBak
	}()

	sqlOpen = func(driverName, dataSourceName string) (database, error) {
		switch dataSourceName {
		case "oracle://?sysdba=1&prelim=1":
			return db1, nil
		case "oracle://?sysdba=1":
			return db2, nil
		default:
			return nil, fmt.Errorf("failed to find mock db for %s", dataSourceName)
		}
	}
	godrorDriverConn = func(ctx context.Context, ex godror.Execer) (conn, error) {
		return c, nil
	}

	ctx := context.Background()
	server := &Server{
		databaseSid: &syncState{val: "MYDB"},
	}
	client, cleanup := newFakeDatabaseDaemonProxyClient(t, server)
	defer cleanup()
	testCases := []struct {
		name        string
		request     *dbdpb.BounceDatabaseRequest
		execContext func(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
		startup     func(mode godror.StartupMode) error
		shutdown    func(mode godror.ShutdownMode) error
	}{
		{
			name: "invalid bounce CDB",
			request: &dbdpb.BounceDatabaseRequest{
				DatabaseName: "GCLOUD",
				Operation:    dbdpb.BounceDatabaseRequest_UNKNOWN,
			},
		},
		{
			name: "failed to startup CDB",
			request: &dbdpb.BounceDatabaseRequest{
				DatabaseName: "GCLOUD",
				Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
			},
			startup: func(mode godror.StartupMode) error {
				return errors.New("fake error")
			},
		},
		{
			name: "failed to open CDB",
			request: &dbdpb.BounceDatabaseRequest{
				DatabaseName: "GCLOUD",
				Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
			},
			execContext: func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
				return nil, errors.New("fake error")
			},
		},
		{
			name: "failed to shutdown CDB",
			request: &dbdpb.BounceDatabaseRequest{
				DatabaseName: "GCLOUD",
				Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
			},
			shutdown: func(mode godror.ShutdownMode) error {
				return errors.New("fake error")
			},
		},
		{
			name: "failed to close CDB",
			request: &dbdpb.BounceDatabaseRequest{
				DatabaseName: "GCLOUD",
				Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
			},
			execContext: func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
				return nil, errors.New("fake error")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db2.fakeExecContext = func(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
				return nil, nil
			}
			if tc.execContext != nil {
				db2.fakeExecContext = tc.execContext
			}
			c.fakeStartup = func(mode godror.StartupMode) error {
				return nil
			}
			if tc.startup != nil {
				c.fakeStartup = tc.startup
			}
			c.fakeShutdown = func(mode godror.ShutdownMode) error {
				return nil
			}
			if tc.shutdown != nil {
				c.fakeShutdown = tc.shutdown
			}
			if _, err := client.BounceDatabase(ctx, tc.request); err == nil {
				t.Errorf("BounceDatabase(ctx, %v) succeeded, want not-nil error", tc.request)
			}
		})
	}
}

func TestProxyRunDbca(t *testing.T) {
	ctx := context.Background()
	server := &Server{
		databaseSid: &syncState{val: "MYDB"},
	}
	client, cleanup := newFakeDatabaseDaemonProxyClient(t, server)
	testDir, err := ioutil.TempDir("", "TestProxyRunDbca")
	if err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}
	fakeDBName := "DBCADB"
	fakeHome := filepath.Join(testDir, "u01")
	fakeDataMount := filepath.Join(testDir, "u02")
	dataMountBak := consts.DataMount
	consts.DataMount = fakeDataMount
	fakeConfigDir := fmt.Sprintf(consts.ConfigDir, consts.DataMount, fakeDBName)
	sourceConfigDir := filepath.Join(fakeHome, "dbs")
	consts.OracleDir = "/tmp"
	for _, dir := range []string{sourceConfigDir, fakeConfigDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to set up test dir %v", err)
		}
	}

	defer func() {
		cleanup()
		os.RemoveAll(testDir)
		consts.DataMount = dataMountBak
	}()

	testCases := []struct {
		name       string
		request    *dbdpb.ProxyRunDbcaRequest
		wantBin    string
		wantParams []string
		wantSid    string
		wantDBHome string
	}{
		{
			name: "RunDbca success",
			request: &dbdpb.ProxyRunDbcaRequest{
				OracleHome:   fakeHome,
				DatabaseName: fakeDBName,
				Params:       []string{"-createDatabase", "-databaseType", "MULTIPURPOSE"},
			},
			wantBin:    filepath.Join(fakeHome, "bin/dbca"),
			wantParams: []string{"-createDatabase", "-databaseType", "MULTIPURPOSE"},
			wantSid:    fakeDBName,
			wantDBHome: fakeHome,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBin string
			var gotParams []string
			server.osUtil = &fakeOsUtil{
				fakeRunCommand: func(bin string, params []string) error {
					gotBin = bin
					gotParams = append(gotParams, params...)
					for _, f := range []string{fmt.Sprintf("spfile%s.ora", fakeDBName), fmt.Sprintf("orapw%s", fakeDBName)} {
						if _, err := os.Create(filepath.Join(sourceConfigDir, f)); err != nil {
							t.Fatalf("failed to set up config file %v", err)
						}
					}
					return nil
				},
			}
			if _, err := client.ProxyRunDbca(ctx, tc.request); err != nil {
				t.Fatalf("ProxyRunDbca(ctx, %v) failed: %v", tc.request, err)
			}
			if gotBin != tc.wantBin {
				t.Errorf("ProxyRunDbca executed %v, want %v", gotBin, tc.wantBin)
			}
			if diff := cmp.Diff(gotParams, tc.wantParams); diff != "" {
				t.Errorf("ProxyRunDbca  executed unexpected params: -want +got %v", diff)
			}
			if server.databaseSid.val != tc.wantSid {
				t.Errorf("ProxyRunDbca set sid to %v, want %v", server.databaseSid.val, tc.wantSid)
			}
			if server.databaseHome != tc.wantDBHome {
				t.Errorf("ProxyRunDbca set DB home to %v, want %v", server.databaseHome, tc.wantDBHome)
			}
		})
	}
}

func TestProxyRunDbcaErrors(t *testing.T) {
	ctx := context.Background()
	server := &Server{
		databaseSid: &syncState{val: "MYDB"},
	}
	client, cleanup := newFakeDatabaseDaemonProxyClient(t, server)
	testDir, err := ioutil.TempDir("", "TestProxyRunDbca")
	if err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}
	fakeDBName := "DBCADB"
	fakeHome := filepath.Join(testDir, "u01")
	fakeDataMount := filepath.Join(testDir, "u02")
	dataMountBak := consts.DataMount
	consts.DataMount = fakeDataMount
	fakeConfigDir := fmt.Sprintf(consts.ConfigDir, consts.DataMount, fakeDBName)
	for _, dir := range []string{filepath.Join(fakeHome, "dbs"), fakeConfigDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to set up test dir %v", err)
		}
	}
	defer func() {
		cleanup()
		os.RemoveAll(testDir)
		consts.DataMount = dataMountBak
	}()

	request := &dbdpb.ProxyRunDbcaRequest{
		OracleHome:   fakeHome,
		DatabaseName: fakeDBName,
		Params:       []string{"-createDatabase", "-databaseType", "MULTIPURPOSE"},
	}

	server.osUtil = &fakeOsUtil{
		fakeRunCommand: func(bin string, params []string) error {
			return errors.New("fake error")
		},
	}
	if _, err := client.ProxyRunDbca(ctx, request); err == nil {
		t.Fatalf("ProxyRunDbca(ctx, %v) succeeded, want not-nil error", request)
	}
}

func newFakeDatabaseDaemonProxyClient(t *testing.T, server *Server) (dbdpb.DatabaseDaemonProxyClient, func()) {
	t.Helper()
	grpcSvr := grpc.NewServer()

	dbdpb.RegisterDatabaseDaemonProxyServer(grpcSvr, server)
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
	return dbdpb.NewDatabaseDaemonProxyClient(dbdConn), func() {
		dbdConn.Close()
		grpcSvr.GracefulStop()
	}
}
