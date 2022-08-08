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
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/google/go-cmp/cmp"
)

const (
	db_domain = "gke"
	db_name   = "gcloud"
)

func TestBootstrapStandbyTaskInitStandbyMetadata(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()

	testCases := []struct {
		name                string
		queryToResp         map[string][]string
		sqlErr              error
		wantErr             bool
		wantStandbyMetadata *standbyMetadata
	}{
		{
			name: "Standby metadata initialization success",
			queryToResp: map[string][]string{
				standbyMetadataSQL: {fmt.Sprintf(`{"DB_DOMAIN": "%s"}`, db_domain), fmt.Sprintf(`{"DB_NAME": "%s"}`, db_name)},
			},
			wantErr: false,
			wantStandbyMetadata: &standbyMetadata{
				dbDomain: db_domain,
				dbName:   db_name,
			},
		},
		{
			name: "Standby metadata initialization fail",
			queryToResp: map[string][]string{
				standbyMetadataSQL: {fmt.Sprintf(`{"DB_DOMAIN": "%s"}`, db_domain), fmt.Sprintf(`{"DB_NAME": "%s"}`, db_name)},
			},
			sqlErr:  errors.New("SQL failed"),
			wantErr: true,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			dbdServer.fakeRunSQLPlusFormatted = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				val, ok := tt.queryToResp[req.GetCommands()[0]]
				if !ok {
					return nil, errors.New("query failed")
				}
				return &dbdpb.RunCMDResponse{Msg: val}, tt.sqlErr
			}

			task := &bootstrapStandbyTask{
				standbyMetadata: &standbyMetadata{},
				dbdClient:       client,
			}

			err := task.initStandbyMetadata(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("Got err=%v, want err=%v", err, tt.sqlErr)
			}

			if err != nil {
				return
			}

			if diff := cmp.Diff(task.standbyMetadata, tt.wantStandbyMetadata, cmp.AllowUnexported(standbyMetadata{})); diff != "" {
				t.Errorf("Unexpected standby metadata (-want +got):\n%v", diff)
			}
		})
	}
}

func TestBootstrapStandbyTaskSetupUsers(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()

	testCases := []struct {
		name    string
		sqlErr  error
		wantErr bool
	}{
		{
			name:    "Setup users success",
			wantErr: false,
		},
		{
			name:    "Setup users fail",
			sqlErr:  errors.New("SQL failed"),
			wantErr: true,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			dbdServer.fakeRunSQLPlusFormatted = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				return &dbdpb.RunCMDResponse{}, tt.sqlErr
			}
			dbdServer.fakeRunSQLPlus = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				return &dbdpb.RunCMDResponse{}, tt.sqlErr
			}

			task := &bootstrapStandbyTask{
				standbyMetadata: &standbyMetadata{dbDomain: db_domain, dbName: db_name},
				dbdClient:       client,
			}

			err := task.setupUsers(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("Got err=%v, want err=%v", err, tt.sqlErr)
			}
		})
	}
}

func TestBootstrapStandbyCreateListener(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()

	testCases := []struct {
		name              string
		createListenerErr error
		wantErr           bool
	}{
		{
			name:    "Create listener success",
			wantErr: false,
		},
		{
			name:              "Create listener fail",
			createListenerErr: errors.New("Create listener failed"),
			wantErr:           true,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			dbdServer.fakeCreateListener = func(_ context.Context, req *dbdpb.CreateListenerRequest) (*dbdpb.CreateListenerResponse, error) {
				return &dbdpb.CreateListenerResponse{}, tt.createListenerErr
			}
			task := &bootstrapStandbyTask{
				standbyMetadata: &standbyMetadata{dbDomain: db_domain, dbName: db_name},
				dbdClient:       client,
			}

			err := task.createListener(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("Got err=%v, want err=%v", err, tt.createListenerErr)
			}
		})
	}
}

func TestBootstrapStandbyCreateSPFile(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()

	spfileLoc := filepath.Join(configBaseDir, db_name, fmt.Sprintf("spfile%s.ora", db_name))
	testCases := []struct {
		name          string
		fileExists    bool
		fileExistsErr error
		sqlErr        error
		shutdownDBErr error
		startupDBErr  error
		wantErr       bool
		wantSQL       []string
	}{
		{
			name:       "Success spfile already exist",
			fileExists: true,
			wantErr:    false,
		},
		{
			name:    "Success created spfile",
			wantSQL: []string{fmt.Sprintf("create spfile='%s' from memory", spfileLoc)},
			wantErr: false,
		},
		{
			name:          "Fail to check spfile exist",
			fileExistsErr: errors.New("FileExists failed"),
			wantErr:       true,
		},
		{
			name:          "Fail to shutdown database",
			shutdownDBErr: errors.New("Shutdown database failed"),
			wantErr:       true,
		},
		{
			name:         "Fail to startup database",
			startupDBErr: errors.New("Startup database failed"),
			wantErr:      true,
		},
	}

	for _, tt := range testCases {
		var gotSQL []string
		t.Run(tt.name, func(t *testing.T) {
			dbdServer.fakeFileExists = func(_ context.Context, req *dbdpb.FileExistsRequest) (*dbdpb.FileExistsResponse, error) {
				return &dbdpb.FileExistsResponse{Exists: tt.fileExists}, tt.fileExistsErr
			}
			dbdServer.fakeBounceDatabase = func(_ context.Context, req *dbdpb.BounceDatabaseRequest) (*dbdpb.BounceDatabaseResponse, error) {
				switch req.GetOperation() {
				case dbdpb.BounceDatabaseRequest_SHUTDOWN:
					return &dbdpb.BounceDatabaseResponse{}, tt.shutdownDBErr
				case dbdpb.BounceDatabaseRequest_STARTUP:
					return &dbdpb.BounceDatabaseResponse{}, tt.startupDBErr
				default:
					t.Errorf("Unwanted bounce database operation: %v", req.GetOperation())
				}
				return &dbdpb.BounceDatabaseResponse{}, nil
			}
			dbdServer.fakeRunSQLPlus = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				gotSQL = append(gotSQL, req.Commands...)
				return &dbdpb.RunCMDResponse{}, tt.sqlErr
			}

			task := &bootstrapStandbyTask{
				standbyMetadata: &standbyMetadata{dbDomain: db_domain, dbName: db_name},
				dbdClient:       client,
			}

			err := task.createSPFile(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("Got err=%v, but expected err: %v", err, tt.wantErr)
			}

			if err != nil {
				return
			}

			if diff := cmp.Diff(tt.wantSQL, gotSQL); diff != "" {
				t.Errorf("Unexpected sql. Diff (-want +got) %v", diff)
			}
		})
	}
}

func TestBootstrapStandbyMarkProvisionDone(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()

	testCases := []struct {
		name          string
		createFileErr error
		wantPath      string
		wantErr       bool
	}{
		{
			name:     "Create provisioning done file success",
			wantPath: consts.ProvisioningDoneFile,
		},
		{
			name:          "Create provisioning done file fail",
			wantPath:      consts.ProvisioningDoneFile,
			createFileErr: errors.New("Create file error"),
			wantErr:       true,
		},
	}

	for _, tt := range testCases {
		var gotPath string
		t.Run(tt.name, func(t *testing.T) {
			dbdServer.fakeCreateFile = func(_ context.Context, req *dbdpb.CreateFileRequest) (*dbdpb.CreateFileResponse, error) {
				gotPath = req.GetPath()
				return &dbdpb.CreateFileResponse{}, tt.createFileErr
			}

			task := &bootstrapStandbyTask{
				standbyMetadata: &standbyMetadata{dbDomain: db_domain, dbName: db_name},
				dbdClient:       client,
			}

			err := task.markProvisionDone(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("Got err=%v, but expected err: %v", err, tt.wantErr)
			}

			if err != nil {
				return
			}

			if diff := cmp.Diff(tt.wantPath, gotPath); diff != "" {
				t.Errorf("Unexpected file path. Diff (-want +got) %v", diff)
			}
		})
	}
}

func TestBootstrapStandbyOpenPDBs(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()

	testCases := []struct {
		name    string
		sqlErr  error
		wantErr bool
		wantSQL []string
	}{
		{
			name:    "Open PDBs success",
			wantSQL: []string{consts.OpenPluggableDatabaseSQL},
		},
		{
			name:    "Open PDBs fail",
			sqlErr:  errors.New("SQL failed"),
			wantSQL: []string{consts.OpenPluggableDatabaseSQL},
			wantErr: true,
		},
	}

	for _, tt := range testCases {
		var gotSQL []string
		t.Run(tt.name, func(t *testing.T) {
			dbdServer.fakeRunSQLPlus = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				gotSQL = append(gotSQL, req.Commands...)
				return &dbdpb.RunCMDResponse{}, tt.sqlErr
			}

			task := &bootstrapStandbyTask{
				standbyMetadata: &standbyMetadata{dbDomain: db_domain, dbName: db_name},
				dbdClient:       client,
			}

			err := task.openPDBs(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("Got err=%v, but expected err: %v", err, tt.wantErr)
			}

			if err != nil {
				return
			}

			if diff := cmp.Diff(tt.wantSQL, gotSQL); diff != "" {
				t.Errorf("Unexpected sql. Diff (-want +got) %v", diff)
			}
		})
	}
}
