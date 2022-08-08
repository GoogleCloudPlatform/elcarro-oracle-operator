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
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/standbyhelpers"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/util/task"
)

const (
	// Example banner: "Oracle Database 12c Enterprise Edition Release 12.2.0.1.0 - 64bit Production"
	checkDBVersionSQL            = "select banner from v$version where banner like 'Oracle%'"
	checkForceLoggingSQL         = "select force_logging from v$database"
	checkLogModeSQL              = "select log_mode from v$database"
	checkDBNameSQL               = "select value from v$parameter where name='db_name'"
	checkDBParamsSQL             = "select value from v$parameter where name in ('dg_broker_start', 'remote_login_passwordfile') order by name"
	failedToConnectMsg           = "Failed to connect to primary database server (host: %s, port: %d, service: %s) due to %v"
	tnsConnectTimeoutMsg         = "ORA-12170: TNS:Connect timeout occurred. Check if host, port, and service name are correctly set, and primary database is configured to accept connection."
	tnsNoListenerMsg             = "ORA-12541: TNS:No listener occurred. Check if host, port, and service name are correctly set, and primary database is configured to accept connection."
	invalidLogonMsg              = "ORA-01017: invalid username/password; logon denied. Check if sys password is correct and remote password file is in either exclusive or shared mode."
	invalidServiceImageMsg       = "Service image is seeded. Requires unseeded service image."
	insufficientGcsPermissionMsg = "Insufficient permission. Requires granting service account object viewer permission to download the GCS object %s. Details: %v"
	invalidCDBNameMsg            = "The standby's CDB name %s is not consistent with the primary database name %s"
	invalidForceLoggingMsg       = "Primary database is not in FORCE_LOGGING mode"
	invalidLogModeMsg            = "Primary database is in %s mode, not ARCHIVELOG mode"
	incompatibleVersionMsg       = "Replication from primary database server (%s) to the standby (%s) is not supported."
	invalidDataGuardBrokerMsg    = "Primary database's data guard broker is not enabled."
	unsupportedDBPwdFileModeMsg  = "Primary database password file mode is %s. Requires EXCLUSIVE or SHARED mode for sync."
)

var supportedDBVersionToKeyWord = map[string]string{
	"12.2": "12c Enterprise",
	"19.3": "19c Enterprise",
}

type verifyStandbySettingsTask struct {
	tasks           *task.Tasks
	primary         *Primary
	standby         *Standby
	dbdClient       dbdpb.DatabaseDaemonClient
	passwordGcsPath string
	backupGcsPath   string
	settingErrs     []*standbyhelpers.StandbySettingErr
}

// verifyGcsPermission verifies dbdaemon can access password/backup file on GCS bucket.
func (task *verifyStandbySettingsTask) verifyGcsPermission(ctx context.Context) error {
	if _, err := task.dbdClient.DownloadDirectoryFromGCS(ctx, &dbdpb.DownloadDirectoryFromGCSRequest{GcsPath: task.passwordGcsPath, AccessPermissionCheck: true}); err != nil {
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INSUFFICIENT_PRIVILEGE, fmt.Sprintf(insufficientGcsPermissionMsg, task.passwordGcsPath, err))
	}

	if task.backupGcsPath != "" {
		if _, err := task.dbdClient.DownloadDirectoryFromGCS(ctx, &dbdpb.DownloadDirectoryFromGCSRequest{GcsPath: task.backupGcsPath, AccessPermissionCheck: true}); err != nil {
			addResultAsErr(task, standbyhelpers.StandbySettingErr_INSUFFICIENT_PRIVILEGE, fmt.Sprintf(insufficientGcsPermissionMsg, task.backupGcsPath, err))
		}
	}
	return nil
}

// verifyStandbyCDBName verifies standby's CDB Name matches primary's CDB Name.
func (task *verifyStandbySettingsTask) verifyStandbyCDBName(ctx context.Context) error {
	name, err := fetchAndParseSingleColumnMultiRowQueries(ctx, task.primary, task.dbdClient, checkDBNameSQL)
	if err != nil {
		internalErr := fmt.Errorf("failed to get database name: %v", err)
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INTERNAL_ERROR, internalErr.Error())
		return internalErr
	}
	if len(name) != 1 {
		internalErr := fmt.Errorf("got unexpected response for database name query: %v", name)
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INTERNAL_ERROR, internalErr.Error())
		return internalErr
	}
	if !strings.EqualFold(name[0], task.standby.CDBName) {
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INVALID_CDB_NAME,
			fmt.Sprintf(invalidCDBNameMsg, task.standby.CDBName, name[0]))
	}
	return nil
}

// verifyPrimaryLogSettings verifies whether the primary is in force logging and archive log mode.
func (task *verifyStandbySettingsTask) verifyPrimaryLogSettings(ctx context.Context) error {
	forceLogging, err := fetchAndParseSingleColumnMultiRowQueries(ctx, task.primary, task.dbdClient, checkForceLoggingSQL)
	if err != nil {
		internalErr := fmt.Errorf("failed to query force logging: %v", err)
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INTERNAL_ERROR, internalErr.Error())
		return internalErr
	}
	if len(forceLogging) != 1 {
		return fmt.Errorf("got unexpected response for force logging query: %v", forceLogging)
	}
	if !strings.EqualFold(forceLogging[0], "YES") {
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INVALID_LOGGING_SETUP, invalidForceLoggingMsg)
	}

	logMode, err := fetchAndParseSingleColumnMultiRowQueries(ctx, task.primary, task.dbdClient, checkLogModeSQL)
	if err != nil {
		return fmt.Errorf("failed to query log mode: %v", err)
	}
	if len(logMode) != 1 {
		return fmt.Errorf("got unexpected response for log mode query: %v", logMode)
	}
	if !strings.EqualFold(logMode[0], "ARCHIVELOG") {
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INVALID_LOGGING_SETUP, fmt.Sprintf(invalidLogModeMsg, logMode[0]))
	}

	return nil
}

// verifyPrimaryDBParams verifies DB version is 12c, primary data guard broker is enabled and password file is in shared or exclusive mode.
func (task *verifyStandbySettingsTask) verifyPrimaryDBParams(ctx context.Context) error {
	//check DB version
	version, err := fetchAndParseSingleColumnMultiRowQueries(ctx, task.primary, task.dbdClient, checkDBVersionSQL)
	if err != nil {
		//ORA-12170: TNS:Connect timeout occurred (this happens if the host doesn't exist or is not reachable)
		if strings.Contains(err.Error(), "ORA-12170:") {
			addResultAsErr(task, standbyhelpers.StandbySettingErr_CONNECTION_FAILURE,
				fmt.Sprintf(failedToConnectMsg, task.primary.Host, task.primary.Port, task.primary.Service, tnsConnectTimeoutMsg))
			return err
		}
		if strings.Contains(err.Error(), "ORA-12541:") {
			addResultAsErr(task, standbyhelpers.StandbySettingErr_CONNECTION_FAILURE,
				fmt.Sprintf(failedToConnectMsg, task.primary.Host, task.primary.Port, task.primary.Service, tnsNoListenerMsg))
			return err
		}
		// ORA-01017: invalid username/password; logon denied (this happens if the sys password is wrong or remote password file mode is NONE)
		if strings.Contains(err.Error(), "ORA-01017:") {
			addResultAsErr(task, standbyhelpers.StandbySettingErr_CONNECTION_FAILURE,
				fmt.Sprintf(failedToConnectMsg, task.primary.Host, task.primary.Port, task.primary.Service, invalidLogonMsg))
			return err
		}

		internalErr := fmt.Errorf("failed to query version: %v", err)
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INTERNAL_ERROR, internalErr.Error())
		return internalErr
	}

	if len(version) != 1 {
		internalErr := fmt.Errorf("got unexpected response for DB version query: %v", version)
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INTERNAL_ERROR, internalErr.Error())
		return internalErr
	}
	if k, ok := supportedDBVersionToKeyWord[task.standby.Version]; !ok || !strings.Contains(strings.ToUpper(version[0]), strings.ToUpper(k)) {
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INCOMPATIBLE_DATABASE_VERSION, fmt.Sprintf(incompatibleVersionMsg, version[0], task.standby.Version))
	}

	//Check primary data guard broker and password file mode.
	dbParams, err := fetchAndParseSingleColumnMultiRowQueries(ctx, task.primary, task.dbdClient, checkDBParamsSQL)
	if err != nil {
		internalErr := fmt.Errorf("failed to query DB params: %v", err)
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INTERNAL_ERROR, internalErr.Error())
		return internalErr
	}
	if len(dbParams) != 2 {
		internalErr := fmt.Errorf("got unexpected response for DB params query: %v", dbParams)
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INTERNAL_ERROR, internalErr.Error())
		return internalErr
	}
	if strings.ToUpper(dbParams[0]) != "TRUE" {
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INVALID_DB_PARAM, fmt.Sprintf(invalidDataGuardBrokerMsg))
	}
	if strings.ToUpper(dbParams[1]) != "SHARED" && strings.ToUpper(dbParams[1]) != "EXCLUSIVE" {
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INVALID_DB_PARAM, fmt.Sprintf(unsupportedDBPwdFileModeMsg, dbParams[1]))
	}

	return nil
}

func (task *verifyStandbySettingsTask) verifyServiceImageUnseeded(ctx context.Context) error {
	serviceImageMetaData, err := task.dbdClient.FetchServiceImageMetaData(ctx, &dbdpb.FetchServiceImageMetaDataRequest{})
	if err != nil {
		internalErr := fmt.Errorf("failed to fetch image metadata: %v", err)
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INTERNAL_ERROR, internalErr.Error())
		return internalErr
	}

	if serviceImageMetaData.CdbName != "" {
		addResultAsErr(task, standbyhelpers.StandbySettingErr_INVALID_SERVICE_IMAGE, fmt.Sprintf(invalidServiceImageMsg))
	}
	return nil
}

// newVerifyStandbySettingsTask does preflight check on standby settings.
func newVerifyStandbySettingsTask(ctx context.Context, primary *Primary, standby *Standby, passwordGcsPath, backupGcsPath string, dbdClient dbdpb.DatabaseDaemonClient) *verifyStandbySettingsTask {
	t := &verifyStandbySettingsTask{
		tasks:           task.NewTasks(ctx, "verifyStandbySettings"),
		dbdClient:       dbdClient,
		primary:         primary,
		standby:         standby,
		passwordGcsPath: passwordGcsPath,
		backupGcsPath:   backupGcsPath,
	}

	t.tasks.AddTask("VerifyServiceImageUnseeded", t.verifyServiceImageUnseeded)
	t.tasks.AddTask("verifyGcsPermission", t.verifyGcsPermission)
	t.tasks.AddTask("verifyPrimaryDBParams", t.verifyPrimaryDBParams)
	t.tasks.AddTask("verifyStandbyCDBName", t.verifyStandbyCDBName)
	t.tasks.AddTask("verifyPrimaryLogSettings", t.verifyPrimaryLogSettings)

	return t
}

func addResultAsErr(task *verifyStandbySettingsTask, t standbyhelpers.StandbySettingErrType, detail string) {
	task.settingErrs = append(task.settingErrs, &standbyhelpers.StandbySettingErr{Type: t, Detail: detail})
}
