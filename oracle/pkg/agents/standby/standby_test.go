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
	"testing"

	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/google/go-cmp/cmp"
)

func TestDgConfigExists(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()
	dg := newDgConfig(
		client,
		func(ctx context.Context) (string, error) {
			return "/", nil
		},
	)
	testCases := []struct {
		name string
		resp *dbdpb.RunDataGuardResponse
		err  error
		want bool
	}{
		{
			name: "config exists",
			resp: &dbdpb.RunDataGuardResponse{
				Output: []string{
					`Connected to "GCLOUD_uscentral1a"

Configuration - operator_managed

  Protection Mode: MaxPerformance
  Members:
  gcloud_uscentral1a - Primary database

Fast-Start Failover: DISABLED

Configuration Status:
DISABLED`,
				},
			},
			err:  nil,
			want: true,
		},
		{
			name: "config does not exist",
			resp: nil,
			err:  errors.New("err: rpc error: code = Unknown desc = RunDataGuard failed, script: \"show configuration\"\nFailed with: Connected to \"GCLOUD_uscentral1a\"\nORA-16532: Oracle Data Guard broker configuration does not exist\n \nConfiguration details cannot be determined by DGMGRL\n \nErr: exit status 1"),
			want: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dbdServer.fakeRunDataGuard = func(context.Context, *dbdpb.RunDataGuardRequest) (*dbdpb.RunDataGuardResponse, error) {
				return tc.resp, tc.err
			}
			got := dg.exists(ctx)
			if got != tc.want {
				t.Errorf("dgConfig.exists got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDgConfigMembers(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()
	dg := newDgConfig(client, func(ctx context.Context) (string, error) {
		return "/", nil
	})
	testCases := []struct {
		name          string
		resp          *dbdpb.RunDataGuardResponse
		wantConfig    string
		wantPrimary   string
		wantPStandbys []string
		wantLStandbys []string
	}{
		{
			name: "only primary",
			resp: &dbdpb.RunDataGuardResponse{
				Output: []string{
					`Connected to "GCLOUD_uscentral1a"

Configuration - operator_managed

  Protection Mode: MaxPerformance
  Members:
  gcloud_uscentral1a - Primary database

Fast-Start Failover: DISABLED

Configuration Status:
DISABLED
`,
				},
			},
			wantConfig:  "operator_managed",
			wantPrimary: "gcloud_uscentral1a",
		},
		{
			name: "primary and standby",
			resp: &dbdpb.RunDataGuardResponse{
				Output: []string{
					`Connected to "GCLOUD_uscentral1a"

Configuration - operator_managed

  Protection Mode: MaxPerformance
  Members:
  gcloud_uscentral1a - Primary database
    gcloud_mydb1     - Physical standby database 

Fast-Start Failover: DISABLED

Configuration Status:
SUCCESS   (status updated 17 seconds ago)
`,
				},
			},
			wantConfig:    "operator_managed",
			wantPrimary:   "gcloud_uscentral1a",
			wantPStandbys: []string{"gcloud_mydb1"},
		},
		{
			name: "primary and standbys",
			resp: &dbdpb.RunDataGuardResponse{
				Output: []string{
					`Connected to "GCLOUD_uscentral1a"

Configuration - operator_managed

  Protection Mode: MaxPerformance
  Members:
  gcloud_uscentral1a - Primary database
    gcloud_mydb1     - Physical standby database
    gcloud_mydb        - Physical standby database
    gcloud_mydb2     - Logical standby database
    gcloud_mydb3        - Logical standby database

Fast-Start Failover: DISABLED

Configuration Status:
SUCCESS   (status updated 17 seconds ago)
`,
				},
			},
			wantConfig:    "operator_managed",
			wantPrimary:   "gcloud_uscentral1a",
			wantPStandbys: []string{"gcloud_mydb1", "gcloud_mydb"},
			wantLStandbys: []string{"gcloud_mydb2", "gcloud_mydb3"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dbdServer.fakeRunDataGuard = func(context.Context, *dbdpb.RunDataGuardRequest) (*dbdpb.RunDataGuardResponse, error) {
				return tc.resp, nil
			}
			got, err := dg.members(ctx)
			if err != nil {
				t.Fatalf("primaryUniqueName(ctx) failed: %v", err)
			}
			if got.configuration != tc.wantConfig {
				t.Errorf("dgConfig.members() got configuration %v, want configuration %v", got.configuration, tc.wantConfig)
			}
			if got.primary != tc.wantPrimary {
				t.Errorf("dgConfig.members() got primay %v, want primary %v", got.primary, tc.wantPrimary)
			}
			if diff := cmp.Diff(tc.wantPStandbys, got.physicalStandbys); diff != "" {
				t.Errorf("BootstrapTask.members() got unexpected physical standbys: -want +got %v", diff)
			}
			if diff := cmp.Diff(tc.wantLStandbys, got.logicalStandbys); diff != "" {
				t.Errorf("BootstrapTask.members() got unexpected logical standbys: -want +got %v", diff)
			}
		})
	}
}

func TestDgConfigConnectIdentifier(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()
	dg := newDgConfig(client, func(ctx context.Context) (string, error) {
		return "/", nil
	})
	testCases := []struct {
		name string
		resp *dbdpb.RunDataGuardResponse
		want string
	}{
		{
			name: "found connect identifier",
			resp: &dbdpb.RunDataGuardResponse{
				Output: []string{
					`DGConnectIdentifier = '35.239.195.134:6021/GCLOUD.gke'`,
				},
			},
			want: "35.239.195.134:6021/GCLOUD.gke",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dbdServer.fakeRunDataGuard = func(context.Context, *dbdpb.RunDataGuardRequest) (*dbdpb.RunDataGuardResponse, error) {
				return tc.resp, nil
			}
			got, err := dg.connectIdentifier(ctx, "Primary")
			if err != nil {
				t.Fatalf("connectIdentifier(ctx) failed: %v", err)
			}
			if got != tc.want {
				t.Errorf("dgConfig.connectIdentifier got %v, want %v", got, tc.want)
			}
		})
	}
}
