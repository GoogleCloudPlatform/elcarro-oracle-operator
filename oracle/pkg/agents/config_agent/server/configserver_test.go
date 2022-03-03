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
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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

func TestConfigServerUsersChanged(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	newDBDClientBak := newDBDClient
	newDBDClient = func(context.Context, *ConfigServer) (dbdpb.DatabaseDaemonClient, func() error, error) {
		return client, func() error { return nil }, nil
	}
	defer func() {
		newDBDClient = newDBDClientBak
		cleanup()
	}()
	ctx := context.Background()
	testCases := []struct {
		name                string
		sqlToResp           map[string][]string
		req                 *pb.UsersChangedRequest
		wantChanged         bool
		wantSuppressedUsers []string
	}{
		{
			name:      "no user changed",
			sqlToResp: sampleSqlToResp,
			req: &pb.UsersChangedRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:         "superuser",
						Password:     "superpassword",
						LastPassword: "superpassword",
						Privileges:   []string{"dba"},
					},
					{
						Name:         "scott",
						Password:     "tiger",
						LastPassword: "tiger",
						Privileges:   []string{"connect", "resource", "unlimited tablespace"},
					},
					{
						Name:         "proberuser",
						Password:     "proberpassword",
						LastPassword: "proberpassword",
						Privileges:   []string{"create session"},
					},
				},
			},
			wantChanged: false,
		},
		{
			name: "added a user in spec",
			sqlToResp: map[string][]string{
				`alter session set container="MYDB";select username from dba_users where ORACLE_MAINTAINED='N' and INHERITED='NO'`: {
					`{"USERNAME": "GPDB_ADMIN"}`,
					`{"USERNAME": "SUPERUSER"}`,
					`{"USERNAME": "PROBERUSER"}`,
				},
				`alter session set container="MYDB";select privilege from dba_sys_privs where grantee='SUPERUSER'`: {},
				`alter session set container="MYDB";select privilege from dba_sys_privs where grantee='PROBERUSER'`: {
					`{"PRIVILEGE": "CREATE SESSION"}`,
				},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='SUPERUSER'`: {
					`{"GRANTED_ROLE": "DBA"}`,
				},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='PROBERUSER'`: {},
			},
			req: &pb.UsersChangedRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:       "superuser",
						Password:   "superpassword",
						Privileges: []string{"dba"},
					},
					{
						Name:       "scott",
						Password:   "tiger",
						Privileges: []string{"connect", "resource", "unlimited tablespace"},
					},
					{
						Name:       "proberuser",
						Password:   "proberpassword",
						Privileges: []string{"create session"},
					},
				},
			},
			wantChanged: true,
		},
		{
			name: "deleted users in spec",
			sqlToResp: map[string][]string{
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
			},
			req: &pb.UsersChangedRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:         "superuser",
						Password:     "superpassword",
						LastPassword: "superpassword",
						Privileges:   []string{"dba"},
					},
				},
			},
			wantChanged:         false,
			wantSuppressedUsers: []string{"PROBERUSER", "SCOTT"},
		},
		{
			name: "user added privs",
			sqlToResp: map[string][]string{
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
				`alter session set container="MYDB";select privilege from dba_sys_privs where grantee='PROBERUSER'`: {},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='SUPERUSER'`: {
					`{"GRANTED_ROLE": "DBA"}`,
				},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='SCOTT'`: {
					`{"GRANTED_ROLE": "RESOURCE"}`,
				},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='PROBERUSER'`: {},
			},
			req: &pb.UsersChangedRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:       "superuser",
						Password:   "superpassword",
						Privileges: []string{"dba"},
					},
					{
						Name:       "scott",
						Password:   "tiger",
						Privileges: []string{"connect", "resource", "unlimited tablespace"},
					},
					{
						Name:       "proberuser",
						Password:   "proberpassword",
						Privileges: []string{"create session"},
					},
				},
			},
			wantChanged: true,
		},
		{
			name: "user added and deleted privs",
			sqlToResp: map[string][]string{
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
				`alter session set container="MYDB";select privilege from dba_sys_privs where grantee='PROBERUSER'`: {},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='SUPERUSER'`: {
					`{"GRANTED_ROLE": "DBA"}`,
				},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='SCOTT'`: {
					`{"GRANTED_ROLE": "RESOURCE"}`,
				},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='PROBERUSER'`: {},
			},
			req: &pb.UsersChangedRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:       "superuser",
						Password:   "superpassword",
						Privileges: []string{"dba"},
					},
					{
						Name:       "scott",
						Password:   "tiger",
						Privileges: []string{"connect"},
					},
					{
						Name:       "proberuser",
						Password:   "proberpassword",
						Privileges: []string{"create session"},
					},
				},
			},
			wantChanged: true,
		},
		{
			name:      "User updated plaintext password",
			sqlToResp: sampleSqlToResp,
			req: &pb.UsersChangedRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:         "superuser",
						Password:     "superpassword1",
						LastPassword: "superpassword",
						Privileges:   []string{"dba"},
					},
					{
						Name:         "scott",
						Password:     "tiger1",
						LastPassword: "tiger",
						Privileges:   []string{"connect", "resource", "unlimited tablespace"},
					},
					{
						Name:         "proberuser",
						Password:     "proberpassword1",
						LastPassword: "proberpassword",
						Privileges:   []string{"create session"},
					},
				},
			},
			wantChanged: true,
		},
		{
			name:      "User updated gsm password",
			sqlToResp: sampleSqlToResp,
			req: &pb.UsersChangedRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:                 "superuser",
						Privileges:           []string{"dba"},
						PasswordGsmSecretRef: gsmRefWithChange,
					},
					{
						Name:                 "scott",
						Privileges:           []string{"connect", "resource", "unlimited tablespace"},
						PasswordGsmSecretRef: gsmRefWithChange,
					},
					{
						Name:                 "proberuser",
						Privileges:           []string{"create session"},
						PasswordGsmSecretRef: gsmRefWithChange,
					},
				},
			},
			wantChanged: true,
		},
		{
			name:      "User gsm password no update",
			sqlToResp: sampleSqlToResp,
			req: &pb.UsersChangedRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:                 "superuser",
						Privileges:           []string{"dba"},
						PasswordGsmSecretRef: gsmRefNoChange,
					},
					{
						Name:                 "scott",
						Privileges:           []string{"connect", "resource", "unlimited tablespace"},
						PasswordGsmSecretRef: gsmRefNoChange,
					},
					{
						Name:                 "proberuser",
						Privileges:           []string{"create session"},
						PasswordGsmSecretRef: gsmRefNoChange,
					},
				},
			},
			wantChanged: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			AccessSecretVersionFunc = func(ctx context.Context, name string) (string, error) {
				return "topsecuredsecret", nil
			}
			tc.sqlToResp[`alter session set container="MYDB";select role from dba_roles`] = []string{
				`{"ROLE": "CONNECT"}`,
				`{"ROLE": "RESOURCE"}`,
				`{"ROLE": "DBA"}`,
				`{"ROLE": "PDB_DBA"}`,
				`{"ROLE": "AUDIT_ADMIN"}`,
			}
			dbdServer.fakeRunSQLPlusFormatted = func(ctx context.Context, request *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				sql := strings.Join(request.GetCommands(), ";")
				resp, ok := tc.sqlToResp[sql]
				if !ok {
					return nil, fmt.Errorf("failed to find mock sql resp for %q", sql)
				}
				return &dbdpb.RunCMDResponse{
					Msg: resp,
				}, nil
			}
			configServer := &ConfigServer{}
			resp, err := configServer.UsersChanged(ctx, tc.req)
			if err != nil {
				t.Fatalf("UsersChanged(ctx, %v) failed: %v", tc.req, err)
			}
			if resp.Changed != tc.wantChanged {
				t.Errorf("UsersChanged got resp.Changed %v, want resp.Changed %v", resp.GetChanged(), tc.wantChanged)
			}
			var gotSuppressedUsers []string
			for _, s := range resp.Suppressed {
				gotSuppressedUsers = append(gotSuppressedUsers, s.UserName)
			}
			sort.Strings(gotSuppressedUsers)
			if diff := cmp.Diff(tc.wantSuppressedUsers, gotSuppressedUsers); diff != "" {
				t.Errorf("UsersChanged got unexpected resp.Suppressed for users: -want +got %v", diff)
			}
			for _, s := range resp.Suppressed {
				wantSQL := fmt.Sprintf(`alter session set container="MYDB"; DROP USER %q CASCADE;`, s.UserName)
				if s.Sql != wantSQL {
					t.Errorf("UsersChanged got unexpected resp.Suppressed SQL %q, want %q", s.Sql, wantSQL)
				}
			}
		})
	}
}

func TestConfigServerUpdateUsers(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	newDBDClientBak := newDBDClient
	newDBDClient = func(context.Context, *ConfigServer) (dbdpb.DatabaseDaemonClient, func() error, error) {
		return client, func() error { return nil }, nil
	}
	defer func() {
		newDBDClient = newDBDClientBak
		cleanup()
	}()
	ctx := context.Background()
	testCases := []struct {
		name      string
		sqlToResp map[string][]string
		req       *pb.UpdateUsersRequest
		wantSQLs  [][]string
	}{
		{
			name:      "no user changed",
			sqlToResp: sampleSqlToResp,
			req: &pb.UpdateUsersRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:         "superuser",
						Password:     "superpassword",
						LastPassword: "superpassword",
						Privileges:   []string{"dba"},
					},
					{
						Name:         "scott",
						Password:     "tiger",
						LastPassword: "tiger",
						Privileges:   []string{"connect", "resource", "unlimited tablespace"},
					},
					{
						Name:         "proberuser",
						Password:     "proberpassword",
						LastPassword: "proberpassword",
						Privileges:   []string{"create session"},
					},
				},
			},
		},
		{
			name: "added a user in spec",
			sqlToResp: map[string][]string{
				`alter session set container="MYDB";select username from dba_users where ORACLE_MAINTAINED='N' and INHERITED='NO'`: {
					`{"USERNAME": "GPDB_ADMIN"}`,
					`{"USERNAME": "SUPERUSER"}`,
					`{"USERNAME": "PROBERUSER"}`,
				},
				`alter session set container="MYDB";select privilege from dba_sys_privs where grantee='SUPERUSER'`: {},
				`alter session set container="MYDB";select privilege from dba_sys_privs where grantee='PROBERUSER'`: {
					`{"PRIVILEGE": "CREATE SESSION"}`,
				},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='SUPERUSER'`: {
					`{"GRANTED_ROLE": "DBA"}`,
				},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='PROBERUSER'`: {},
			},
			req: &pb.UpdateUsersRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:         "superuser",
						Password:     "superpassword",
						LastPassword: "superpassword",
						Privileges:   []string{"dba"},
					},
					{
						Name:         "scott",
						Password:     "tiger",
						LastPassword: "tiger",
						Privileges:   []string{"connect", "resource", "unlimited tablespace"},
					},
					{
						Name:         "proberuser",
						Password:     "proberpassword",
						LastPassword: "proberpassword",
						Privileges:   []string{"create session"},
					},
				},
			},
			wantSQLs: [][]string{
				{
					`alter session set container="MYDB"`,
					`create user "SCOTT" identified by "tiger"`,
					`grant CONNECT to "SCOTT"`,
					`grant RESOURCE to "SCOTT"`,
					`grant UNLIMITED TABLESPACE to "SCOTT"`,
				},
			},
		},
		{
			name: "deleted users in spec",
			sqlToResp: map[string][]string{
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
			},
			req: &pb.UpdateUsersRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:         "superuser",
						Password:     "superpassword",
						LastPassword: "superpassword",
						Privileges:   []string{"dba"},
					},
				},
			},
		},
		{
			name: "user added privs",
			sqlToResp: map[string][]string{
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
				`alter session set container="MYDB";select privilege from dba_sys_privs where grantee='PROBERUSER'`: {},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='SUPERUSER'`: {
					`{"GRANTED_ROLE": "DBA"}`,
				},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='SCOTT'`: {
					`{"GRANTED_ROLE": "RESOURCE"}`,
				},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='PROBERUSER'`: {},
			},
			req: &pb.UpdateUsersRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:         "superuser",
						Password:     "superpassword",
						LastPassword: "superpassword",
						Privileges:   []string{"dba"},
					},
					{
						Name:         "scott",
						Password:     "tiger",
						LastPassword: "tiger",
						Privileges:   []string{"connect", "resource", "unlimited tablespace"},
					},
					{
						Name:         "proberuser",
						Password:     "proberpassword",
						LastPassword: "proberpassword",
						Privileges:   []string{"create session"},
					},
				},
			},
			wantSQLs: [][]string{
				{`alter session set container="MYDB"`, `grant CREATE SESSION to "PROBERUSER"`},
				{`alter session set container="MYDB"`, `grant CONNECT to "SCOTT"`},
			},
		},
		{
			name: "user added and deleted privs",
			sqlToResp: map[string][]string{
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
				`alter session set container="MYDB";select privilege from dba_sys_privs where grantee='PROBERUSER'`: {},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='SUPERUSER'`: {
					`{"GRANTED_ROLE": "DBA"}`,
				},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='SCOTT'`: {
					`{"GRANTED_ROLE": "RESOURCE"}`,
				},
				`alter session set container="MYDB";select granted_role from dba_role_privs where grantee='PROBERUSER'`: {},
			},
			req: &pb.UpdateUsersRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:         "superuser",
						Password:     "superpassword",
						LastPassword: "superpassword",
						Privileges:   []string{"dba"},
					},
					{
						Name:         "scott",
						Password:     "tiger",
						LastPassword: "tiger",
						Privileges:   []string{"connect"},
					},
					{
						Name:         "proberuser",
						Password:     "proberpassword",
						LastPassword: "proberpassword",
						Privileges:   []string{"create session"},
					},
				},
			},
			wantSQLs: [][]string{
				{`alter session set container="MYDB"`, `grant CREATE SESSION to "PROBERUSER"`},
				{`alter session set container="MYDB"`, `grant CONNECT to "SCOTT"`},
				// revoke role first then privs
				{`alter session set container="MYDB"`, `revoke RESOURCE from "SCOTT"`},
				{`alter session set container="MYDB"`, `revoke UNLIMITED TABLESPACE from "SCOTT"`},
			},
		},
		{
			name:      "user updated plaintext password",
			sqlToResp: sampleSqlToResp,
			req: &pb.UpdateUsersRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:         "proberuser",
						Password:     "proberpassword1",
						LastPassword: "proberpassword",
						Privileges:   []string{"create session"},
					},
					{
						Name:         "scott",
						Password:     "tiger1",
						LastPassword: "tiger",
						Privileges:   []string{"connect", "resource", "unlimited tablespace"},
					},
					{
						Name:         "superuser",
						Password:     "superpassword1",
						LastPassword: "superpassword",
						Privileges:   []string{"dba"},
					},
				},
			},
			wantSQLs: [][]string{
				{`alter session set container="MYDB"`, `alter user "PROBERUSER" identified by "proberpassword1"`},
				{`alter session set container="MYDB"`, `alter user "SCOTT" identified by "tiger1"`},
				{`alter session set container="MYDB"`, `alter user "SUPERUSER" identified by "superpassword1"`},
			},
		},
		{
			name:      "user updated gsm password",
			sqlToResp: sampleSqlToResp,
			req: &pb.UpdateUsersRequest{
				PdbName: "MYDB",
				UserSpecs: []*pb.User{
					{
						Name:                 "proberuser",
						Privileges:           []string{"create session"},
						PasswordGsmSecretRef: gsmRefWithChange,
					},
					{
						Name:                 "scott",
						Privileges:           []string{"connect", "resource", "unlimited tablespace"},
						PasswordGsmSecretRef: gsmRefWithChange,
					},
					{
						Name:                 "superuser",
						Privileges:           []string{"dba"},
						PasswordGsmSecretRef: gsmRefWithChange,
					},
				},
			},
			wantSQLs: [][]string{
				{`alter session set container="MYDB"`, `alter user "PROBERUSER" identified by "topsecuredsecret"`},
				{`alter session set container="MYDB"`, `alter user "SCOTT" identified by "topsecuredsecret"`},
				{`alter session set container="MYDB"`, `alter user "SUPERUSER" identified by "topsecuredsecret"`},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			AccessSecretVersionFunc = func(ctx context.Context, name string) (string, error) {
				return "topsecuredsecret", nil
			}
			tc.sqlToResp[`alter session set container="MYDB";select role from dba_roles`] = []string{
				`{"ROLE": "CONNECT"}`,
				`{"ROLE": "RESOURCE"}`,
				`{"ROLE": "DBA"}`,
				`{"ROLE": "PDB_DBA"}`,
				`{"ROLE": "AUDIT_ADMIN"}`,
			}
			dbdServer.fakeRunSQLPlusFormatted = func(ctx context.Context, request *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				sql := strings.Join(request.GetCommands(), ";")
				resp, ok := tc.sqlToResp[sql]
				if !ok {
					return nil, fmt.Errorf("failed to find mock sql resp for %q", sql)
				}
				return &dbdpb.RunCMDResponse{
					Msg: resp,
				}, nil
			}
			var gotSQLs [][]string
			dbdServer.fakeRunSQLPlus = func(ctx context.Context, request *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				gotSQLs = append(gotSQLs, request.GetCommands())
				return &dbdpb.RunCMDResponse{}, nil
			}
			configServer := &ConfigServer{}
			_, err := configServer.UpdateUsers(ctx, tc.req)
			if err != nil {
				t.Fatalf("UpdateUsers(ctx, %v) failed: %v", tc.req, err)
			}
			if len(tc.wantSQLs) != len(gotSQLs) {
				t.Fatalf("UpdateUsers got %d SQLs cmd, want %d SQLs cmd", len(gotSQLs), len(tc.wantSQLs))
			}
			for idx := range gotSQLs {
				if diff := cmp.Diff(tc.wantSQLs[idx], gotSQLs[idx]); diff != "" {
					t.Errorf("UpdateUsers got unexpected SQLs: -want +got %v", diff)
				}
			}
		})
	}
}

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
