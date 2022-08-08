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
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/standbyhelpers"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/util/task"
	"github.com/golang/protobuf/proto"
	"github.com/google/go-cmp/cmp"
)

const wantDSN = "sys/syspwd@123.123.123.123:6021/GCLOUD.gke AS SYSDBA"
const passwordGcsPath = "gs//verification-test/orapwGCLOUD"

func TestVerifyServiceImageUnseeded(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	task := &verifyStandbySettingsTask{
		dbdClient: client,
	}

	dbdServer.fakeFetchServiceImageMetaData = func(ctx context.Context, in *dbdpb.FetchServiceImageMetaDataRequest) (*dbdpb.FetchServiceImageMetaDataResponse, error) {
		return &dbdpb.FetchServiceImageMetaDataResponse{}, nil
	}

	err := task.verifyServiceImageUnseeded(ctx)
	if err != nil {
		t.Fatalf("Verifying service image failed: %v", err)
	}
	if task.settingErrs != nil && len(task.settingErrs) > 0 {
		t.Error("Verifying service image returned setting errors: \n")
		for index, e := range task.settingErrs {
			t.Errorf("Error %v: Type: %v, Detail: %v\n", index, e.Type, e.Detail)
		}
	}
}

func TestVerifyServiceImageUnseededErrors(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	tests := []struct {
		name                 string
		serviceCDBName       string
		fetchServiceImageErr error
		wantError            bool
		wantResp             *standbyhelpers.StandbySettingErr
	}{
		{
			name:                 "Verification failed due to internal error",
			fetchServiceImageErr: errors.New("configagent/FetchServiceImageMetaData failed"),
			wantError:            true,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_INTERNAL_ERROR,
				Detail: "failed to fetch image metadata: rpc error: code = Unknown desc = configagent/FetchServiceImageMetaData failed",
			},
		},
		{
			name:                 "Verification failed due to seeded service image",
			serviceCDBName:       "name",
			fetchServiceImageErr: nil,
			wantError:            false,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_INVALID_SERVICE_IMAGE,
				Detail: "Service image is seeded. Requires unseeded service image.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbdServer.fakeFetchServiceImageMetaData = func(ctx context.Context, in *dbdpb.FetchServiceImageMetaDataRequest) (*dbdpb.FetchServiceImageMetaDataResponse, error) {
				return &dbdpb.FetchServiceImageMetaDataResponse{CdbName: tt.serviceCDBName}, tt.fetchServiceImageErr
			}

			task := &verifyStandbySettingsTask{
				dbdClient: client,
			}

			err := task.verifyServiceImageUnseeded(ctx)
			if err == nil && tt.wantError {
				t.Errorf("Verifying service image returned no internal err, want err")
			}

			if task.settingErrs == nil {
				t.Fatal("Verifying service image succeeded, want setting error")
			}

			if len(task.settingErrs) > 1 {
				t.Fatalf("Verifying service image returned %v errors, want one setting error", len(task.settingErrs))
			}

			if diff := cmp.Diff(tt.wantResp, task.settingErrs[0], cmp.Comparer(proto.Equal)); diff != "" {
				t.Errorf("Verifying service image returned diff resp (-want +got):\n%v", diff)
			}
		})

	}

}

func TestVerifyGcsPermission(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	dbdServer.fakeDownloadDirectoryFromGCS = func(ctx context.Context, req *dbdpb.DownloadDirectoryFromGCSRequest) (*dbdpb.DownloadDirectoryFromGCSResponse, error) {
		return &dbdpb.DownloadDirectoryFromGCSResponse{}, nil
	}
	task := &verifyStandbySettingsTask{
		dbdClient:       client,
		passwordGcsPath: passwordGcsPath,
	}

	err := task.verifyGcsPermission(ctx)
	if err != nil {
		t.Fatalf("Verifying GCS permission failed: %v", err)
	}
	if task.settingErrs != nil && len(task.settingErrs) > 0 {
		t.Error("Verifying GCS permission returned setting errors as follows: \n")
		for index, e := range task.settingErrs {
			t.Errorf("Error %v: Type: %v, Detail: %v\n", index, e.Type, e.Detail)
		}
	}

}

func TestVerifyGcsPermissionError(t *testing.T) {
	dbdServer := &fakeServer{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	dbdServer.fakeDownloadDirectoryFromGCS = func(ctx context.Context, req *dbdpb.DownloadDirectoryFromGCSRequest) (*dbdpb.DownloadDirectoryFromGCSResponse, error) {
		return nil, errors.New("caller does not have storage.objects.get access to the GCS object")
	}
	task := &verifyStandbySettingsTask{
		dbdClient:       client,
		passwordGcsPath: passwordGcsPath,
	}

	wantResp := &standbyhelpers.StandbySettingErr{
		Type: standbyhelpers.StandbySettingErr_INSUFFICIENT_PRIVILEGE,
		Detail: "Insufficient permission. Requires granting service account object viewer permission to download the GCS object gs//verification-test/orapwGCLOUD. " +
			"Details: rpc error: code = Unknown desc = caller does not have storage.objects.get access to the GCS object",
	}

	err := task.verifyGcsPermission(ctx)
	if err != nil {
		t.Fatalf("Verifying GCS permission failed: %v", err)
	}

	if task.settingErrs == nil {
		t.Fatal("Verifying GCS permission succeeded, want setting error")
	}

	if task.settingErrs != nil && len(task.settingErrs) > 1 {
		t.Fatalf("Verifying GCS permission returned %v errors, want one setting error", len(task.settingErrs))
	}

	if diff := cmp.Diff(wantResp, task.settingErrs[0], cmp.Comparer(proto.Equal)); diff != "" {
		t.Errorf("Verifying GCS permission returned diff resp (-want +got):\n%v", diff)
	}

}

func TestVerifyPrimaryDBParams(t *testing.T) {
	dbdServer := &fakeServer{}
	secretAccessor := &fakeSecretAccessor{}
	secretAccessor.fakeGet = func(ctx context.Context) (string, error) {
		return "syspwd", nil
	}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()
	primary := &Primary{
		Host:             "123.123.123.123",
		Port:             6021,
		Service:          "GCLOUD.gke",
		User:             "sys",
		PasswordAccessor: secretAccessor,
	}

	tests := []struct {
		name        string
		queryToResp map[string][]string
		standby     *Standby
	}{
		{
			name: "12.2",
			queryToResp: map[string][]string{
				checkDBVersionSQL: {`{"BANNER": "12c Enterprise"}`},
				checkDBParamsSQL:  {`{"VALUE": "TRUE"}`, `{"VALUE": "EXCLUSIVE"}`},
			},
			standby: &Standby{
				Version: "12.2",
			},
		},
		{
			name: "19.3",
			queryToResp: map[string][]string{
				checkDBVersionSQL: {`{"BANNER": "19c Enterprise"}`},
				checkDBParamsSQL:  {`{"VALUE": "TRUE"}`, `{"VALUE": "EXCLUSIVE"}`},
			},
			standby: &Standby{
				Version: "19.3",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &verifyStandbySettingsTask{
				primary:   primary,
				dbdClient: client,
				standby:   tt.standby,
			}
			dbdServer.fakeRunSQLPlusFormatted = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				if !strings.EqualFold(req.GetDsn(), wantDSN) {
					return nil, errors.New("query failed")
				}
				val, ok := tt.queryToResp[req.GetCommands()[0]]
				if !ok {
					return nil, errors.New("query failed")
				}
				return &dbdpb.RunCMDResponse{Msg: val}, nil
			}
			err := task.verifyPrimaryDBParams(ctx)
			if err != nil {
				t.Fatalf("Verifying primary DB parameters failed: %v", err)
			}
			if len(task.settingErrs) > 0 {
				t.Error("Verifying primary DB parameters returned setting errors: \n")
				for index, e := range task.settingErrs {
					t.Errorf("Error %v: Type: %v, Detail: %v\n", index, e.Type, e.Detail)
				}
			}
		})
	}
}

func TestVerifyPrimaryDBParamsErrors(t *testing.T) {
	dbdServer := &fakeServer{}
	secretAccessor := &fakeSecretAccessor{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	tests := []struct {
		name        string
		version     string
		queryToResp map[string][]string
		sqlErr      error
		wantErr     bool
		wantResp    *standbyhelpers.StandbySettingErr
	}{
		{
			name:    "Verification failed due to primary database connection issue(connect timeout)\",",
			version: "12.2",
			queryToResp: map[string][]string{
				checkDBVersionSQL: {},
			},
			sqlErr:  errors.New("ORA-12170: TNS:Connect timeout occurred"),
			wantErr: true,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_CONNECTION_FAILURE,
				Detail: fmt.Sprintf(failedToConnectMsg, "123.123.123.123", 6021, "GCLOUD.gke", tnsConnectTimeoutMsg),
			},
		},
		{
			name:    "Verification failed due to primary database connection issue(no listener)",
			version: "12.2",
			queryToResp: map[string][]string{
				checkDBVersionSQL: {},
			},
			sqlErr:  errors.New("ORA-12541: TNS:No listener occurred"),
			wantErr: true,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_CONNECTION_FAILURE,
				Detail: fmt.Sprintf(failedToConnectMsg, "123.123.123.123", 6021, "GCLOUD.gke", tnsNoListenerMsg),
			},
		},
		{
			name:    "Verification failed due to primary database connection issue(invalid username/password)",
			version: "12.2",
			queryToResp: map[string][]string{
				checkDBVersionSQL: {},
			},
			sqlErr:  errors.New("ORA-01017: invalid username/password"),
			wantErr: true,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_CONNECTION_FAILURE,
				Detail: fmt.Sprintf(failedToConnectMsg, "123.123.123.123", 6021, "GCLOUD.gke", invalidLogonMsg),
			},
		},
		{
			name:    "Verification failed due to unknown database issue",
			version: "12.2",
			queryToResp: map[string][]string{
				checkDBVersionSQL: {},
			},
			sqlErr:  errors.New("internal error"),
			wantErr: true,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_INTERNAL_ERROR,
				Detail: "failed to query version: failed to run query [\"select banner from v$version where banner like 'Oracle%'\"]: rpc error: code = Unknown desc = internal error",
			},
		},
		{
			name:    "Verification failed due to incompatible DB version issue",
			version: "12.2",
			queryToResp: map[string][]string{
				checkDBVersionSQL: {`{"BANNER": "19c Enterprise"}`},
				checkDBParamsSQL:  {`{"VALUE": "TRUE"}`, `{"VALUE": "EXCLUSIVE"}`},
			},
			sqlErr:  nil,
			wantErr: false,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_INCOMPATIBLE_DATABASE_VERSION,
				Detail: "Replication from primary database server (19c Enterprise) to the standby (12.2) is not supported.",
			},
		},
		{
			name:    "Verification failed due to unsupported DB version issue",
			version: "11.2",
			queryToResp: map[string][]string{
				checkDBVersionSQL: {`{"BANNER": "11c Enterprise"}`},
				checkDBParamsSQL:  {`{"VALUE": "TRUE"}`, `{"VALUE": "EXCLUSIVE"}`},
			},
			sqlErr:  nil,
			wantErr: false,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_INCOMPATIBLE_DATABASE_VERSION,
				Detail: "Replication from primary database server (11c Enterprise) to the standby (11.2) is not supported.",
			},
		},
		{
			name:    "Verification failed due to disabled data guard broker",
			version: "12.2",
			queryToResp: map[string][]string{
				checkDBVersionSQL: {`{"BANNER": "12c Enterprise"}`},
				checkDBParamsSQL:  {`{"VALUE": "FALSE"}`, `{"VALUE": "EXCLUSIVE"}`},
			},
			sqlErr:  nil,
			wantErr: false,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_INVALID_DB_PARAM,
				Detail: "Primary database's data guard broker is not enabled.",
			},
		},
		{
			name:    "Verification failed due to wrong password file mode",
			version: "12.2",
			queryToResp: map[string][]string{
				checkDBVersionSQL: {`{"BANNER": "12c Enterprise"}`},
				checkDBParamsSQL:  {`{"VALUE": "TRUE"}`, `{"VALUE": "NONE"}`},
			},
			sqlErr:  nil,
			wantErr: false,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_INVALID_DB_PARAM,
				Detail: "Primary database password file mode is NONE. Requires EXCLUSIVE or SHARED mode for sync.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbdServer.fakeRunSQLPlusFormatted = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				if !strings.EqualFold(req.GetDsn(), wantDSN) {
					return nil, errors.New("query failed")
				}
				val, ok := tt.queryToResp[req.GetCommands()[0]]
				if !ok {
					return nil, errors.New("query failed")
				}
				return &dbdpb.RunCMDResponse{Msg: val}, tt.sqlErr
			}

			secretAccessor.fakeGet = func(ctx context.Context) (string, error) {
				return "syspwd", nil
			}
			task := &verifyStandbySettingsTask{
				primary: &Primary{
					Host:             "123.123.123.123",
					Port:             6021,
					Service:          "GCLOUD.gke",
					User:             "sys",
					PasswordAccessor: secretAccessor,
				},
				standby: &Standby{
					Version: tt.version,
				},
				dbdClient: client,
			}
			err := task.verifyPrimaryDBParams(ctx)
			if err == nil && tt.wantErr {
				t.Error("Verifying primary DB parameters returned no internal err, want err")
			}
			if err != nil && !tt.wantErr {
				t.Errorf("Verifying primary DB parameters failed: %v", err)
			}

			if task.settingErrs == nil {
				t.Fatal("Verifying primary DB parameters succeeded, want error")
			}

			if len(task.settingErrs) > 1 {
				t.Fatalf("Verifying primary DB parameters returned %v errors, want one error", len(task.settingErrs))
			}

			if diff := cmp.Diff(tt.wantResp, task.settingErrs[0], cmp.Comparer(proto.Equal)); diff != "" {
				t.Errorf("Verifying primary DB parameters returned diff resp (-want +got):\n%v", diff)
			}

		})
	}

}

func TestVerifyStandbyCDBName(t *testing.T) {
	dbdServer := &fakeServer{}
	secretAccessor := &fakeSecretAccessor{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	task := &verifyStandbySettingsTask{
		primary: &Primary{
			Host:             "123.123.123.123",
			Port:             6021,
			Service:          "GCLOUD.gke",
			User:             "sys",
			PasswordAccessor: secretAccessor,
		},
		standby: &Standby{
			CDBName: "GCLOUD",
		},
		dbdClient: client,
	}
	queryToResp := map[string][]string{
		checkDBNameSQL: {`{"VALUE": "GCLOUD"}`},
	}
	dbdServer.fakeRunSQLPlusFormatted = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
		if !strings.EqualFold(req.GetDsn(), wantDSN) {
			return nil, errors.New("query failed")
		}
		val, ok := queryToResp[req.GetCommands()[0]]
		if !ok {
			return nil, errors.New("query failed")
		}
		return &dbdpb.RunCMDResponse{Msg: val}, nil
	}

	secretAccessor.fakeGet = func(ctx context.Context) (string, error) {
		return "syspwd", nil
	}

	err := task.verifyStandbyCDBName(ctx)
	if err != nil {
		t.Fatalf("Verifying standby CDB name failed: %v", err)
	}
	if task.settingErrs != nil && len(task.settingErrs) > 0 {
		t.Error("Verifying standby CDB name returned setting errors: \n")
		for index, e := range task.settingErrs {
			t.Errorf("Error %v: Type: %v, Detail: %v\n", index, e.Type, e.Detail)
		}
	}
}

func TestVerifyStandbyCDBNameErrors(t *testing.T) {
	dbdServer := &fakeServer{}
	secretAccessor := &fakeSecretAccessor{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	tests := []struct {
		name     string
		sqlErr   error
		wantErr  bool
		wantResp *standbyhelpers.StandbySettingErr
	}{
		{
			name:    "Verification failed due to internal error",
			sqlErr:  errors.New("internal error"),
			wantErr: true,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_INTERNAL_ERROR,
				Detail: "failed to get database name: failed to run query [\"select value from v$parameter where name='db_name'\"]: rpc error: code = Unknown desc = internal error",
			},
		},
		{
			name:    "Verification failed due to inconsistent CDB name between primary and standby",
			sqlErr:  nil,
			wantErr: false,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_INVALID_CDB_NAME,
				Detail: "The standby's CDB name GCLOUD is not consistent with the primary database name GCLO",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbdServer.fakeRunSQLPlusFormatted = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				if !strings.EqualFold(req.GetDsn(), wantDSN) {
					return nil, errors.New("query failed")
				}
				return &dbdpb.RunCMDResponse{Msg: []string{`{"VALUE": "GCLO"}`}}, tt.sqlErr
			}

			secretAccessor.fakeGet = func(ctx context.Context) (string, error) {
				return "syspwd", nil
			}

			task := &verifyStandbySettingsTask{
				primary: &Primary{
					Host:             "123.123.123.123",
					Port:             6021,
					Service:          "GCLOUD.gke",
					User:             "sys",
					PasswordAccessor: secretAccessor,
				},
				standby: &Standby{
					CDBName: "GCLOUD",
				},
				dbdClient: client,
			}

			err := task.verifyStandbyCDBName(ctx)
			if err == nil && tt.wantErr {
				t.Error("Verifying standby CDB name returned no internal err, want err")
			}
			if err != nil && !tt.wantErr {
				t.Errorf("Verifying standby CDB name failed: %v", err)
			}

			if task.settingErrs == nil {
				t.Fatal("Verifying standby CDB name succeeded, want setting error")
			}

			if len(task.settingErrs) > 1 {
				t.Fatalf("Verifying standby CDB name returned %v errors, want one setting error", len(task.settingErrs))
			}

			if diff := cmp.Diff(tt.wantResp, task.settingErrs[0], cmp.Comparer(proto.Equal)); diff != "" {
				t.Errorf("Verifying standby CDB name returned diff resp (-want +got):\n%v", diff)
			}
		})
	}

}

func TestVerifyPrimaryLogSettings(t *testing.T) {
	dbdServer := &fakeServer{}
	secretAccessor := &fakeSecretAccessor{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	task := &verifyStandbySettingsTask{
		primary: &Primary{
			Host:             "123.123.123.123",
			Port:             6021,
			Service:          "GCLOUD.gke",
			User:             "sys",
			PasswordAccessor: secretAccessor,
		},
		dbdClient: client,
	}
	queryToResp := map[string][]string{
		checkForceLoggingSQL: {`{"VALUE": "YES"}`},
		checkLogModeSQL:      {`{"VALUE": "ARCHIVELOG"}`},
	}

	dbdServer.fakeRunSQLPlusFormatted = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
		if !strings.EqualFold(req.GetDsn(), wantDSN) {
			return nil, errors.New("query failed")
		}
		val, ok := queryToResp[req.GetCommands()[0]]
		if !ok {
			return nil, errors.New("query failed")
		}
		return &dbdpb.RunCMDResponse{Msg: val}, nil
	}

	secretAccessor.fakeGet = func(ctx context.Context) (string, error) {
		return "syspwd", nil
	}

	err := task.verifyPrimaryLogSettings(ctx)
	if err != nil {
		t.Fatalf("Verifying primary DB log settings failed: %v", err)
	}
	if task.settingErrs != nil && len(task.settingErrs) > 0 {
		t.Error("Verifying primary DB log settings returned setting errors: \n")
		for index, e := range task.settingErrs {
			t.Errorf("Error %v: Type: %v, Detail: %v\n", index, e.Type, e.Detail)
		}
	}
}

func TestVerifyPrimaryLogSettingsErrors(t *testing.T) {
	dbdServer := &fakeServer{}
	secretAccessor := &fakeSecretAccessor{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	tests := []struct {
		name        string
		queryToResp map[string][]string
		sqlErr      error
		wantErr     bool
		wantResp    *standbyhelpers.StandbySettingErr
	}{
		{
			name: "Verification failed due to internal error",
			queryToResp: map[string][]string{
				checkForceLoggingSQL: {},
			},
			sqlErr:  errors.New("internal error"),
			wantErr: true,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_INTERNAL_ERROR,
				Detail: "failed to query force logging: failed to run query [\"select force_logging from v$database\"]: rpc error: code = Unknown desc = internal error",
			},
		},
		{
			name: "Verification failed due to primary database is not in force logging mode",
			queryToResp: map[string][]string{
				checkForceLoggingSQL: {`{"VALUE": "NO"}`},
				checkLogModeSQL:      {`{"VALUE": "ARCHIVELOG"}`},
			},
			sqlErr:  nil,
			wantErr: false,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_INVALID_LOGGING_SETUP,
				Detail: "Primary database is not in FORCE_LOGGING mode",
			}},
		{
			name: "Verification failed due to the archiving mode of primary database is not ARCHIVELOG",
			queryToResp: map[string][]string{
				checkForceLoggingSQL: {`{"VALUE": "YES"}`},
				checkLogModeSQL:      {`{"VALUE": "NOARCHIVELOG"}`},
			},
			sqlErr:  nil,
			wantErr: false,
			wantResp: &standbyhelpers.StandbySettingErr{
				Type:   standbyhelpers.StandbySettingErr_INVALID_LOGGING_SETUP,
				Detail: "Primary database is in NOARCHIVELOG mode, not ARCHIVELOG mode",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbdServer.fakeRunSQLPlusFormatted = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				if !strings.EqualFold(req.GetDsn(), wantDSN) {
					return nil, errors.New("query failed")
				}
				val, ok := tt.queryToResp[req.GetCommands()[0]]
				if !ok {
					return nil, errors.New("query failed")
				}
				return &dbdpb.RunCMDResponse{Msg: val}, tt.sqlErr
			}

			secretAccessor.fakeGet = func(ctx context.Context) (string, error) {
				return "syspwd", nil
			}

			task := &verifyStandbySettingsTask{
				primary: &Primary{
					Host:             "123.123.123.123",
					Port:             6021,
					Service:          "GCLOUD.gke",
					User:             "sys",
					PasswordAccessor: secretAccessor,
				},
				dbdClient: client,
			}

			err := task.verifyPrimaryLogSettings(ctx)
			if err == nil && tt.wantErr {
				t.Error("Verifying primary DB log settings returned no internal err, want err")
			}

			if err != nil && !tt.wantErr {
				t.Errorf("Verifying primary DB log settings failed: %v", err)
			}

			if task.settingErrs == nil {
				t.Fatal("Verifying primary DB log settings succeeded, want setting error")
			}

			if len(task.settingErrs) > 1 {
				t.Fatalf("Verifying primary DB log settings returned %v errors, want one setting error", len(task.settingErrs))
			}

			if diff := cmp.Diff(tt.wantResp, task.settingErrs[0], cmp.Comparer(proto.Equal)); diff != "" {
				t.Errorf("Verifying primary DB log settings returned diff resp (-want +got):\n%v", diff)
			}
		})
	}

}

func TestVerifyStandbySettings(t *testing.T) {
	dbdServer := &fakeServer{}
	secretAccessor := &fakeSecretAccessor{}
	client, cleanup := newFakeDatabaseDaemonClient(t, dbdServer)
	defer cleanup()
	ctx := context.Background()

	primary := &Primary{
		Host:             "123.123.123.123",
		Port:             6021,
		Service:          "GCLOUD.gke",
		User:             "sys",
		PasswordAccessor: secretAccessor,
	}

	standby := &Standby{
		CDBName: "GCLOUD",
		Version: "12.2",
	}

	tests := []struct {
		name        string
		queryToResp map[string][]string
	}{
		{
			name: "success with all good standby setting",
			queryToResp: map[string][]string{
				checkDBVersionSQL:    {`{"BANNER": "12c Enterprise"}`},
				checkDBParamsSQL:     {`{"VALUE": "TRUE"}`, `{"VALUE": "EXCLUSIVE"}`},
				checkDBNameSQL:       {`{"VALUE": "GCLOUD"}`},
				checkForceLoggingSQL: {`{"VALUE": "YES"}`},
				checkLogModeSQL:      {`{"VALUE": "ARCHIVELOG"}`},
			},
		},
		{
			name: "success with all good standby settings (case insensitive)",
			queryToResp: map[string][]string{
				checkDBVersionSQL:    {`{"BANNER": "12c enterprise"}`},
				checkDBParamsSQL:     {`{"VALUE": "true"}`, `{"VALUE": "exclusive"}`},
				checkDBNameSQL:       {`{"VALUE": "GCLOUD"}`},
				checkForceLoggingSQL: {`{"VALUE": "yes"}`},
				checkLogModeSQL:      {`{"VALUE": "archivelog"}`},
			},
		},
		{
			name: "success with all good standby settings (shared password file)",
			queryToResp: map[string][]string{
				checkDBVersionSQL:    {`{"BANNER": "12c Enterprise"}`},
				checkDBParamsSQL:     {`{"VALUE": "TRUE"}`, `{"VALUE": "SHARED"}`},
				checkDBNameSQL:       {`{"VALUE": "GCLOUD"}`},
				checkForceLoggingSQL: {`{"VALUE": "YES"}`},
				checkLogModeSQL:      {`{"VALUE": "ARCHIVELOG"}`},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbdServer.fakeFetchServiceImageMetaData = func(ctx context.Context, in *dbdpb.FetchServiceImageMetaDataRequest) (*dbdpb.FetchServiceImageMetaDataResponse, error) {
				return &dbdpb.FetchServiceImageMetaDataResponse{}, nil
			}

			dbdServer.fakeDownloadDirectoryFromGCS = func(ctx context.Context, req *dbdpb.DownloadDirectoryFromGCSRequest) (*dbdpb.DownloadDirectoryFromGCSResponse, error) {
				return &dbdpb.DownloadDirectoryFromGCSResponse{}, nil
			}

			dbdServer.fakeRunSQLPlusFormatted = func(_ context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
				if !strings.EqualFold(req.GetDsn(), wantDSN) {
					return nil, errors.New("query failed")
				}
				val, ok := tt.queryToResp[req.GetCommands()[0]]
				if !ok {
					return nil, errors.New("query failed")
				}
				return &dbdpb.RunCMDResponse{Msg: val}, nil
			}

			secretAccessor.fakeGet = func(ctx context.Context) (string, error) {
				return "syspwd", nil
			}

			verificationTask := newVerifyStandbySettingsTask(ctx, primary, standby, passwordGcsPath, "", client)
			err := task.Do(ctx, verificationTask.tasks)
			if err != nil {
				t.Fatalf("Verify standby setting failed: %v", err)
			}
			if verificationTask.settingErrs != nil && len(verificationTask.settingErrs) > 0 {
				t.Error("Verify standby setting returned different response: \n")
				for index, e := range verificationTask.settingErrs {
					t.Errorf("Error %v: Type: %v, Detail: %v\n", index, e.Type, e.Detail)
				}
			}

		})
	}
}
