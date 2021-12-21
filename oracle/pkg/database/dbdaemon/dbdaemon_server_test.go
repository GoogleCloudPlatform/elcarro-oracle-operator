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

package dbdaemon

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/godror/godror"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

func TestServerCreateDirs(t *testing.T) {
	ctx := context.Background()
	client, cleanup := newFakeDatabaseDaemonClient(t)
	defer cleanup()

	oldMask := syscall.Umask(0022)
	testDir, err := ioutil.TempDir("", "TestServerCreateDir")
	if err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}
	defer func() {
		syscall.Umask(oldMask)
		os.RemoveAll(testDir)
	}()
	testCases := []struct {
		name     string
		path     string
		perm     uint32
		wantPerm uint32
	}{
		{
			name:     "direct dir",
			path:     filepath.Join(testDir, "dir1"),
			perm:     0777,
			wantPerm: 0755,
		},
		{
			name:     "nested dirs",
			path:     filepath.Join(testDir, "dir2", "dir3"),
			perm:     0750,
			wantPerm: 0750,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.CreateDirs(ctx, &dbdpb.CreateDirsRequest{
				Dirs: []*dbdpb.CreateDirsRequest_DirInfo{
					{
						Path: tc.path,
						Perm: tc.perm,
					},
				},
			}); err != nil {
				t.Fatalf("dbdaemon.CreateDir failed: %v", err)
			}
			info, err := os.Stat(tc.path)
			if err != nil {
				t.Fatalf("dbdaemon.CreateDir os.Stat(%q) failed: %v", tc.path, err)
			}
			if !info.IsDir() {
				t.Errorf("dbdaemon.CreateDir got file %q, want dir", tc.path)
			}
			// the API set perm before umask , https://github.com/golang/go/issues/15210
			if info.Mode().Perm() != os.FileMode(tc.wantPerm) {
				t.Errorf("dbdaemon.CreateDir got file perm %q, want perm %q", info.Mode().Perm(), os.FileMode(tc.wantPerm))
			}
		})
	}
}

func TestServerReadDir(t *testing.T) {
	client, cleanup := newFakeDatabaseDaemonClient(t)
	defer cleanup()
	ctx := context.Background()

	testDir, err := ioutil.TempDir("", "TestServerReadDir")
	if err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}
	defer os.RemoveAll(testDir)
	testCases := []struct {
		name         string
		path         string
		recursive    bool
		dirs         []string
		files        []string
		wantCurrPath string
		wantSubPaths []string
	}{
		{
			name:         "file",
			path:         filepath.Join(testDir, "test1"),
			wantCurrPath: filepath.Join(testDir, "test1"),
		},
		{
			name:         "dir without content",
			path:         filepath.Join(testDir, "dir1"),
			wantCurrPath: filepath.Join(testDir, "dir1"),
		},
		{
			name:      "dir with contents recursive",
			path:      filepath.Join(testDir, "dir2"),
			recursive: true,
			dirs: []string{
				filepath.Join(testDir, "dir2", "dir3"),
				filepath.Join(testDir, "dir2", "dir3", "dir4"),
			},
			files: []string{
				filepath.Join(testDir, "dir2", "test2"),
				filepath.Join(testDir, "dir2", "dir3", "test3"),
				filepath.Join(testDir, "dir2", "dir3", "dir4", "test4"),
			},
			wantCurrPath: filepath.Join(testDir, "dir2"),
			wantSubPaths: []string{
				filepath.Join(testDir, "dir2/dir3"),
				filepath.Join(testDir, "dir2/dir3/dir4"),
				filepath.Join(testDir, "dir2/dir3/dir4/test4"),
				filepath.Join(testDir, "dir2/dir3/test3"),
				filepath.Join(testDir, "dir2/test2"),
			},
		},
		{
			name: "dir with contents not recursive",
			path: filepath.Join(testDir, "dir5"),
			dirs: []string{
				filepath.Join(testDir, "dir5", "dir3"),
				filepath.Join(testDir, "dir5", "dir3", "dir4"),
			},
			files: []string{
				filepath.Join(testDir, "dir5", "test2"),
				filepath.Join(testDir, "dir5", "dir3", "test3"),
				filepath.Join(testDir, "dir5", "dir3", "dir4", "test4"),
			},
			wantCurrPath: filepath.Join(testDir, "dir5"),
			wantSubPaths: []string{
				filepath.Join(testDir, "dir5/dir3"),
				filepath.Join(testDir, "dir5/test2"),
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(tc.path, 0755); err != nil {
				t.Fatalf("dbdaemon.DeleteDir failed to create test dir: %v", err)
			}
			for _, dir := range tc.dirs {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatalf("dbdaemon.DeleteDir failed to create test dir: %v", err)
				}
			}
			for _, file := range tc.files {
				f, err := os.Create(file)
				f.Close()
				if err != nil {
					t.Fatalf("dbdaemon.ReadDir failed to create test file: %v", err)
				}
			}

			resp, err := client.ReadDir(ctx, &dbdpb.ReadDirRequest{
				Path:      tc.path,
				Recursive: tc.recursive,
			})
			if err != nil {
				t.Fatalf("dbdaemon.ReadDir failed: %v", err)
			}
			gotCurrPath := resp.CurrPath.AbsPath
			var gotSubPaths []string
			for _, sp := range resp.SubPaths {
				gotSubPaths = append(gotSubPaths, sp.AbsPath)
			}
			if gotCurrPath != tc.wantCurrPath {
				t.Errorf("dbdaemon.ReadDir curr path %s, want %s", gotCurrPath, tc.wantCurrPath)
			}
			if diff := cmp.Diff(tc.wantSubPaths, gotSubPaths); diff != "" {
				t.Errorf("dbdaemon.ReadDir sub paths got unexpected files/dirs: -want +got %v", diff)
			}
			// verify fileInfo
			verifyPaths := append(tc.wantSubPaths, tc.wantCurrPath)
			gotInfo := append(resp.SubPaths, resp.CurrPath)
			for i, path := range verifyPaths {
				info, err := os.Stat(path)
				if err != nil {
					t.Errorf("dbdaemon.ReadDir os.Stat(%q) failed: %v", path, err)
					continue
				}
				if gotInfo[i].Name != info.Name() {
					t.Errorf("dbdaemon.ReadDir sub path %q, got name %v, want %v", path, gotInfo[i].Name, info.Name())
				}
				if gotInfo[i].Size != info.Size() {
					t.Errorf("dbdaemon.ReadDir sub path %q, got size %v, want %v", path, gotInfo[i].Size, info.Size())
				}
				if os.FileMode(gotInfo[i].Mode) != info.Mode() {
					t.Errorf("dbdaemon.ReadDir sub path %q, got mode %v, want %v", path, os.FileMode(gotInfo[i].Mode), info.Mode())
				}
				if gotInfo[i].IsDir != info.IsDir() {
					t.Errorf("dbdaemon.ReadDir sub path %q, got isDir %v, want %v", path, gotInfo[i].IsDir, info.IsDir())
				}
				if gotInfo[i].AbsPath != path {
					t.Errorf("dbdaemon.ReadDir sub path %q, got abs path %v, want %v", path, gotInfo[i].AbsPath, path)
				}
			}
		})
	}
}

func TestServerDeleteDir(t *testing.T) {
	ctx := context.Background()
	client, cleanup := newFakeDatabaseDaemonClient(t)
	defer cleanup()

	testDir, err := ioutil.TempDir("", "TestServerDeleteDir")
	if err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}
	defer os.RemoveAll(testDir)
	testCases := []struct {
		name  string
		path  string
		force bool
		files []string
		dirs  []string
	}{
		{
			name: "dir without content",
			path: filepath.Join(testDir, "dir1"),
		},
		{
			name:  "dir without content",
			force: true,
			path:  filepath.Join(testDir, "dir2"),
		},
		{
			name:  "dir with contents",
			path:  filepath.Join(testDir, "dir3"),
			force: true,
			files: []string{filepath.Join(testDir, "dir3", "test1")},
			dirs:  []string{filepath.Join(testDir, "dir3", "dir4")},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(tc.path, 0755); err != nil {
				t.Fatalf("dbdaemon.DeleteDir failed to create test dir: %v", err)
			}
			for _, file := range tc.files {
				f, err := os.Create(file)
				f.Close()
				if err != nil {
					t.Fatalf("dbdaemon.DeleteDir failed to create test file: %v", err)
				}
			}
			for _, dir := range tc.dirs {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatalf("dbdaemon.DeleteDir failed to create test dir: %v", err)
				}
			}
			if _, err := client.DeleteDir(ctx, &dbdpb.DeleteDirRequest{
				Path:  tc.path,
				Force: tc.force,
			}); err != nil {
				t.Fatalf("dbdaemon.DeleteDir failed: %v", err)
			}
			if _, err := os.Stat(tc.path); !os.IsNotExist(err) {
				t.Fatalf("dbdaemon.DeleteDir got %q exists, want not exists", tc.path)
			}
		})
	}
}

func TestServerDeleteDirErrors(t *testing.T) {
	ctx := context.Background()
	client, cleanup := newFakeDatabaseDaemonClient(t)
	defer cleanup()

	testDir, err := ioutil.TempDir("", "TestServerDeleteDirErrors")
	if err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}
	defer os.RemoveAll(testDir)
	testCases := []struct {
		name  string
		path  string
		files []string
		dirs  []string
	}{
		{
			name:  "dir with files contents",
			path:  filepath.Join(testDir, "dir1"),
			files: []string{filepath.Join(testDir, "dir1", "test1")},
		},
		{
			name: "dir with dirs contents",
			path: filepath.Join(testDir, "dir2"),
			dirs: []string{filepath.Join(testDir, "dir2", "dir3")},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(tc.path, 0755); err != nil {
				t.Fatalf("dbdaemon.DeleteDir failed to create test dir: %v", err)
			}
			for _, file := range tc.files {
				f, err := os.Create(file)
				f.Close()
				if err != nil {
					t.Fatalf("dbdaemon.DeleteDir failed to create test file: %v", err)
				}
			}
			for _, dir := range tc.dirs {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatalf("dbdaemon.DeleteDir failed to create test dir: %v", err)
				}
			}
			if _, err := client.DeleteDir(ctx, &dbdpb.DeleteDirRequest{
				Path: tc.path,
			}); err == nil {
				t.Errorf("dbdaemon.DeleteDir succeeded, want not-nil err")
			}
			// double check the dir still exists
			if _, err := os.Stat(tc.path); err != nil {
				t.Fatalf("dbdaemon.DeleteDir failed and got %q not exists, want exists", tc.path)
			}
		})
	}
}

func newFakeDatabaseDaemonClient(t *testing.T) (dbdpb.DatabaseDaemonClient, func()) {
	t.Helper()
	grpcSvr := grpc.NewServer()

	dbdpb.RegisterDatabaseDaemonServer(grpcSvr, &Server{})
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

func TestServerString(t *testing.T) {
	password := "myPassword456"
	s := Server{
		hostName:   "hostnameValue",
		pdbConnStr: fmt.Sprintf("username/%v@localhost:6021/GCLOUD.GCE", password),
	}

	stringVal := s.String()
	if strings.Contains(stringVal, password) {
		t.Errorf("server.String() got password in string %v, want string without password %v", stringVal, password)
	}
}

// Mock DB ('dbdaemon' interface)
type mockDB struct {
	setDatabaseUpgradeModeCount int
	openPDBsCount               int
}

func (m mockDB) shutdownDatabase(ctx context.Context, mode godror.ShutdownMode) error {
	panic("implement me")
}

func (m mockDB) startupDatabase(ctx context.Context, mode godror.StartupMode, s string) error {
	panic("implement me")
}

func (m *mockDB) setDatabaseUpgradeMode(ctx context.Context) error {
	m.setDatabaseUpgradeModeCount++
	return nil
}

func (m *mockDB) openPDBs(ctx context.Context) error {
	m.openPDBsCount++
	return nil
}

func (m mockDB) runSQL(ctx context.Context, i []string, b bool, b2 bool, database oracleDatabase) ([]string, error) {
	panic("implement me")
}

func (m mockDB) runQuery(ctx context.Context, i []string, database oracleDatabase) ([]string, error) {
	panic("implement me")
}

// Mock dbdaemon_proxy client
type mockDatabaseDaemonProxyClient struct {
	startupCount  int
	shutdownCount int
}

func (m *mockDatabaseDaemonProxyClient) BounceDatabase(ctx context.Context, in *dbdpb.BounceDatabaseRequest, opts ...grpc.CallOption) (*dbdpb.BounceDatabaseResponse, error) {
	if in.Operation == dbdpb.BounceDatabaseRequest_STARTUP {
		m.startupCount++
	} else if in.Operation == dbdpb.BounceDatabaseRequest_SHUTDOWN {
		m.shutdownCount++
	}
	return new(dbdpb.BounceDatabaseResponse), nil
}

func (m *mockDatabaseDaemonProxyClient) BounceListener(ctx context.Context, in *dbdpb.BounceListenerRequest, opts ...grpc.CallOption) (*dbdpb.BounceListenerResponse, error) {
	panic("implement me")
}

func (m *mockDatabaseDaemonProxyClient) ProxyRunDbca(ctx context.Context, in *dbdpb.ProxyRunDbcaRequest, opts ...grpc.CallOption) (*dbdpb.ProxyRunDbcaResponse, error) {
	panic("implement me")
}

func (m *mockDatabaseDaemonProxyClient) ProxyRunNID(ctx context.Context, in *dbdpb.ProxyRunNIDRequest, opts ...grpc.CallOption) (*dbdpb.ProxyRunNIDResponse, error) {
	panic("implement me")
}

func (m *mockDatabaseDaemonProxyClient) SetEnv(ctx context.Context, in *dbdpb.SetEnvRequest, opts ...grpc.CallOption) (*dbdpb.SetEnvResponse, error) {
	panic("implement me")
}

func (m *mockDatabaseDaemonProxyClient) ProxyRunInitOracle(ctx context.Context, in *dbdpb.ProxyRunInitOracleRequest, opts ...grpc.CallOption) (*dbdpb.ProxyRunInitOracleResponse, error) {
	panic("implement me")
}

func (m *mockDatabaseDaemonProxyClient) ProxyFetchServiceImageMetaData(ctx context.Context, in *dbdpb.ProxyFetchServiceImageMetaDataRequest, opts ...grpc.CallOption) (*dbdpb.ProxyFetchServiceImageMetaDataResponse, error) {
	return &dbdpb.ProxyFetchServiceImageMetaDataResponse{Version: "12.2", OracleHome: "/u01/app/oracle/product/12.2/db", CdbName: "MYDB"}, nil
}

func (m *mockDatabaseDaemonProxyClient) SetDnfsState(ctx context.Context, in *dbdpb.SetDnfsStateRequest, opts ...grpc.CallOption) (*dbdpb.SetDnfsStateResponse, error) {
	panic("implement me")
}

// Mock osUtil
type mockOsUtil struct {
	commands []string
}

func (m *mockOsUtil) runCommand(bin string, params []string) error {
	m.commands = append(m.commands, bin)
	return nil
}

func (m *mockOsUtil) isReturnCodeEqual(err error, code int) bool {
	panic("implement me")
}

func (m *mockOsUtil) createFile(file string, content io.Reader) error {
	panic("implement me")
}

func (m *mockOsUtil) removeFile(file string) error {
	panic("implement me")
}

func NewMockServer(ctx context.Context, cdbNameFromYaml string) (*Server, error) {

	s := &Server{
		hostName:       "MOCK_HOST",
		database:       &mockDB{},
		osUtil:         &mockOsUtil{},
		databaseSid:    &syncState{},
		dbdClient:      &mockDatabaseDaemonProxyClient{},
		dbdClientClose: nil,
		lroServer:      nil,
		syncJobs:       &syncJobs{},
		gcsUtil:        &gcsUtilImpl{},
	}
	s.databaseHome = "DBHOME"
	s.databaseSid.val = "MOCK_DB"
	return s, nil
}
