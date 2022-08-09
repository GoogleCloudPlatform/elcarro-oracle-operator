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
	"testing"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/google/go-cmp/cmp"
)

const showConfig = `Configuration - el-carro-operator-config

  Protection Mode: MaxPerformance
  Members:
  %s

Fast-Start Failover: %s

Configuration Status:
SUCCESS   (status updated 55 seconds ago)
`

func TestPromoteStandbyTaskRemoveDataGuardConfig(t *testing.T) {
	dbdServer := &fakeServer{}
	secretAccessor := &fakeSecretAccessor{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	task := newPromoteStandbyTask(
		ctx,
		&Primary{
			Host:             "123.123.123.123",
			Port:             6021,
			Service:          "GCLOUD.gke",
			User:             "sys",
			PasswordAccessor: secretAccessor,
		},
		&Standby{
			CDBName:      "GCLOUD",
			DBUniqueName: "gcloud_gke",
		},
		client)

	testCases := []struct {
		name               string
		showConfigResponse *dbdpb.RunDataGuardResponse
		wantCmds           []string
	}{
		{
			name: "Default configuration with only one standby",
			showConfigResponse: &dbdpb.RunDataGuardResponse{
				Output: []string{fmt.Sprintf(
					showConfig,
					"gcloud_uscentral1a - Primary database\ngcloud_gke- Physical standby database",
					"DISABLED",
				),
				},
			},
			wantCmds: []string{"remove configuration"},
		},
		{
			name: "Default configuration with multiple standbys",
			showConfigResponse: &dbdpb.RunDataGuardResponse{
				Output: []string{fmt.Sprintf(
					showConfig,
					"gcloud_uscentral1a - Primary database\ngcloud_gke- Physical standby database\ngcloud_gke_2- Physical standby database",
					"DISABLED",
				),
				},
			},
			wantCmds: []string{
				"disable database gcloud_gke",
				"remove database gcloud_gke",
			},
		},
		{
			name: "Default configuration with no standby",
			showConfigResponse: &dbdpb.RunDataGuardResponse{
				Output: []string{fmt.Sprintf(
					showConfig,
					"gcloud_uscentral1a - Primary database",
					"DISABLED",
				),
				},
			},
			wantCmds: nil,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var gotCmds []string
			dbdServer.fakeRunDataGuard = func(ctx context.Context, req *dbdpb.RunDataGuardRequest) (*dbdpb.RunDataGuardResponse, error) {
				scripts := req.GetScripts()
				for _, script := range scripts {
					if script == "show configuration" {
						return tc.showConfigResponse, nil
					}
				}
				gotCmds = append(gotCmds, scripts...)
				return &dbdpb.RunDataGuardResponse{Output: []string{"Done."}}, nil
			}
			secretAccessor.fakeGet = func(ctx context.Context) (string, error) {
				return "syspwd", nil
			}
			if err := task.removeDataGuardConfig(ctx); err != nil {
				t.Fatalf("task.removeDataGuardConfig(ctx) got %v, want nil", err)
			}
			if diff := cmp.Diff(tc.wantCmds, gotCmds); diff != "" {
				t.Errorf("task.removeDataGuardConfig(ctx) called unexpected cmds: -want +got %v", diff)
			}
		})
	}
}

func TestPromoteStandbyTaskRemoveDataGuardConfigErrors(t *testing.T) {
	dbdServer := &fakeServer{}
	secretAccessor := &fakeSecretAccessor{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	task := newPromoteStandbyTask(
		ctx,
		&Primary{
			Host:             "123.123.123.123",
			Port:             6021,
			Service:          "GCLOUD.gke",
			User:             "sys",
			PasswordAccessor: secretAccessor,
		},
		&Standby{
			CDBName:      "GCLOUD",
			DBUniqueName: "gcloud_gke",
		},
		client)

	testCases := []struct {
		name               string
		showConfigResponse *dbdpb.RunDataGuardResponse
		err                error
		wantCmds           []string
	}{
		{
			name: "Removing primary Data Guard configuration errors out",
			showConfigResponse: &dbdpb.RunDataGuardResponse{
				Output: []string{fmt.Sprintf(
					showConfig,
					"gcloud_uscentral1a - Primary database\ngcloud_gke- Physical standby database",
					"ENABLED",
				),
				},
			},
			err:      errors.New("Error: ORA-16654: fast-start failover is enabled\n \nFailed."),
			wantCmds: []string{"remove configuration"},
		},
		{
			name: "Disable standby database errors out",
			showConfigResponse: &dbdpb.RunDataGuardResponse{
				Output: []string{fmt.Sprintf(
					showConfig,
					"gcloud_uscentral1a - Primary database\ngcloud_gke- Physical standby database\ngcloud_gke_2- Physical standby database",
					"DISABLED",
				),
				},
			},
			err:      errors.New("Error: could not disable database\n \nFailed."),
			wantCmds: []string{"disable database gcloud_gke"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var gotCmds []string
			dbdServer.fakeRunDataGuard = func(ctx context.Context, req *dbdpb.RunDataGuardRequest) (*dbdpb.RunDataGuardResponse, error) {
				scripts := req.GetScripts()
				for _, script := range scripts {
					if script == "show configuration" {
						return tc.showConfigResponse, nil
					}
				}
				gotCmds = append(gotCmds, scripts...)
				return &dbdpb.RunDataGuardResponse{Output: []string{"Done."}}, tc.err
			}
			secretAccessor.fakeGet = func(ctx context.Context) (string, error) {
				return "syspwd", nil
			}
			if err := task.removeDataGuardConfig(ctx); err == nil {
				t.Fatalf("task.removeDataGuardConfig(ctx) got nil, want %v", tc.err)
			}
			if diff := cmp.Diff(tc.wantCmds, gotCmds); diff != "" {
				t.Errorf("task.removeDataGuardConfig(ctx) called unexpected cmds: -want +got %v", diff)
			}
		})
	}
}

func TestPromoteStandby(t *testing.T) {
	dbdServer := &fakeServer{}
	secretAccessor := &fakeSecretAccessor{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	task := newPromoteStandbyTask(
		ctx,
		&Primary{
			Host:             "123.123.123.123",
			Port:             6021,
			Service:          "GCLOUD.gke",
			User:             "sys",
			PasswordAccessor: secretAccessor,
		},
		&Standby{
			CDBName:      "GCLOUD",
			DBUniqueName: "gcloud_gke",
		},
		client)
	testCases := []struct {
		name        string
		queryToResp map[string][]string
		wantSQLs    []string
	}{
		{
			name: "managed recovery process exists",
			queryToResp: map[string][]string{
				consts.ListMRPSql:          {`{"PROCESS": "MRP0"}`},
				consts.CancelMRPSql:        {},
				consts.ListPrimaryRoleSql:  {},
				consts.ActivateStandbySql:  {},
				consts.ListOpenDatabaseSql: {},
				consts.OpenDatabaseSql:     {},
			},
			wantSQLs: []string{
				consts.ListMRPSql,
				consts.CancelMRPSql,
				consts.ListPrimaryRoleSql,
				consts.ActivateStandbySql,
				consts.ListOpenDatabaseSql,
				consts.OpenDatabaseSql,
			},
		},
		{
			name: "managed recovery process does not exists",
			queryToResp: map[string][]string{
				consts.ListMRPSql:          {},
				consts.CancelMRPSql:        {},
				consts.ListPrimaryRoleSql:  {},
				consts.ActivateStandbySql:  {},
				consts.ListOpenDatabaseSql: {},
				consts.OpenDatabaseSql:     {},
			},
			wantSQLs: []string{
				consts.ListMRPSql,
				consts.ListPrimaryRoleSql,
				consts.ActivateStandbySql,
				consts.ListOpenDatabaseSql,
				consts.OpenDatabaseSql,
			},
		},
		{
			name: "managed recovery process does not exists and db already in primary role",
			queryToResp: map[string][]string{
				consts.ListMRPSql:          {},
				consts.CancelMRPSql:        {},
				consts.ListPrimaryRoleSql:  {`{"DATABASE_ROLE": "PRIMARY"}`},
				consts.ActivateStandbySql:  {},
				consts.ListOpenDatabaseSql: {},
				consts.OpenDatabaseSql:     {},
			},
			wantSQLs: []string{
				consts.ListMRPSql,
				consts.ListPrimaryRoleSql,
				consts.ListOpenDatabaseSql,
				consts.OpenDatabaseSql,
			},
		},
		{
			name: "managed recovery process does not exists, db in primary and already opened",
			queryToResp: map[string][]string{
				consts.ListMRPSql:          {},
				consts.CancelMRPSql:        {},
				consts.ListPrimaryRoleSql:  {`{"DATABASE_ROLE": "PRIMARY"}`},
				consts.ActivateStandbySql:  {},
				consts.ListOpenDatabaseSql: {`{"INSTANCE_NAME": "GCLOUD"}`},
				consts.OpenDatabaseSql:     {},
			},
			wantSQLs: []string{
				consts.ListMRPSql,
				consts.ListPrimaryRoleSql,
				consts.ListOpenDatabaseSql,
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var gotSQLs []string
			dbdServer.fakeRunSQLPlusFormatted = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				val, ok := tc.queryToResp[req.GetCommands()[0]]
				if !ok {
					return nil, errors.New("query failed")
				}
				gotSQLs = append(gotSQLs, req.GetCommands()...)
				return &dbdpb.RunCMDResponse{Msg: val}, nil
			}
			dbdServer.fakeRunSQLPlus = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				val, ok := tc.queryToResp[req.GetCommands()[0]]
				if !ok {
					return nil, errors.New("query failed")
				}
				gotSQLs = append(gotSQLs, req.GetCommands()...)
				return &dbdpb.RunCMDResponse{Msg: val}, nil
			}
			secretAccessor.fakeGet = func(ctx context.Context) (string, error) {
				return "syspwd", nil
			}
			if err := task.promoteStandby(ctx); err != nil {
				t.Fatalf("task.promoteStandby(ctx) got %v, want nil", err)
			}
			if diff := cmp.Diff(tc.wantSQLs, gotSQLs); diff != "" {
				t.Errorf("task.promoteStandby(ctx) called unexpected sqls: -want +got %v", diff)
			}
		})
	}
}

func TestPromoteStandbyErrors(t *testing.T) {
	dbdServer := &fakeServer{}
	secretAccessor := &fakeSecretAccessor{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	task := newPromoteStandbyTask(
		ctx,
		&Primary{
			Host:             "123.123.123.123",
			Port:             6021,
			Service:          "GCLOUD.gke",
			User:             "sys",
			PasswordAccessor: secretAccessor,
		},
		&Standby{
			CDBName:      "GCLOUD",
			DBUniqueName: "gcloud_gke",
		},
		client)
	testCases := []struct {
		name        string
		queryToResp map[string][]string
		wantSQLs    []string
		err         error
	}{
		{
			name: "Promoting standby database errors out",
			queryToResp: map[string][]string{
				consts.ListMRPSql:         {`{"PROCESS": "MRP0"}`},
				consts.CancelMRPSql:       {},
				consts.ListPrimaryRoleSql: {},
				consts.ActivateStandbySql: {},
			},
			wantSQLs: []string{
				consts.ListMRPSql,
				consts.CancelMRPSql,
				consts.ListPrimaryRoleSql,
				consts.ActivateStandbySql,
			},
			err: errors.New("could not promote standby database"),
		},
		{
			name: "Opening new primary database errors out",
			queryToResp: map[string][]string{
				consts.ListMRPSql:          {},
				consts.CancelMRPSql:        {},
				consts.ListPrimaryRoleSql:  {`{"DATABASE_ROLE": "PRIMARY"}`},
				consts.ActivateStandbySql:  {},
				consts.ListOpenDatabaseSql: {},
				consts.OpenDatabaseSql:     {},
			},
			wantSQLs: []string{
				consts.ListMRPSql,
				consts.ListPrimaryRoleSql,
				consts.ListOpenDatabaseSql,
				consts.OpenDatabaseSql,
			},
			err: errors.New("could not open primary database"),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var gotSQLs []string
			dbdServer.fakeRunSQLPlusFormatted = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				val, ok := tc.queryToResp[req.GetCommands()[0]]
				if !ok {
					return nil, errors.New("query failed")
				}
				gotSQLs = append(gotSQLs, req.GetCommands()...)
				return &dbdpb.RunCMDResponse{Msg: val}, nil
			}
			dbdServer.fakeRunSQLPlus = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				val, ok := tc.queryToResp[req.GetCommands()[0]]
				if !ok {
					return nil, errors.New("query failed")
				}
				gotSQLs = append(gotSQLs, req.GetCommands()...)
				return &dbdpb.RunCMDResponse{Msg: val}, tc.err
			}
			secretAccessor.fakeGet = func(ctx context.Context) (string, error) {
				return "syspwd", nil
			}
			if err := task.promoteStandby(ctx); err == nil {
				t.Fatalf("task.promoteStandby(ctx) got nil, want %v", tc.err)
			}
			if diff := cmp.Diff(tc.wantSQLs, gotSQLs); diff != "" {
				t.Errorf("task.promoteStandby(ctx) called unexpected sqls: -want +got %v", diff)
			}
		})
	}
}
