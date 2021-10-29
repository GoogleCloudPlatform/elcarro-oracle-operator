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
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	_ "github.com/godror/godror" // Register database/sql driver
	"k8s.io/klog/v2"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/security"
)

// Max number of retries for db startup.
const startupRetries = 5

// BootstrapTask defines a task can be invoked to bootstrap an Oracle DB.
type BootstrapTask struct {
	db          oracleDB
	uid         uint32
	gid         uint32
	subTasks    []task
	osUtil      osUtil
	dbdClient   dbdpb.DatabaseDaemonClient
	cdbRenaming bool
	isSeeded    bool
}

// GetName returns task name.
func (task *BootstrapTask) GetName() string {
	return "Bootstrap"
}

// Call triggers bootstrap process for an Oracle DB.
func (task *BootstrapTask) Call(ctx context.Context) error {
	return doSubTasks(ctx, task.GetName(), task.subTasks)
}

func (task *BootstrapTask) initUIDGID(ctx context.Context) error {
	uid, gid, err := oracleUser(task.osUtil)
	if err != nil {
		return fmt.Errorf("failed to find uid gid: %v", err)
	}
	task.uid = uid
	task.gid = gid
	return nil
}

func (task *BootstrapTask) createDirs(ctx context.Context) error {
	dirs := []string{
		task.db.GetDataFilesDir(),
		task.db.GetConfigFilesDir(),
		task.db.GetFlashDir(),
		task.db.GetListenerDir(),
		task.db.GetAdumpDir(),
		task.db.GetCdumpDir(),
	}

	if task.db.IsCDB() {
		dirs = append(dirs, filepath.Join(task.db.GetDataFilesDir(), "pdbseed"))
	}
	if err := MakeDirs(ctx, dirs, task.uid, task.gid); err != nil {
		return fmt.Errorf("failed to create prerequisite directories: %v", err)
	}
	return nil
}

func (task *BootstrapTask) setSourceEnv(ctx context.Context) error {
	// Sets env to mount the starter DB for running nid.
	return os.Setenv("ORACLE_SID", task.db.GetSourceDatabaseName())
}

func (task *BootstrapTask) setEnv(ctx context.Context) error {
	// Sets env to mount the starter DB for running nid.
	return os.Setenv("ORACLE_SID", task.db.GetDatabaseName())
}

func (task *BootstrapTask) setParameters(ctx context.Context) error {

	klog.InfoS("nomounting database for setting parameters")
	retry := 0
	var err error
	for ; retry < startupRetries; retry++ {
		_, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
			Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
			DatabaseName: task.db.GetDatabaseName(),
			Option:       "nomount",
		})
		if err == nil {
			break
		}
		klog.InfoS("setParameters: startup nomount failed", "attempt", retry, "err", err)
	}

	if retry == startupRetries {
		return fmt.Errorf("setParameters: startup nomount failed: %v", err)
	}

	// We want to be running in nomount to set all spfile parameters.
	klog.InfoS("setting parameters in spfile")
	if err := task.setParametersHelper(ctx); err != nil {
		return fmt.Errorf("setParameters: set server parameters: %v", err)
	}
	// Since parameters are set in spfile, we do a bounce.
	klog.InfoS("bouncing database for setting parameters")
	if _, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
		DatabaseName: task.db.GetDatabaseName(),
	}); err != nil {
		return fmt.Errorf("setParameters: shutdown after setting parameters failed: %v", err)
	}

	// For seeded database, we start the CDB in nomount mode required for the subsequent task moveDatabase.
	// For unseeded database, the subsequent task is prepDatabase which has a step to start the CDB in normal mounted mode.
	if task.isSeeded {
		if _, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
			Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
			DatabaseName: task.db.GetDatabaseName(),
			Option:       "force_nomount",
		}); err != nil {
			return fmt.Errorf("setParameters: force startup nomount failed: %v", err)
		}
	}
	return nil
}

func (task *BootstrapTask) moveDatabase(ctx context.Context) error {
	dbf := []string{}
	for _, f := range task.db.GetDataFiles() {
		dbf = append(dbf, fmt.Sprintf("'%s'", filepath.Join(task.db.GetDataFilesDir(), f)))
	}
	multiline := strings.Join(dbf, ",\n")
	c := &controlfileInput{
		DatabaseName:       task.db.GetDatabaseName(),
		DataFilesDir:       task.db.GetDataFilesDir(),
		DataFilesMultiLine: multiline,
	}

	ctl, err := template.New(filepath.Base(ControlFileTemplateName)).ParseFiles(ControlFileTemplateName)
	if err != nil {
		return fmt.Errorf("moveDatabase: parsing %q failed: %v", ControlFileTemplateName, err)
	}

	ctlBuf := &bytes.Buffer{}
	if err := ctl.Execute(ctlBuf, c); err != nil {
		return fmt.Errorf("moveDatabase: executing %q failed: %v", ControlFileTemplateName, err)
	}

	if _, err := runSQLPlus(ctx, task.db.GetVersion(), task.db.GetDatabaseName(), []string{ctlBuf.String()}, false); err != nil {
		return fmt.Errorf("moveDatabase: controlfile creation failed: %v", err)
	}
	sqlResp, err := task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{"alter database open resetlogs"},
		Suppress: false,
	})
	if err != nil {
		return fmt.Errorf("moveDatabase: resetlogs failed: %v", err)
	}
	klog.InfoS("reset logs after database move", "output", sqlResp)
	if _, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
		DatabaseName: task.db.GetDatabaseName(),
		Option:       "immediate",
	}); err != nil {
		return fmt.Errorf("moveDatabase: shutdown failed: %v", err)
	}
	klog.InfoS("database shutdown after move")

	return nil
}

// setupUsers creates all users required for the DB at instance creation.
func (task *BootstrapTask) setupUsers(ctx context.Context) error {
	checkUserCmd := "select * from all_users where username='%s'"
	cmds := task.db.GetCreateUserCmds()
	if cmds == nil {
		klog.Errorf("error retrieving create user commands potentially caused by error generating temporary password.")
	}
	for _, cu := range cmds {
		resp, err := task.dbdClient.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{fmt.Sprintf(checkUserCmd, strings.ToUpper(cu.user))}})
		if err != nil {
			return fmt.Errorf("check user %s failed: %v", cu.user, err)
		}
		if len(resp.GetMsg()) > 0 {
			continue
		}
		if _, err := task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
			Commands: cu.cmds,
			Suppress: true,
		}); err != nil {
			return fmt.Errorf("creating user %s failed: %v", cu.user, err)
		}
		klog.InfoS("creating user done", "user", cu.user, "command", cu.cmds[1:])
	}
	return nil
}

// createDumpDirs creates the required adump and cdump dirs..
func (task *BootstrapTask) createDumpDirs(ctx context.Context) error {
	dumpDirs := []string{"adump", "cdump"}
	var toCreate []*dbdpb.CreateDirsRequest_DirInfo
	for _, dumpDir := range dumpDirs {
		toCreate = append(toCreate, &dbdpb.CreateDirsRequest_DirInfo{
			Path: fmt.Sprintf("%s/admin/%s/%s", consts.OracleBase, task.db.GetDatabaseName(), dumpDir),
			Perm: 760,
		})
	}
	if _, err := task.dbdClient.CreateDirs(ctx, &dbdpb.CreateDirsRequest{
		Dirs: toCreate,
	}); err != nil {
		return fmt.Errorf("configagent/createDumpDirs: error while creating the dump dirs: %v", err)
	}
	return nil
}

func (task *BootstrapTask) moveConfigFiles(ctx context.Context) error {
	for i := range task.db.GetConfigFiles() {
		sf := filepath.Join(task.db.GetSourceConfigFilesDir(), task.db.GetSourceConfigFiles()[i])
		tf := filepath.Join(task.db.GetConfigFilesDir(), task.db.GetConfigFiles()[i])
		if err := MoveFile(sf, tf); err != nil {
			return fmt.Errorf("moveConfigFiles: failed to move config file from %s to %s : %v", sf, tf, err)
		}
	}
	return nil
}

func (task *BootstrapTask) moveDataFiles(ctx context.Context) error {
	for _, f := range task.db.GetDataFiles() {
		if err := MoveFile(filepath.Join(task.db.GetSourceDataFilesDir(), f), filepath.Join(task.db.GetDataFilesDir(), f)); err != nil {
			return fmt.Errorf("moveDataFiles: failed to move data file from %s to %s : %v", filepath.Join(task.db.GetSourceDataFilesDir(), f), task.db.GetDataFilesDir(), err)
		}
	}
	return nil
}

// relinkConfigFiles creates softlinks under the Oracle standard paths from the
// persistent configuration in the PD.
func (task *BootstrapTask) relinkConfigFiles(ctx context.Context) error {
	for _, f := range task.db.GetConfigFiles() {
		destn := filepath.Join(task.db.GetSourceConfigFilesDir(), f)
		if _, err := os.Stat(destn); err == nil {
			if err := os.Remove(destn); err != nil {
				return fmt.Errorf("relinkConfigFiles: unable to delete existing file %s: %v", f, err)
			}
		}
		if err := os.Symlink(filepath.Join(task.db.GetConfigFilesDir(), f), filepath.Join(task.db.GetSourceConfigFilesDir(), f)); err != nil {
			return fmt.Errorf("relinkConfigFiles: symlink creation failed for %s to oracle directories: %v", f, err)
		}
	}
	return nil
}

func (task *BootstrapTask) runNID(ctx context.Context) error {
	if task.cdbRenaming {
		return nil
	}

	if _, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
		DatabaseName: task.db.GetDatabaseName(),
		Option:       "mount",
	}); err != nil {
		return fmt.Errorf("runNID: startup mount failed: %v", err)
	}

	klog.InfoS("runNID: startup mount returned no errors")
	// running NID to change the DBID not the name of the database.
	if _, err := task.dbdClient.NID(ctx, &dbdpb.NIDRequest{
		Sid:          task.db.GetDatabaseName(),
		DatabaseName: "",
	}); err != nil {
		return fmt.Errorf("nid cmd failed: %v", err)
	}

	klog.InfoS("runNID: nid executed successfully")
	return nil
}

func getLocalListener(listeners map[string]*consts.Listener) (string, error) {
	for name, l := range listeners {
		if l.Local {
			return name, nil
		}
	}
	return "", fmt.Errorf("no local listener defined")
}

func (task *BootstrapTask) setParametersHelper(ctx context.Context) error {
	localListener, err := getLocalListener(task.db.GetListeners())
	if err != nil {
		return fmt.Errorf("parameter validation failed: %v", err)
	}
	//The following section would override the system specific init parameters specified in the spec.
	parameters := []string{
		fmt.Sprintf("audit_file_dest='%s/app/oracle/admin/%s/adump'", task.db.GetMountPointAdmin(), task.db.GetDatabaseName()),
		"audit_trail='db'",
		fmt.Sprintf("control_files='%s/control01.ctl'", task.db.GetDataFilesDir()),
		"db_block_size=8192",
		fmt.Sprintf("db_domain='%s'", task.db.GetDBDomain()),
		fmt.Sprintf("db_name='%s'", task.db.GetDatabaseName()),
		fmt.Sprintf("db_unique_name='%s'", task.db.GetDatabaseUniqueName()),
		"db_recovery_file_dest_size=100G",
		fmt.Sprintf("db_recovery_file_dest='%s'", task.db.GetFlashDir()),
		fmt.Sprintf("diagnostic_dest='%s/app/oracle'", task.db.GetMountPointDiag()),
		fmt.Sprintf("dispatchers='(PROTOCOL=TCP) (SERVICE=%sXDB)'", task.db.GetDatabaseName()),
		fmt.Sprintf("enable_pluggable_database=%s", strings.ToUpper(strconv.FormatBool(task.db.IsCDB()))),
		"filesystemio_options=SETALL",
		fmt.Sprintf("local_listener='(DESCRIPTION=(ADDRESS=(PROTOCOL=ipc)(KEY=REGLSNR_%d)))'", task.db.GetListeners()[localListener].Port),
		"open_cursors=300",
		"processes=300",
		"remote_login_passwordfile='EXCLUSIVE'",
		"undo_tablespace='UNDOTBS1'",
		fmt.Sprintf("log_archive_dest_1='LOCATION=USE_DB_RECOVERY_FILE_DEST VALID_FOR=(ALL_LOGFILES,ALL_ROLES) DB_UNIQUE_NAME=%s'", task.db.GetDatabaseUniqueName()),
		"log_archive_dest_state_1=enable",
		"log_archive_format='%t_%s_%r.arch'",
		"standby_file_management=AUTO",
	}

	if task.db.IsCDB() {
		parameters = append(parameters, "common_user_prefix='gcsql$'")
	}

	if task.isSeeded && task.db.GetVersion() != consts.Oracle18c {
		/* We do not change the pga_aggregate_target and sga_target parameters for Oracle 18c XE because of limitations
		   Oracle places on memory allocation for the Express Edition. The parameter "compatible" comes preset with the
			 desired value for Oracle 18c XE */
		parameters = append(parameters, fmt.Sprintf("pga_aggregate_target=%dM", task.db.GetDatabaseParamPGATargetMB()))
		parameters = append(parameters, fmt.Sprintf("sga_target=%dM", task.db.GetDatabaseParamSGATargetMB()))
		parameters = append(parameters, fmt.Sprintf("compatible='%s.0'", task.db.GetVersion()))
	}

	if !task.isSeeded {
		// hack fix for new PDB listener
		parameters = append(parameters, fmt.Sprintf("local_listener='(DESCRIPTION=(ADDRESS=(PROTOCOL=ipc)(KEY=REGLSNR_%d)))'", consts.SecureListenerPort))
	}

	// We might want to send the whole batch over at once, but this way its
	// easier to see where it failed.
	for _, p := range parameters {
		// Most of these cannot be set in scope=memory, so we only use scope=spfile.
		stmt := fmt.Sprintf("alter system set %s scope=spfile", p)
		klog.InfoS("setParametersHelper: executing", "stmt", stmt)
		if _, err := task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
			Commands: []string{stmt},
			Suppress: false,
		}); err != nil {
			return fmt.Errorf("failed to set %q: %v", p, err)
		}
		klog.InfoS("setParametersHelper: stmt executed successfully")
	}
	return nil
}

func (task *BootstrapTask) prepDatabase(ctx context.Context) error {
	password, err := security.RandOraclePassword()
	if err != nil {
		return fmt.Errorf("error generating temporary password")
	}

	// Post NID, we need to start the database and resetlogs.
	if _, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
		DatabaseName: task.db.GetDatabaseName(),
		Option:       "mount",
	}); err != nil {
		return fmt.Errorf("prepDatabase: startup mount failed: %v", err)
	}
	klog.InfoS("prepDatabase: startup mount")
	// Enabling archivelog mode.
	commands := []string{"alter database archivelog"}
	// resetlogs is only required after seeded database is renamed using a NID operation
	if task.cdbRenaming || !task.isSeeded {
		commands = append(commands, "alter database open")
	} else {
		commands = append(commands, "alter database open resetlogs")
	}
	sqlResp, err := task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: commands,
		Suppress: false,
	})

	if err != nil {
		return fmt.Errorf("prepDatabase: enabling archive log and resetlogs open failed: %v", err)
	}
	klog.InfoS("prepDatabase: archive log mode and resetlogs open", "output", sqlResp)

	// /u02/app/oracle/oradata/<CDB name>/temp01.dbf is already part of database in unseeded use case, so it is skipped.
	if task.isSeeded {
		tempfile := []string{fmt.Sprintf("ALTER TABLESPACE TEMP ADD TEMPFILE '%s/temp01.dbf' SIZE 1G REUSE AUTOEXTEND ON", task.db.GetDataFilesDir())}
		sqlResp, err = task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
			Commands: tempfile,
			Suppress: false,
		})
		if err != nil {
			return fmt.Errorf("prepDatabase: adding tempfile failed: %v", err)
		}
		klog.InfoS("prepDatabase: add tempfile", "output", sqlResp)
	}
	sys := []string{
		ChangePasswordCmd("sys", password),
		ChangePasswordCmd("system", password)}
	sqlResp, err = task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: sys,
		Suppress: true,
	})
	if err != nil {
		return fmt.Errorf("prepDatabase: change sys & system password failed: %v", err)
	}
	klog.InfoS("prepDatabase: sys & system password change", "output", sqlResp)
	return nil
}

func (task *BootstrapTask) fixOratab(ctx context.Context) error {
	if err := replace(task.db.GetOratabFile(), task.db.GetSourceDatabaseName(), task.db.GetDatabaseName(), task.uid, task.gid); err != nil {
		return fmt.Errorf("oratab replacing dbname: %v", err)
	}
	if err := replace(task.db.GetOratabFile(), task.db.GetSourceDatabaseHost(), task.db.GetHostName(), task.uid, task.gid); err != nil {
		return fmt.Errorf("oratab replacing hostname: %v", err)
	}
	return nil
}

func (task *BootstrapTask) cleanup(ctx context.Context) error {
	if err := os.RemoveAll(task.db.GetSourceDataFilesDir()); err != nil {
		klog.ErrorS(err, "BootstrapTask: failed to cleanup source data directory")
	}
	return nil
}

func (task *BootstrapTask) initListeners(ctx context.Context) error {
	lType := "SECURE"
	_, err := task.dbdClient.CreateListener(ctx, &dbdpb.CreateListenerRequest{
		DatabaseName: task.db.GetDatabaseName(),
		Port:         task.db.GetListeners()[lType].Port,
		Protocol:     task.db.GetListeners()[lType].Protocol,
		OracleHome:   task.db.GetDatabaseHome(),
		DbDomain:     task.db.GetDBDomain(),
	})
	return err
}

// recreateFlashDir creates a flash dir if it does not exist.
func (task *BootstrapTask) recreateFlashDir(ctx context.Context) error {
	if _, err := os.Stat(task.db.GetFlashDir()); os.IsNotExist(err) {
		klog.InfoS("recreateFlashDir: recreating flash directory", "flashDir", task.db.GetFlashDir())

		if err := MakeDirs(ctx, []string{task.db.GetFlashDir()}, task.uid, task.gid); err != nil {
			return fmt.Errorf("recreateFlashDir: creating flash directory %q failed: %v", task.db.GetFlashDir(), err)
		}
	}
	return nil
}

func (task *BootstrapTask) startDB(ctx context.Context) error {
	backupMode := false
	if _, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
		DatabaseName: task.db.GetDatabaseName(),
		Option:       "open",
	}); err != nil {
		// StartupDatabase failed at this point. It failed because of ORA-10873, there is file in backup mode.
		if strings.Contains(err.Error(), "ORA-10873:") {
			backupMode = true
		} else {
			// The cdb startup failed because of non backup error.
			return fmt.Errorf("startDB: start db failed: %v", err)
		}
	}

	var sqls []string
	if backupMode {
		sqls = append(sqls, "alter database end backup", "alter database open")
	}
	if task.db.IsCDB() {
		sqls = append(sqls, "alter pluggable database all open")
	}

	if sqls != nil {
		if _, err := task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
			Commands: sqls,
			Suppress: false,
		}); err != nil {
			return fmt.Errorf("startDB: open db failed: %v", err)
		}
	}
	klog.InfoS("startDB: successfully open db")
	return nil
}

func (task *BootstrapTask) createPDBSeedTemp(ctx context.Context) error {
	sqlResp, err := task.dbdClient.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{"select name, con_id from v$tempfile where con_id in (select con_id from v$containers where name='PDB$SEED')"},
		Suppress: false,
	})

	if err != nil {
		return fmt.Errorf("createPDBSeedTemp: failed to query temp file for PDB$SEED: %v", err)
	}

	if len(sqlResp.GetMsg()) > 0 {
		return nil
	}

	// Ask dbdaemon to remove empty /u01/app/oracle/admin/<SOURCE_DB>/dpdump/ directory
	// in its own container as it might cause issues with Oracle 19x on FUSE filesystems.
	dpDumpDir := fmt.Sprintf("%s/admin/%s/dpdump/", os.Getenv("ORACLE_BASE"), task.db.GetSourceDatabaseName())
	if _, err := task.dbdClient.DeleteDir(ctx, &dbdpb.DeleteDirRequest{Path: dpDumpDir, Force: true}); err != nil {
		klog.ErrorS(err, "createPDBSeedTemp: unable to delete", dpDumpDir)
	}

	if _, err := task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{
			"alter session set container=PDB$SEED",
			"alter session set \"_oracle_script\"=TRUE",
			"alter pluggable database PDB$SEED close",
			"alter pluggable database PDB$SEED open read write",
			fmt.Sprintf("alter tablespace TEMP add tempfile '%s/temp01.dbf' size 500m reuse", filepath.Join(task.db.GetDataFilesDir(), "pdbseed")),
			"alter pluggable database PDB$SEED close",
			"alter pluggable database PDB$SEED open read only",
		},
		Suppress: false,
	}); err != nil {
		return fmt.Errorf("createPDBSeedTemp: failed to add temp file for PDB$SEED: %v", err)
	}
	return nil
}

var runSQLPlus = func(ctx context.Context, version, dbname string, sqls []string, suppress bool) ([]string, error) {

	// Required for local connections
	// (when no SID is specified on connect string)
	if err := os.Setenv("ORACLE_SID", dbname); err != nil {
		return nil, fmt.Errorf("failed to set env variable: %v", err)
	}

	if err := os.Setenv("TNS_ADMIN", fmt.Sprintf(consts.ListenerDir, consts.DataMount)); err != nil {
		return nil, fmt.Errorf("failed to set env variable: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("TNS_ADMIN"); err != nil {
			klog.Warningf("failed to unset env variable: %v", err)
		}
	}()

	// For connect string format refer to
	// https://github.com/godror/godror/blob/main/drv.go
	prelim := false
	db, err := sql.Open("godror", "oracle://?sysdba=1")     // "/ as sysdba"
	if pingErr := db.Ping(); err != nil || pingErr != nil { // Force a connection with Ping.
		// Connection pool opened but ping failed, close this pool.
		if err == nil {
			err = pingErr
			if err := db.Close(); err != nil {
				klog.Warningf("failed to close db connection: %v", err)
			}
		}

		// Try a preliminary connection for CREATE (S)PFILE only.
		// In this case we wont be able to get DBMS_OUTPUT.
		if !strings.HasPrefix(strings.ToLower(sqls[0]), "create spfile") &&
			!strings.HasPrefix(strings.ToLower(sqls[0]), "create pfile") {
			klog.Errorf("Failed to connect to oracle: %v", err)
			return nil, err
		}
		prelim = true
		db, err = sql.Open("godror", "oracle://?sysdba=1&prelim=1") // "/ as sysdba"
		if err != nil {
			klog.Errorf("Failed to connect to oracle: %v", err)
			return nil, err
		}
	}
	defer db.Close()

	// This will fail on prelim connections, so ignore errors in that case.
	if _, err := db.ExecContext(ctx, "BEGIN DBMS_OUTPUT.ENABLE(); END;"); err != nil && !prelim {
		klog.Errorf("Failed to enable dbms_output: %v", err)
		return nil, err
	}

	sqlForLogging := strings.Join(sqls, ";")
	if suppress {
		sqlForLogging = "suppressed"
	}
	klog.Infof("Executing SQL command: %q", sqlForLogging)

	output := []string{}
	for _, sql := range sqls {
		if _, err := db.ExecContext(ctx, sql); err != nil {
			klog.Errorf("Failed to execute: %q:\n%v", sql, err)
			return nil, err
		}
		out, err := dbmsOutputGetLines(ctx, db)
		if err != nil && !prelim {
			klog.Errorf("Failed to get DMBS_OUTPUT for %q:\n%v", sql, err)
			return nil, err
		}
		output = append(output, out...)
	}
	klog.Infof("output: %q", strings.Join(output, "\n"))
	return output, nil
}

func dbmsOutputGetLines(ctx context.Context, db *sql.DB) ([]string, error) {
	lines := make([]string, 0, 1024)
	status := 0
	// 0 is success, until it fails there may be more lines buffered.
	for status == 0 {
		var line string
		if _, err := db.ExecContext(ctx, "BEGIN DBMS_OUTPUT.GET_LINE(:line, :status); END;",
			sql.Named("line", sql.Out{Dest: &line}),
			sql.Named("status", sql.Out{Dest: &status, In: true})); err != nil {
			return nil, err
		}
		if status == 0 {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func (task *BootstrapTask) renameDatabase(ctx context.Context) error {

	if !task.cdbRenaming {
		return nil
	}

	// Prepare a SPFile.
	i := initFileInput{
		SourceDBName: task.db.GetSourceDatabaseName(),
		DestDBName:   task.db.GetDatabaseName(),
	}
	initOraFileContent, err := i.LoadInitOraTemplate(task.db.GetVersion())
	if err != nil {
		return err
	}

	initOraFileName := fmt.Sprintf("init%s.ora", task.db.GetDatabaseName())
	dbsDir := filepath.Join(task.db.GetDatabaseHome(), "dbs")

	lByte := []byte(initOraFileContent)
	if err := ioutil.WriteFile(filepath.Join(dbsDir, initOraFileName), lByte, 0600); err != nil {
		return err
	}
	klog.InfoS("renameDatabase: prepare init file succeeded")

	// Start the database.
	if _, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:         dbdpb.BounceDatabaseRequest_STARTUP,
		DatabaseName:      task.db.GetSourceDatabaseName(),
		Option:            "mount",
		AvoidConfigBackup: true,
	}); err != nil {
		return fmt.Errorf("renameDatabase: startup mount failed: %v", err)
	}
	klog.InfoS("renameDatabase: startup mount succeeded")

	// running NID to change the DBID not the name of the database.
	if _, err := task.dbdClient.NID(ctx, &dbdpb.NIDRequest{
		Sid:          task.db.GetSourceDatabaseName(),
		DatabaseName: task.db.GetDatabaseName(),
	}); err != nil {
		return fmt.Errorf("nid cmd failed: %v", err)
	}
	klog.InfoS("renameDatabase: nid command succeeded")

	if _, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:         dbdpb.BounceDatabaseRequest_STARTUP,
		DatabaseName:      task.db.GetDatabaseName(),
		Option:            "mount",
		AvoidConfigBackup: true,
	}); err != nil {
		return fmt.Errorf("renameDatabase: shutdown failed: %v", err)
	}
	klog.InfoS("renameDatabase: startup succeeded after nid command")

	sqlResp, err := runSQLPlus(ctx, task.db.GetVersion(), task.db.GetDatabaseName(), []string{"alter database open resetlogs"},
		false)
	if err != nil {
		return fmt.Errorf("renameDatabase: resetlogs failed: %v", err)
	}
	klog.InfoS("reset logs after database move succeeded", "output", sqlResp)

	if _, err := runSQLPlus(ctx, task.db.GetVersion(), task.db.GetDatabaseName(), []string{"CREATE SPFILE from PFILE"}, false); err != nil {
		return fmt.Errorf("renameDatabase: spfile generation succeeded: %v", err)
	}
	klog.InfoS("spfile generation succeeded")

	klog.InfoS("shutting database ")
	if _, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
		DatabaseName: task.db.GetDatabaseName(),
	}); err != nil {
		return fmt.Errorf("renameDatabase: shutdown failed: %v", err)
	}

	klog.InfoS("renameDatabase: executed successfully")
	return nil
}

// NewBootstrapDatabaseTask returns a Task which can be invoked to bootstrap a DB.
func NewBootstrapDatabaseTask(ctx context.Context, iscdb bool, isSeeded bool, cdbNameFromImage, cdbNameFromYaml, version, zone, host, DBDomain string, pgaMB, sgaMB uint64, provisioned bool, dbdClient dbdpb.DatabaseDaemonClient) (*BootstrapTask, error) {
	var db oracleDB
	if !iscdb {
		return nil, errors.New("only support CDB provisioning")
	}

	var cdbRenaming bool
	if !strings.EqualFold(cdbNameFromImage, cdbNameFromYaml) {
		klog.InfoS("NewBootstrapDatabaseTask", "cdbName FromImage", cdbNameFromImage, "cdbName FromYaml", cdbNameFromYaml)
		cdbRenaming = true
	}

	db = newOracleCDB(ctx, cdbNameFromImage, cdbNameFromYaml, version, zone, host, DBDomain, pgaMB, sgaMB)
	bootstrapTask := &BootstrapTask{
		db:          db,
		osUtil:      &OSUtilImpl{},
		dbdClient:   dbdClient,
		cdbRenaming: cdbRenaming,
		isSeeded:    isSeeded,
	}
	if provisioned {
		bootstrapTask.subTasks = []task{
			&simpleTask{name: "initUIDGID", callFun: bootstrapTask.initUIDGID},
			&simpleTask{name: "relinkConfigFiles", callFun: bootstrapTask.relinkConfigFiles},
			&simpleTask{name: "recreateFlashDir", callFun: bootstrapTask.recreateFlashDir},
			&simpleTask{name: "setEnv", callFun: bootstrapTask.setEnv},
			&simpleTask{name: "startDB", callFun: bootstrapTask.startDB},
			&simpleTask{name: "initListeners", callFun: bootstrapTask.initListeners},
		}
	} else {
		bootstrapTask.subTasks = []task{
			&simpleTask{name: "renameDatabase", callFun: bootstrapTask.renameDatabase},
			&simpleTask{name: "initUIDGID", callFun: bootstrapTask.initUIDGID},
			&simpleTask{name: "createDirs", callFun: bootstrapTask.createDirs},
			&simpleTask{name: "setSourceEnv", callFun: bootstrapTask.setSourceEnv},
			&simpleTask{name: "moveDataFiles", callFun: bootstrapTask.moveDataFiles},
			&simpleTask{name: "moveConfigFiles", callFun: bootstrapTask.moveConfigFiles},
			&simpleTask{name: "relinkConfigFiles", callFun: bootstrapTask.relinkConfigFiles},
			&simpleTask{name: "setParameters", callFun: bootstrapTask.setParameters},
			&simpleTask{name: "moveDatabase", callFun: bootstrapTask.moveDatabase},
			&simpleTask{name: "runNID", callFun: bootstrapTask.runNID},
			&simpleTask{name: "prepDatabase", callFun: bootstrapTask.prepDatabase},
			&simpleTask{name: "fixOratab", callFun: bootstrapTask.fixOratab},
			&simpleTask{name: "setupUsers", callFun: bootstrapTask.setupUsers},
			&simpleTask{name: "cleanup", callFun: bootstrapTask.cleanup},
			&simpleTask{name: "initListeners", callFun: bootstrapTask.initListeners},
		}
	}
	if iscdb {
		bootstrapTask.subTasks = append(bootstrapTask.subTasks, &simpleTask{name: "createPDBSeedTemp", callFun: bootstrapTask.createPDBSeedTemp})
	}

	return bootstrapTask, nil
}

// NewBootstrapDatabaseTaskForUnseeded returns a Task for bootstrapping a CDB created during instance creation.
func NewBootstrapDatabaseTaskForUnseeded(cdbName, dbUniqueName, dbDomain string, dbdClient dbdpb.DatabaseDaemonClient) *BootstrapTask {
	cdb := &oracleCDB{
		cdbName:    cdbName,
		uniqueName: dbUniqueName,
		DBDomain:   dbDomain,
	}
	bootstrapTask := &BootstrapTask{
		db:          cdb,
		osUtil:      &OSUtilImpl{},
		dbdClient:   dbdClient,
		cdbRenaming: false,
	}

	// The following are the tasks common for the seeded and unseeded workflow.
	// All the remaining tasks are all related to database relocation from u01 to u02 and u03
	bootstrapTask.subTasks = []task{
		&simpleTask{name: "setParameters", callFun: bootstrapTask.setParameters},
		&simpleTask{name: "prepDatabase", callFun: bootstrapTask.prepDatabase},
		&simpleTask{name: "setupUsers", callFun: bootstrapTask.setupUsers},
		&simpleTask{name: "createPDBSeedTemp", callFun: bootstrapTask.createPDBSeedTemp},
		&simpleTask{name: "createDumpDirs", callFun: bootstrapTask.createDumpDirs},
	}
	return bootstrapTask
}

// NewBootstrapDatabaseTaskForStandby returns a Task for bootstrapping a standby instance.
func NewBootstrapDatabaseTaskForStandby(cdbName, dbDomain string, dbdClient dbdpb.DatabaseDaemonClient) *BootstrapTask {
	cdb := &oracleCDB{
		cdbName: cdbName,
	}
	bootstrapTask := &BootstrapTask{
		db:          cdb,
		osUtil:      &OSUtilImpl{},
		dbdClient:   dbdClient,
		cdbRenaming: false,
	}

	bootstrapTask.subTasks = []task{
		&simpleTask{name: "setupUsers", callFun: bootstrapTask.setupUsers},
	}
	return bootstrapTask
}
