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

// Package dbdaemon implements a gRPC service for
// running privileged database ops, e.g. sqlplus, rman.
package dbdaemon

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/godror/godror" // Register database/sql driver
	"github.com/golang/protobuf/ptypes"
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"google.golang.org/api/iterator"
	lropb "google.golang.org/genproto/googleapis/longrunning"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"k8s.io/klog/v2"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common"
	sqlq "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common/sql"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/pitr"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/security"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/database/lib/lro"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/database/provision"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/util"
)

const (
	listenerDir = "/u02/app/oracle/oraconfig/network"
)

var (
	oraDataDir = "/u02/app/oracle/oradata"

	maxWalkFiles = 10000
)

// oracleDatabase defines the sql.DB APIs, which will be used in this package
type oracleDatabase interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	Ping() error
	Close() error
}

type dbdaemon interface {
	shutdownDatabase(context.Context, godror.ShutdownMode) error
	startupDatabase(context.Context, godror.StartupMode, string) error
	setDatabaseUpgradeMode(ctx context.Context) error
	openPDBs(ctx context.Context) error
	runSQL(context.Context, []string, bool, bool, oracleDatabase) ([]string, error)
	runQuery(context.Context, []string, oracleDatabase) ([]string, error)
}

// DB is a wrapper around database/sql.DB database handle.
// In unit tests it gets mocked with the FakeDB.
type DB struct {
}

// Server holds a database config.
type Server struct {
	*dbdpb.UnimplementedDatabaseDaemonServer
	hostName       string
	database       dbdaemon
	databaseSid    *syncState
	databaseHome   string
	pdbConnStr     string
	osUtil         osUtil
	dbdClient      dbdpb.DatabaseDaemonProxyClient
	dbdClientClose func() error
	lroServer      *lro.Server
	syncJobs       *syncJobs
	gcsUtil        util.GCSUtil
}

// Remove pdbConnStr from String(), as that may contain the pdb user/password
// Remove UnimplementedDatabaseDaemonServer field to improve logs for better readability
func (s Server) String() string {
	pdbConnStr := s.pdbConnStr
	if pdbConnStr != "" {
		pdbConnStr = "<REDACTED>"
	}
	return fmt.Sprintf("{hostName=%q, database=%+v, databaseSid=%+v, databaseHome=%q, pdbConnStr=%q}", s.hostName, s.database, s.databaseSid, s.databaseHome, pdbConnStr)
}

type syncState struct {
	sync.RWMutex
	val string
}

type syncJobs struct {
	// pdbLoadMutex is a mutex for operations running
	// under consts.PDBLoaderUser user, currently those are DataPump import/export.
	// pdbLoadMutex is used to ensure only one of such operations is running at a time.
	pdbLoadMutex sync.Mutex

	// Mutex used for maintenance operations (currently for patching)
	maintenanceMutex sync.RWMutex
}

// Call this function to get any buffered DMBS_OUTPUT.  sqlplus* calls this
// after every command issued. Typically any output you expect to see from
// sqlplus* will be returned via DBMS_OUTPUT.
func dbmsOutputGetLines(ctx context.Context, db oracleDatabase) ([]string, error) {
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

// shutdownDatabase performs a database shutdown in a requested <mode>.
// It always connects to the local database.
// Set ORACLE_HOME and ORACLE_SID in the env to control the target database.
// A caller may decide to ignore ORA-1034 and just log a warning
// if a database has already been down (or raise an error if appropriate)..
func (d *DB) shutdownDatabase(ctx context.Context, mode godror.ShutdownMode) error {
	// Consider allowing PRELIM mode connections for SHUTDOWN ABORT mode.
	// This is useful when the server has maxed out on connections.
	db, err := sql.Open("godror", "oracle://?sysdba=1")
	if err != nil {
		klog.ErrorS(err, "dbdaemon/shutdownDatabase: failed to connect to a database")
		return err
	}
	defer db.Close()

	oraDB, err := godror.DriverConn(ctx, db)
	if err != nil {
		return err
	}
	if err := oraDB.Shutdown(mode); err != nil {
		return err
	}
	// The shutdown process is over after the first Shutdown call in ABORT
	// mode.
	if mode == godror.ShutdownAbort {
		return err
	}

	_, err = db.Exec("alter database close normal")
	if err != nil && strings.Contains(err.Error(), "ORA-01507:") {
		klog.InfoS("dbdaemon/shutdownDatabase: database is already closed", "err", err)
		err = nil
	}
	if err != nil {
		return err
	}

	_, err = db.Exec("alter database dismount")
	if err != nil && strings.Contains(err.Error(), "ORA-01507:") {
		klog.InfoS("dbdaemon/shutdownDatabase: database is already dismounted", "err", err)
		err = nil
	}
	if err != nil {
		return err
	}

	return oraDB.Shutdown(godror.ShutdownFinal)
}

// startupDatabase performs a database startup in a requested mode.
// godror.StartupMode controls FORCE/RESTRICT options.
// databaseState string controls NOMOUNT/MOUNT/OPEN options.
// Setting a pfile to use on startup is currently unsupported.
// It always connects to the local database.
// Set ORACLE_HOME and ORACLE_SID in the env to control the target database.
func (d *DB) startupDatabase(ctx context.Context, mode godror.StartupMode, state string) error {
	// To startup a shutdown database, open a prelim connection.
	db, err := sql.Open("godror", "oracle://?sysdba=1&prelim=1")
	if err != nil {
		return err
	}
	defer db.Close()

	oraDB, err := godror.DriverConn(ctx, db)
	if err != nil {
		return err
	}
	if err := oraDB.Startup(mode); err != nil {
		return err
	}
	if strings.ToLower(state) == "nomount" {
		return nil
	}

	// To finish mounting/opening, open a normal connection.
	db2, err := sql.Open("godror", "oracle://?sysdba=1")
	if err != nil {
		return err
	}
	defer db2.Close()

	if _, err := db2.Exec("alter database mount"); err != nil {
		return err
	}
	if strings.ToLower(state) == "mount" {
		return nil
	}
	_, err = db2.Exec("alter database open")
	return err
}

// Turn a freshly started NOMOUNT database to a migrate mode
// Opens CDB in upgrade mode
// Opens all PDBs in upgrade mode
// Executes the following steps:
// SQL> alter database mount
// SQL> alter database open upgrade
// SQL> alter pluggable database all open upgrade
func (d *DB) setDatabaseUpgradeMode(ctx context.Context) error {
	db, err := sql.Open("godror", "oracle://?sysdba=1")
	if err != nil {
		return fmt.Errorf("dbdaemon/setDatabaseUpgradeMode failed to open DB connection: %w", err)
	}
	defer db.Close()

	// SQL> alter database mount -- this will turn CDB$ROOT, PDB$SEED and all PDBs into 'MOUNTED' state
	if _, err := db.Exec("alter database mount"); err != nil {
		return err
	}

	// SQL> alter database open upgrade -- this will turn CDB$ROOT, PDB$SEED into 'MIGRATE' state
	if _, err := db.Exec("alter database open upgrade"); err != nil {
		return err
	}

	// SQL> alter pluggable database all open upgrade
	if _, err := db.Exec("alter pluggable database all open upgrade"); err != nil {
		return err
	}

	// At this point CDB$ROOT, PDB$SEED and all PDBs should be in 'MIGRATE' state
	// Check that all container states = 'MIGRATE'

	rows, err := db.Query("SELECT name,open_mode FROM v$containers")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var name, openMode string
		if err := rows.Scan(&name, &openMode); err != nil {
			return err
		}
		klog.InfoS("dbdaemon/setDatabaseUpgradeMode CONTAINER MODE: ", name, openMode)
		if openMode != "MIGRATE" {
			return fmt.Errorf("failed to turn container %v into MIGRATE mode: %w", name, err)
		}
	}
	return nil
}

// Open all PDBs
func (d *DB) openPDBs(ctx context.Context) error {
	db, err := sql.Open("godror", "oracle://?sysdba=1")
	if err != nil {
		return fmt.Errorf("dbdaemon/openPDBs: failed to open DB connection: %w", err)
	}
	defer db.Close()

	// SQL> alter pluggable database all open
	if _, err := db.Exec("alter pluggable database all open"); err != nil {
		return err
	}
	return nil
}

// CreatePasswordFile is a Database Daemon method to create password file.
func (s *Server) CreatePasswordFile(ctx context.Context, req *dbdpb.CreatePasswordFileRequest) (*dbdpb.CreatePasswordFileResponse, error) {
	if req.GetDatabaseName() == "" {
		return nil, fmt.Errorf("missing database name for req: %v", req)
	}
	if req.GetSysPassword() == "" {
		return nil, fmt.Errorf("missing password for req: %v", req)
	}

	if req.GetDir() != "" {
		if err := os.MkdirAll(req.GetDir(), 0750); err != nil {
			return nil, fmt.Errorf("failed to create dir: %v", req.GetDir())
		}
	}

	passwordFile := fmt.Sprintf("%s/orapw%s", req.GetDir(), req.GetDatabaseName())

	params := []string{fmt.Sprintf("file=%s", passwordFile)}
	params = append(params, fmt.Sprintf("password=%s", req.SysPassword))
	params = append(params, fmt.Sprintf("ignorecase=n"))

	if err := os.Remove(passwordFile); err != nil {
		klog.Warningf("failed to remove %v: %v", passwordFile, err)
	}

	if err := s.osUtil.runCommand(orapwd(s.databaseHome), params); err != nil {
		return nil, fmt.Errorf("orapwd cmd failed: %v", err)
	}
	return &dbdpb.CreatePasswordFileResponse{}, nil
}

// CreateFile creates file based on request.
func (s *Server) CreateFile(ctx context.Context, req *dbdpb.CreateFileRequest) (*dbdpb.CreateFileResponse, error) {
	klog.InfoS("dbdaemon/CreateFile: ", "req", req)

	if err := s.osUtil.createFile(req.Path, strings.NewReader(req.Content)); err != nil {
		return nil, fmt.Errorf("dbdaemon/CreateFile: create failed: %v", err)
	}
	return &dbdpb.CreateFileResponse{}, nil
}

// SetListenerRegistration is a Database Daemon method to create a static listener registration.
func (s *Server) SetListenerRegistration(ctx context.Context, req *dbdpb.SetListenerRegistrationRequest) (*dbdpb.BounceListenerResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

// physicalRestore runs
//  1. RMAN restore command
//  2. SQL to get latest SCN
//  3. RMAN recover command, created by applying SCN value
//
// to the recover statement template passed as a parameter.
func (s *Server) physicalRestore(ctx context.Context, req *dbdpb.PhysicalRestoreRequest) (*empty.Empty, error) {
	errorPrefix := "dbdaemon/physicalRestore: "

	if _, err := s.RunRMAN(ctx, &dbdpb.RunRMANRequest{Scripts: []string{req.GetRestoreStatement()}}); err != nil {
		return nil, fmt.Errorf(errorPrefix+"failed to restore a database: %v", err)
	}

	if req.GetPitrRestoreInput() != nil {
		if err := s.stageAndCatalog(ctx, req); err != nil {
			return nil, fmt.Errorf(errorPrefix+"failed to stage or catalog redo logs: %v", err)
		}
	}

	var recoverStmt string
	if req.GetPitrRestoreInput() == nil {
		scn, err := s.latestSCN(ctx, req.GetLatestRecoverableScnQuery())
		if err != nil {
			return nil, fmt.Errorf(errorPrefix+"failed to get the latest SCN: %v", err)
		}
		recoverStmt = fmt.Sprintf(req.GetRecoverStatementTemplate(), fmt.Sprintf("scn %d", scn))
	} else if req.GetPitrRestoreInput().GetEndTime() != nil {
		recoverStmt = fmt.Sprintf(req.GetRecoverStatementTemplate(), fmt.Sprintf(`time "to_date('%s','DD-MON-YYYY HH24:MI:SS')"`, req.GetPitrRestoreInput().GetEndTime().AsTime().Format("02-Jan-2006 15:04:05")))
	} else if req.GetPitrRestoreInput().GetEndScn() != 0 {
		recoverStmt = fmt.Sprintf(req.GetRecoverStatementTemplate(), fmt.Sprintf("scn %d", req.GetPitrRestoreInput().GetEndScn()))
	}

	if recoverStmt == "" {
		return nil, fmt.Errorf(errorPrefix+"failed to build recover statement from req %+v", req)
	}

	klog.InfoS(errorPrefix+"final recovery request", "recoverStmt", recoverStmt)

	recoverReq := &dbdpb.RunRMANRequest{Scripts: []string{recoverStmt}}
	if _, err := s.RunRMAN(ctx, recoverReq); err != nil {
		return nil, fmt.Errorf(errorPrefix+"failed to recover a database: %v", err)
	}

	// always remove rman staging dir for restore from GCS
	if err := os.RemoveAll(consts.RMANStagingDir); err != nil {
		klog.Warningf("physicalRestore: can't cleanup staging dir from local disk.")
	}
	return &empty.Empty{}, nil
}

func (s *Server) latestSCN(ctx context.Context, query string) (int64, error) {
	scnResp, err := s.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{query}})
	if err != nil || len(scnResp.GetMsg()) < 1 {
		return 0, fmt.Errorf("failed to query archive log SCNs, results: %v, err: %v", scnResp, err)
	}

	row := make(map[string]string)
	if err := json.Unmarshal([]byte(scnResp.GetMsg()[0]), &row); err != nil {
		return 0, err
	}

	scn, ok := row["SCN"]
	if !ok {
		return 0, fmt.Errorf("failed to find column SCN in the archive log query")
	}

	scnNum, err := strconv.ParseInt(scn, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse the SCN query (%v) to find int64: %v", scn, err)
	}
	return scnNum, nil
}

func (s *Server) stageAndCatalog(ctx context.Context, req *dbdpb.PhysicalRestoreRequest) error {
	var include func(entry pitr.LogMetadataEntry) bool
	input := req.GetPitrRestoreInput()
	if input.GetEndTime() != nil {
		if input.GetStartTime() == nil {
			return fmt.Errorf("failed to find recover end time in req %+v", req)
		}
		include = func(entry pitr.LogMetadataEntry) bool {
			return !(entry.NextTime.Before(input.GetStartTime().AsTime()) || entry.FirstTime.After(input.GetEndTime().AsTime()))
		}
	} else if input.GetEndScn() != 0 {
		include = func(entry pitr.LogMetadataEntry) bool {
			entryS, err := strconv.ParseInt(entry.FirstChange, 10, 64)
			if err != nil {
				klog.ErrorS(err, "failed to parse replicated log FirstChange scn in metadata %+v", entry)
				return true
			}

			entryE, err := strconv.ParseInt(entry.NextChange, 10, 64)
			if err != nil {
				klog.ErrorS(err, "failed to parse replicated log NextChange scn in metadata %+v", entry)
				return true
			}

			return !(entryE < input.GetStartScn() || entryS > input.GetEndScn())
		}
	}

	if include == nil {
		return fmt.Errorf("either start SCN or timestamp must be specified in req %+v", req)
	}

	dir := filepath.Join(consts.RMANStagingDir, "pitr")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("failed to create redo logs staging dir: %v", err)
	}
	if err := pitr.StageLogs(ctx, dir, include, input.GetLogGcsPath()); err != nil {
		return fmt.Errorf("failed to stage redo logs: %v", err)
	}
	if _, err := s.RunRMAN(ctx, &dbdpb.RunRMANRequest{
		Scripts: []string{
			fmt.Sprintf("catalog start with '%s' noprompt;", dir),
		},
	}); err != nil {
		return fmt.Errorf("failed to catalog redo logs: %v", err)
	}
	return nil
}

// PhysicalRestoreAsync turns physicalRestore into an async call.
func (s *Server) PhysicalRestoreAsync(ctx context.Context, req *dbdpb.PhysicalRestoreAsyncRequest) (*lropb.Operation, error) {
	job, err := lro.CreateAndRunLROJobWithID(ctx, req.GetLroInput().GetOperationId(), "PhysicalRestore", s.lroServer,
		func(ctx context.Context) (proto.Message, error) {
			msg, err := s.physicalRestore(ctx, req.SyncRequest)
			klog.InfoS("Physical restore completed", "err", err)
			return msg, err
		})

	if err != nil {
		klog.ErrorS(err, "dbdaemon/PhysicalRestoreAsync failed to create an LRO job", "request", req)
		return nil, err
	}

	return &lropb.Operation{Name: job.ID(), Done: false}, nil
}

func filterParamsForMetadata(in []string) []string {
	// These parameters are incompatible with sqlfile, so we must remove
	// them for our metadata dump.
	restricted := []string{
		"TABLE_EXISTS_ACTION",
	}

	var filtered []string
	for _, i := range in {
		safe := true
		for _, r := range restricted {
			if strings.Split(i, "=")[0] == r {
				safe = false
				break
			}
		}
		if safe {
			filtered = append(filtered, i)
		}
	}

	return filtered
}

// dataPumpImport runs impdp Oracle tool against existing PDB which
// imports data from a data pump .dmp file.
func (s *Server) dataPumpImport(ctx context.Context, req *dbdpb.DataPumpImportRequest) (*dbdpb.DataPumpImportResponse, error) {
	s.syncJobs.pdbLoadMutex.Lock()
	defer s.syncJobs.pdbLoadMutex.Unlock()

	importFilename := "import.dmp"
	importMetaFile := importFilename + ".meta"
	logFilename := "import.log"

	pdbPath := fmt.Sprintf(consts.PDBPathPrefix, consts.DataMount, s.databaseSid.val, strings.ToUpper(req.PdbName))
	dumpDir := filepath.Join(pdbPath, consts.DpdumpDir.Linux)
	klog.InfoS("dbdaemon/dataPumpImport", "dumpDir", dumpDir)

	dmpReader, err := s.gcsUtil.Download(ctx, req.GcsPath)
	if err != nil {
		return nil, fmt.Errorf("dbdaemon/dataPumpImport: initiating GCS download failed: %v", err)
	}
	defer dmpReader.Close()

	importFileFullPath := filepath.Join(dumpDir, importFilename)
	if err := s.osUtil.createFile(importFileFullPath, dmpReader); err != nil {
		return nil, fmt.Errorf("dbdaemon/dataPumpImport: download from GCS failed: %v", err)
	}
	klog.Infof("dbdaemon/dataPumpImport: downloaded import dmp file from %s to %s", req.GcsPath, importFileFullPath)
	defer func() {
		if err := s.osUtil.removeFile(importFileFullPath); err != nil {
			klog.Warning(fmt.Sprintf("dbdaemon/dataPumpImport: failed to remove import dmp file after import: %v", err))
		}
	}()

	impdpTarget, err := security.SetupUserPwConnStringOnServer(ctx, s, consts.PDBLoaderUser, req.PdbName, req.DbDomain)
	if err != nil {
		return nil, fmt.Errorf("dbdaemon/dataPumpImport: failed to alter user %s", consts.PDBLoaderUser)
	}

	// Tested with:
	// impdp \"/ as sysdba\" sqlfile=test_meta_tables.sql dumpfile=prod_export_cmms02072022.dmp directory=REFRESH_DUMP_DIR NOLOGFILE=YES FULL=Y INCLUDE=TABLESPACE INCLUDE=TABLESPACE_QUOTA INCLUDE=USER PARALLEL=4
	// We dont dump tables because this can be slow O(minutes), but it would be more precise if needed it can be added.
	tsCheckParams := []string{impdpTarget}
	tsCheckParams = append(tsCheckParams, filterParamsForMetadata(req.CommandParams)...)
	tsCheckParams = append(tsCheckParams, fmt.Sprintf("directory=%s", consts.DpdumpDir.Oracle))
	tsCheckParams = append(tsCheckParams, "dumpfile="+importFilename)
	tsCheckParams = append(tsCheckParams, "sqlfile="+importMetaFile)
	tsCheckParams = append(tsCheckParams, "nologfile=YES")
	tsCheckParams = append(tsCheckParams, "include=TABLESPACE")
	tsCheckParams = append(tsCheckParams, "include=USER")
	tsCheckParams = append(tsCheckParams, "include=TABLESPACE_QUOTA")

	if err := s.runCommand(impdp(s.databaseHome), tsCheckParams); err != nil {
		// On error code 5 (EX_SUCC_ERR), process completed reached the
		// end but data in the DMP might have been skipped (foreign
		// schemas, already imported tables, even failed schema imports
		// because the DMP didn't include CREATE USER statements.)
		if !s.osUtil.isReturnCodeEqual(err, 5) {
			return nil, fmt.Errorf("data pump import metadata gathering failed, err = %v", err)
		}

		klog.Warning("dbdaemon/dataPumpImport: metadata gathering completed with EX_SUCC_ERR")
	}

	metaFullPath := filepath.Join(dumpDir, importMetaFile)
	s.createTablespacesFromSqlfile(ctx, metaFullPath, req.PdbName)

	params := []string{impdpTarget}
	params = append(params, req.CommandParams...)
	params = append(params, fmt.Sprintf("directory=%s", consts.DpdumpDir.Oracle))
	params = append(params, "dumpfile="+importFilename)
	params = append(params, "logfile="+logFilename)

	if err := s.runCommand(impdp(s.databaseHome), params); err != nil {
		// On error code 5 (EX_SUCC_ERR), process completed reached the
		// end but data in the DMP might have been skipped (foreign
		// schemas, already imported tables, even failed schema imports
		// because the DMP didn't include CREATE USER statements.)
		if !s.osUtil.isReturnCodeEqual(err, 5) {
			return nil, fmt.Errorf("data pump import failed, err = %v", err)
		}

		klog.Warning("dbdaemon/dataPumpImport: completed with EX_SUCC_ERR")
	}

	if len(req.GcsLogPath) > 0 {
		logFullPath := filepath.Join(dumpDir, logFilename)

		if err := s.gcsUtil.UploadFile(ctx, req.GcsLogPath, logFullPath, contentTypePlainText); err != nil {
			return nil, fmt.Errorf("dbdaemon/dataPumpImport: import completed successfully, failed to upload import log to GCS: %v", err)
		}

		klog.Infof("dbdaemon/dataPumpImport: uploaded import log to %s", req.GcsLogPath)
	}

	return &dbdpb.DataPumpImportResponse{}, nil
}

var tsRegexp = regexp.MustCompile("(DEFAULT|CREATE|UNDO|TEMPORARY) TABLESPACE \"(.*?)\"|QUOTA UNLIMITED ON \"(.*?)\"")

// createTablespacesFromSqlfile scans the sqlfile looking for tablespace
// references. It gathers these references and then ensures the tablespaces are
// created as BIGFILE for regular tablespaces or as AUTOEXTEND single datafile
// for temporary tablespaces.
func (s *Server) createTablespacesFromSqlfile(ctx context.Context, metaFullPath, PDBName string) {
	f, err := os.Open(metaFullPath)
	if err != nil {
		klog.Warningf("Not creating tablespaces for import. Failed to open %q: %v", metaFullPath, err)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			klog.Warningf("failed to close import metafile %v: %v", f, err)
		}
	}()

	// Gather a list of tablespaces required by this sqlfile.
	ts := map[string]bool{}
	tsTemp := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		matches := tsRegexp.FindStringSubmatch(line)
		if len(matches) > 0 {
			kind := matches[1]
			name1 := matches[2]
			name2 := matches[3]
			if kind == "DEFAULT" || kind == "CREATE" {
				ts[name1] = true
			}
			if kind == "" { // from the grant match.
				ts[name2] = true
			}
			if kind == "TEMPORARY" {
				tsTemp[name1] = true
			}
		}
	}

	sqlResp, err := s.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{
			sqlq.QuerySetSessionContainer(PDBName),
			"select contents, tablespace_name from dba_tablespaces",
		},
	})
	if err != nil {
		klog.Warningf("createTablespacesFromSqlfile: query tablespaces failed: %v", err)
		return
	}

	// Check what tablespaces already exist and remove them from our list.
	tsJSONs := sqlResp.GetMsg()
	for _, js := range tsJSONs {
		row := make(map[string]string)
		if err := json.Unmarshal([]byte(js), &row); err != nil {
			klog.Warningf("createTablespacesFromSqlfile: failed to parse pdb tablespaces response: %v", err)
			return
		}
		name := row["TABLESPACE_NAME"]
		kind := row["CONTENTS"]
		if _, ok := ts[name]; ok && kind == "PERMANENT" {
			delete(ts, name)
		}
		if _, ok := tsTemp[name]; ok && kind == "TEMPORARY" {
			delete(tsTemp, name)
		}
	}

	// Create any remaining tablespaces
	for t := range ts {
		_, err := s.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
			Commands: []string{
				sqlq.QuerySetSessionContainer(PDBName),
				fmt.Sprintf("create bigfile tablespace \"%s\"", t),
			},
		})
		if err != nil {
			klog.Warningf("createTablespacesFromSqlfile: failed to create tablespace %s: %v", t, err)
		}
	}
	for t := range tsTemp {
		_, err := s.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
			Commands: []string{
				sqlq.QuerySetSessionContainer(PDBName),
				fmt.Sprintf("create temporary tablespace \"%s\" size 128M autoextend on", t),
			},
		})
		if err != nil {
			klog.Warningf("createTablespacesFromSqlfile: failed to create tablespace %s: %v", t, err)
		}
	}
}

// DataPumpImportAsync turns dataPumpImport into an async call.
func (s *Server) DataPumpImportAsync(ctx context.Context, req *dbdpb.DataPumpImportAsyncRequest) (*lropb.Operation, error) {
	job, err := lro.CreateAndRunLROJobWithID(ctx, req.GetLroInput().GetOperationId(), "DataPumpImport", s.lroServer,
		func(ctx context.Context) (proto.Message, error) {
			return s.dataPumpImport(ctx, req.SyncRequest)
		})

	if err != nil {
		klog.ErrorS(err, "dbdaemon/DataPumpImportAsync failed to create an LRO job", "request", req)
		return nil, err
	}

	return &lropb.Operation{Name: job.ID(), Done: false}, nil
}

// dataPumpExport runs expdp Oracle tool to export data to a data pump .dmp file.
func (s *Server) dataPumpExport(ctx context.Context, req *dbdpb.DataPumpExportRequest) (*dbdpb.DataPumpExportResponse, error) {
	s.syncJobs.pdbLoadMutex.Lock()
	defer s.syncJobs.pdbLoadMutex.Unlock()

	dmpObjectType := "SCHEMAS"
	exportName := fmt.Sprintf("export_%s", time.Now().Format("20060102150405"))
	dmpFile := exportName + ".dmp"
	dmpLogFile := exportName + ".log"
	parFile := exportName + ".par"

	if len(req.ObjectType) != 0 {
		dmpObjectType = req.ObjectType
	}

	pdbPath := fmt.Sprintf(consts.PDBPathPrefix, consts.DataMount, s.databaseSid.val, strings.ToUpper(req.PdbName))
	dmpPath := filepath.Join(pdbPath, consts.DpdumpDir.Linux, dmpFile) // full path
	parPath := filepath.Join(pdbPath, consts.DpdumpDir.Linux, parFile)

	klog.InfoS("dbdaemon/dataPumpExport", "dmpPath", dmpPath)

	// Remove the dmp file from os if it already exists because oracle will not dump to existing files.
	// expdp will log below errors:
	// ORA-39000: bad dump file specification
	// ORA-31641: unable to create dump file "/u02/app/oracle/oradata/TEST/PDB1/dmp/exportTable.dmp"
	// ORA-27038: created file already exists
	if err := os.Remove(dmpPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("dataPumpExport failed: can't remove existing dmp file %s", dmpPath)
	}

	expdpTarget, err := security.SetupUserPwConnStringOnServer(ctx, s, consts.PDBLoaderUser, req.PdbName, req.DbDomain)
	if err != nil {
		return nil, fmt.Errorf("dbdaemon/dataPumpExport: failed to alter user %s", consts.PDBLoaderUser)
	}

	var params []string
	params = append(params, fmt.Sprintf("%s=%s", dmpObjectType, req.Objects))
	params = append(params, fmt.Sprintf("DIRECTORY=%s", consts.DpdumpDir.Oracle))
	params = append(params, fmt.Sprintf("DUMPFILE=%s", dmpFile))
	params = append(params, fmt.Sprintf("LOGFILE=%s", dmpLogFile))
	params = append(params, req.CommandParams...)
	if len(req.FlashbackTime) != 0 {
		params = append(params, fmt.Sprintf("FLASHBACK_TIME=%q", req.FlashbackTime))
	}

	// To avoid having to supply additional quotation marks on the command line, Oracle recommends the use of parameter files.
	if err = writeParFile(parPath, params); err != nil {
		return nil, fmt.Errorf("data pump export failed, err = %v", err)
	}

	cmdParams := []string{expdpTarget}
	cmdParams = append(cmdParams, fmt.Sprintf("parfile=%s", parPath))
	if err := s.runCommand(expdp(s.databaseHome), cmdParams); err != nil {
		if s.osUtil.isReturnCodeEqual(err, 5) { // see dataPumpImport for an explanation of error code 5
			return nil, fmt.Errorf("data pump export failed, err = %v", err)
		}
		klog.Warning("dbdaemon/dataPumpExport: completed with EX_SUCC_ERR")
	}
	klog.Infof("dbdaemon/dataPumpExport: export to %s completed successfully", dmpPath)

	if err := s.gcsUtil.UploadFile(ctx, req.GcsPath, dmpPath, contentTypePlainText); err != nil {
		return nil, fmt.Errorf("dbdaemon/dataPumpExport: failed to upload dmp file to %s: %v", req.GcsPath, err)
	}
	klog.Infof("dbdaemon/dataPumpExport: uploaded dmp file to %s", req.GcsPath)

	if len(req.GcsLogPath) > 0 {
		logPath := filepath.Join(pdbPath, consts.DpdumpDir.Linux, dmpLogFile)

		if err := s.gcsUtil.UploadFile(ctx, req.GcsLogPath, logPath, contentTypePlainText); err != nil {
			return nil, fmt.Errorf("dbdaemon/dataPumpExport: failed to upload log file to %s: %v", req.GcsLogPath, err)
		}
		klog.Infof("dbdaemon/dataPumpExport: uploaded log file to %s", req.GcsLogPath)
	}

	return &dbdpb.DataPumpExportResponse{}, nil
}

// writeParFile writes data pump export parameter file in parPath.
func writeParFile(parPath string, params []string) error {
	f, err := os.Create(parPath)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); err != nil {
			klog.Warningf("failed to close %v: %v", f, err)
		}
	}()
	for _, param := range params {
		if _, err := f.WriteString(param + "\n"); err != nil {
			return err
		}
	}
	return nil
}

// DataPumpExportAsync turns dataPumpExport into an async call.
func (s *Server) DataPumpExportAsync(ctx context.Context, req *dbdpb.DataPumpExportAsyncRequest) (*lropb.Operation, error) {
	job, err := lro.CreateAndRunLROJobWithID(ctx, req.GetLroInput().GetOperationId(), "DataPumpExport", s.lroServer,
		func(ctx context.Context) (proto.Message, error) {
			return s.dataPumpExport(ctx, req.SyncRequest)
		})

	if err != nil {
		klog.ErrorS(err, "dbdaemon/DataPumpExportAsync failed to create an LRO job", "request", req)
		return nil, err
	}
	return &lropb.Operation{Name: job.ID(), Done: false}, nil
}

// Restart database in upgrade mode, apply 'datapath', restart in normal mode
// Executes following steps:
// SQL> shutdown immediate
// SQL> startup nomount
// SQL> alter database mount
// SQL> alter database open upgrade
// SQL> alter pluggable database all open upgrade
// /u01/app/oracle/product/12.2/db/OPatch/datapatch -verbose
// SQL> shutdown immediate
// SQL> startup
// SQL> alter pluggable database all open
func (s *Server) applyDataPatch(ctx context.Context) (*dbdpb.ApplyDataPatchResponse, error) {
	s.syncJobs.maintenanceMutex.Lock()
	defer s.syncJobs.maintenanceMutex.Unlock()

	klog.InfoS("dbdaemon/applyDataPatch started")

	// Ask proxy to shutdown the DB
	if _, err := s.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
		DatabaseName: s.databaseSid.val,
		Option:       "immediate",
	}); err != nil {
		return nil, fmt.Errorf("proxy request to shutdown DB failed: %w", err)
	}

	klog.InfoS("dbdaemon/applyDataPatch DB is down, starting in upgrade mode")

	// Ask proxy to startup the DB in NOMOUNT mode
	if _, err := s.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
		DatabaseName: s.databaseSid.val,
		Option:       "nomount",
	}); err != nil {
		return nil, fmt.Errorf("proxy request to startup DB failed: %w", err)
	}

	// Set upgrade mode (possible OJVM upgrades still require this, but may fail some standard patches).
	if err := s.database.setDatabaseUpgradeMode(ctx); err != nil {
		return nil, err
	}

	klog.InfoS("dbdaemon/applyDataPatch DB is in migrate state, starting datapatch")

	// oracle/product/12.2/db/OPatch/datapatch -verbose
	dpCode := 0
	if err := s.runCommand(datapatch(s.databaseHome), []string{"-verbose"}); err != nil {
		if exitError, ok := err.(*exec.ExitError); !ok {
			return nil, fmt.Errorf("datapatch failed: %w", err)
		} else {
			dpCode = exitError.ExitCode()
		}
	}
	// We will try again in normal mode.
	klog.InfoS("dbdaemon/applyDataPatch datapatch attempt in upgrade mode completed, restarting DB in normal mode", "return code", dpCode)

	// Ask proxy to shutdown the DB
	// SQL> shutdown immediate
	if _, err := s.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
		DatabaseName: s.databaseSid.val,
		Option:       "immediate",
	}); err != nil {
		return nil, fmt.Errorf("proxy request to shutdown DB failed: %w", err)
	}

	// Ask proxy to startup the DB
	// SQL> startup
	if _, err := s.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
		DatabaseName: s.databaseSid.val,
		Option:       "",
	}); err != nil {
		return nil, fmt.Errorf("proxy request to startup DB failed: %w", err)
	}

	// SQL> alter pluggable database all open
	if err := s.database.openPDBs(ctx); err != nil {
		return nil, err
	}
	// At this point CDB$ROOT, PDB$SEED and all PDBs should be in normal 'RW' or 'RO' state

	// Retry datapatch in normal mode for those that require it.
	if err := s.runCommand(datapatch(s.databaseHome), []string{"-verbose"}); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("datapatch failed in normal mode with exit code = %v: %w", exitError.ExitCode(), err)
		}
		return nil, fmt.Errorf("datapatch failed: %w", err)
	}

	klog.InfoS("dbdaemon/applyDataPatch completed, DB is back up")
	return &dbdpb.ApplyDataPatchResponse{}, nil
}

// ApplyDataPatchAsync turns applyDataPatch into an async call.
func (s *Server) ApplyDataPatchAsync(ctx context.Context, req *dbdpb.ApplyDataPatchAsyncRequest) (*lropb.Operation, error) {
	job, err := lro.CreateAndRunLROJobWithID(ctx, req.GetLroInput().GetOperationId(), "ApplyDataPatch", s.lroServer,
		func(ctx context.Context) (proto.Message, error) {
			// Protect databaseSid
			s.databaseSid.Lock()
			defer s.databaseSid.Unlock()
			resp, err := s.applyDataPatch(ctx)
			if err != nil {
				klog.ErrorS(err, "dbdaemon/ApplyDataPatchAsync failed")
			}
			return resp, err
		})

	if err != nil {
		klog.ErrorS(err, "dbdaemon/ApplyDataPatchAsync failed to create an LRO job", "request", req)
		return nil, err
	}
	return &lropb.Operation{Name: job.ID(), Done: false}, nil
}

// ListOperations returns a paged list of currently managed long running operations.
func (s *Server) ListOperations(ctx context.Context, req *lropb.ListOperationsRequest) (*lropb.ListOperationsResponse, error) {
	return s.lroServer.ListOperations(ctx, req)
}

// GetOperation returns details of a requested long running operation.
func (s *Server) GetOperation(ctx context.Context, req *lropb.GetOperationRequest) (*lropb.Operation, error) {
	return s.lroServer.GetOperation(ctx, req)
}

// DeleteOperation deletes a long running operation by its id.
func (s *Server) DeleteOperation(ctx context.Context, req *lropb.DeleteOperationRequest) (*empty.Empty, error) {
	return s.lroServer.DeleteOperation(ctx, req)
}

func (s *Server) runCommand(bin string, params []string) error {
	// Sets env to bounce a database|listener.
	if err := os.Setenv("ORACLE_SID", s.databaseSid.val); err != nil {
		return fmt.Errorf("failed to set env variable: %v", err)
	}

	return s.osUtil.runCommand(bin, params)
}

var newDB = func(driverName, dataSourceName string) (oracleDatabase, error) {
	return sql.Open(driverName, dataSourceName)
}

// open returns a connection to the given database URL,
// When `prelim` is true, open will make a second connection attempt
// if the first connection fails.
//
// The caller is responsible for closing the returned connection.
//
// open method is created to break down runSQLPlusHelper and make the code
// testable, thus it returns interface oracleDatabase.
func open(ctx context.Context, dbURL string, prelim bool) (oracleDatabase, error) {
	// "/ as sysdba"
	db, err := newDB("godror", dbURL)
	if err == nil {
		// Force a connection with Ping.
		err = db.Ping()
		if err == nil {
			return db, nil
		}
		// Connection pool opened but ping failed, close this pool.
		if errC := db.Close(); errC != nil {
			klog.Warningf("failed to close db connection: %v", errC)
		}
	}

	if !prelim {
		klog.ErrorS(err, "dbdaemon/open: newDB failed", "prelim", prelim)
		return nil, err
	}

	// If a prelim connection is requested (e.g. for creating
	// an spfile) retry with prelim argument.
	db, err = newDB("godror", dbURL+"&prelim=1")
	if err != nil {
		klog.ErrorS(err, "dbdaemon/open: newDB failed prelim retry", "prelim", prelim)
		return nil, err
	}

	return db, nil
}

func (d *DB) runSQL(ctx context.Context, sqls []string, prelim, suppress bool, db oracleDatabase) ([]string, error) {
	sqlForLogging := strings.Join(sqls, ";")
	if suppress {
		sqlForLogging = "suppressed"
	}

	// This will fail on prelim connections, so ignore errors in that case
	if _, err := db.ExecContext(ctx, "BEGIN DBMS_OUTPUT.ENABLE(); END;"); err != nil && !prelim {
		klog.ErrorS(err, "dbdaemon/runSQL: failed to enable dbms_output", "sql", sqlForLogging)
		return nil, err
	}

	klog.InfoS("dbdaemon/runSQL: running SQL statements", "sql", sqlForLogging)

	output := []string{}
	for _, sql := range sqls {
		if _, err := db.ExecContext(ctx, sql); err != nil {
			klog.ErrorS(err, "dbdaemon/runSQL: failed to execute", "sql", sqlForLogging)
			return nil, err
		}
		out, err := dbmsOutputGetLines(ctx, db)
		if err != nil && !prelim {
			klog.ErrorS(err, "dbdaemon/runSQL: failed to get DBMS_OUTPUT", "sql", sqlForLogging)
			return nil, err
		}
		output = append(output, out...)
	}

	return output, nil
}

func (d *DB) runQuery(ctx context.Context, sqls []string, db oracleDatabase) ([]string, error) {
	//TODO: Query suppression
	klog.InfoS("dbdaemon/runQuery: running sql", "sql", sqls)
	sqlLen := len(sqls)
	for i := 0; i < sqlLen-1; i++ {
		if _, err := db.ExecContext(ctx, sqls[i]); err != nil {
			return nil, err
		}
	}
	rows, err := db.QueryContext(ctx, sqls[sqlLen-1])
	if err != nil {
		klog.ErrorS(err, "dbdaemon/runQuery: failed to query a database", "sql", sqls[sqlLen-1])
		return nil, err
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		klog.ErrorS(err, "dbdaemon/runQuery: failed to get column names for query", "sql", sqls[sqlLen-1])
		return nil, err
	}

	var output []string
	for rows.Next() {
		// Store as strings, database/sql will handle conversion to
		// string type for us in Rows.Scan.
		data := make([]string, len(colNames))
		dataPtr := make([]interface{}, len(colNames))
		for i := range colNames {
			dataPtr[i] = &data[i]
		}
		if err := rows.Scan(dataPtr...); err != nil {
			klog.ErrorS(err, "dbdaemon/runQuery: failed to read a row")
			return nil, err
		}

		// Convert row to JSON map
		dataMap := map[string]string{}
		for i, colName := range colNames {
			dataMap[colName] = data[i]
		}
		j, err := json.Marshal(dataMap)
		if err != nil {
			klog.ErrorS(err, "dbdaemon/runQuery: failed to marshal a data map", "dataMap", dataMap)
			return nil, err
		}
		output = append(output, string(j))
	}
	return output, nil
}

func (s *Server) runSQLPlusHelper(ctx context.Context, req *dbdpb.RunSQLPlusCMDRequest, formattedSQL bool) (*dbdpb.RunCMDResponse, error) {
	if req.GetTnsAdmin() != "" {
		if err := os.Setenv("TNS_ADMIN", req.GetTnsAdmin()); err != nil {
			return nil, fmt.Errorf("failed to set env variable: %v", err)
		}
		defer func() {
			if err := os.Unsetenv("TNS_ADMIN"); err != nil {
				klog.Warningf("failed to unset env variable: %v", err)
			}
		}()
	}

	sqls := req.GetCommands()
	if len(sqls) < 1 {
		return nil, fmt.Errorf("dbdaemon/RunSQLPlus requires a sql statement to run, provided: %d", len(sqls))
	}

	// formattedSQL = query, hence it is not an op that needs a prelim conn.
	// Only enable prelim for known prelim queries, CREATE SPFILE and CREATE PFILE.
	var prelim bool
	if !formattedSQL && (strings.HasPrefix(strings.ToLower(sqls[0]), "create spfile") ||
		strings.HasPrefix(strings.ToLower(sqls[0]), "create pfile")) {
		prelim = true
	}

	// This default connect string requires the ORACLE_SID env variable to be set.
	connectString := "oracle://?sysdba=1"

	switch req.ConnectInfo.(type) {
	case *dbdpb.RunSQLPlusCMDRequest_Dsn:
		connectString = req.GetDsn()
	case *dbdpb.RunSQLPlusCMDRequest_DatabaseName:
		if err := os.Setenv("ORACLE_SID", req.GetDatabaseName()); err != nil {
			return nil, fmt.Errorf("failed to set env variable: %v", err)
		}
	case *dbdpb.RunSQLPlusCMDRequest_Local:
		if err := os.Setenv("ORACLE_SID", s.databaseSid.val); err != nil {
			return nil, fmt.Errorf("failed to set env variable: %v", err)
		}
	default:
		// For backward compatibility if connect_info field isn't defined in the request
		// we fallback to the Local option.
		if err := os.Setenv("ORACLE_SID", s.databaseSid.val); err != nil {
			return nil, fmt.Errorf("failed to set env variable: %v", err)
		}
	}

	db, err := open(ctx, connectString, prelim)
	if err != nil {
		return nil, fmt.Errorf("dbdaemon/RunSQLPlus failed to open a database connection: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			klog.Warningf("failed to close db connection: %v", err)
		}
	}()

	var o []string
	if formattedSQL {
		o, err = s.database.runQuery(ctx, sqls, db)
	} else {
		o, err = s.database.runSQL(ctx, sqls, prelim, req.GetSuppress(), db)
	}
	if err != nil {
		klog.ErrorS(err, "dbdaemon/RunSQLPlus: error in execution", "formattedSQL", formattedSQL, "ORACLE_SID", s.databaseSid.val)
		return nil, err
	}

	klog.InfoS("dbdaemon/RunSQLPlus", "output", strings.Join(o, "\n"))
	return &dbdpb.RunCMDResponse{Msg: o}, nil
}

// RunSQLPlus executes oracle's sqlplus and returns output.
// This function only returns DBMS_OUTPUT and not any row data.
// To read from SELECTs use RunSQLPlusFormatted.
func (s *Server) RunSQLPlus(ctx context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
	// Add lock to protect server state "databaseSid" and os env variable "ORACLE_SID".
	// Only add lock in top level API to avoid deadlock.
	s.databaseSid.Lock()
	defer s.databaseSid.Unlock()

	if req.GetSuppress() {
		klog.InfoS("dbdaemon/RunSQLPlus", "req", "suppressed", "SID", s.databaseSid.val, "serverObj", s)
	} else {
		klog.InfoS("dbdaemon/RunSQLPlus", "req", req, "SID", s.databaseSid.val, "serverObj", s)
	}

	return s.runSQLPlusHelper(ctx, req, false)
}

// RunSQLPlusFormatted executes a SQL command and returns the row results.
// If instead you want DBMS_OUTPUT please issue RunSQLPlus
func (s *Server) RunSQLPlusFormatted(ctx context.Context, req *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error) {
	// Add lock to protect server state "databaseSid" and os env variable "ORACLE_SID".
	// Only add lock in top level API to avoid deadlock.
	s.databaseSid.Lock()
	defer s.databaseSid.Unlock()

	if req.GetSuppress() {
		klog.InfoS("dbdaemon/RunSQLPlusFormatted", "req", "suppressed", "SID", s.databaseSid.val, "serverObj", s)
	} else {
		klog.InfoS("dbdaemon/RunSQLPlusFormatted", "req", req, "SID", s.databaseSid.val, "serverObj", s)
	}

	return s.runSQLPlusHelper(ctx, req, true)
}

// KnownPDBs runs a database query returning a list of PDBs known
// to a database. By default it doesn't include a seed PDB.
// It also by default doesn't pay attention to a state of a PDB.
// A caller can overwrite both of the above settings with the flags.
func (s *Server) KnownPDBs(ctx context.Context, req *dbdpb.KnownPDBsRequest) (*dbdpb.KnownPDBsResponse, error) {
	klog.InfoS("dbdaemon/KnownPDBs", "req", req, "serverObj", s)
	// Add lock to protect server state "databaseSid" and os env variable "ORACLE_SID".
	// Only add lock in top level API to avoid deadlock.
	s.databaseSid.RLock()
	defer s.databaseSid.RUnlock()
	knownPDBs, err := s.knownPDBs(ctx, req.GetIncludeSeed(), req.GetOnlyOpen())
	if err != nil {
		return nil, err
	}
	return &dbdpb.KnownPDBsResponse{KnownPdbs: knownPDBs}, nil
}

func (s *Server) knownPDBs(ctx context.Context, includeSeed, onlyOpen bool) ([]string, error) {
	sql := consts.ListPDBsSQL

	if !includeSeed {
		where := "and name != 'PDB$SEED'"
		sql = fmt.Sprintf("%s %s", sql, where)
	}

	if onlyOpen {
		where := "and open_mode = 'READ WRITE'"
		sql = fmt.Sprintf("%s %s", sql, where)

	}

	resp, err := s.runSQLPlusHelper(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{sql}}, true)
	if err != nil {
		return nil, err
	}
	klog.InfoS("dbdaemon/knownPDBs", "resp", resp)

	var knownPDBs []string
	for _, msg := range resp.Msg {
		row := make(map[string]string)
		if err := json.Unmarshal([]byte(msg), &row); err != nil {
			klog.ErrorS(err, "dbdaemon/knownPDBS: failed to unmarshal PDB query resultset")
			return nil, err
		}
		if name, ok := row["NAME"]; ok {
			knownPDBs = append(knownPDBs, name)
		}
	}
	klog.InfoS("dbdaemon/knownPDBs", "knownPDBs", knownPDBs)

	return knownPDBs, nil
}

func (s *Server) isKnownPDB(ctx context.Context, name string, includeSeed, onlyOpen bool) (bool, []string) {
	knownPDBs, err := s.knownPDBs(ctx, includeSeed, onlyOpen)
	if err != nil {
		return false, nil
	}

	for _, pdb := range knownPDBs {
		if pdb == strings.ToUpper(name) {
			return true, knownPDBs
		}
	}
	return false, knownPDBs
}

// CheckDatabaseState pings a database to check its status.
// This method has been tested for checking a CDB state.
func (s *Server) CheckDatabaseState(ctx context.Context, req *dbdpb.CheckDatabaseStateRequest) (*dbdpb.CheckDatabaseStateResponse, error) {
	klog.InfoS("dbdaemon/CheckDatabaseState", "req", req, "serverObj", s)
	reqDatabaseName := req.GetDatabaseName()
	if reqDatabaseName == "" {
		return nil, fmt.Errorf("a database check is requested, but a mandatory database name parameter is not provided (server: %v)", s)
	}

	var dbURL string
	if req.GetIsCdb() {
		// Local connection, set env variables.
		if err := os.Setenv("ORACLE_SID", req.GetDatabaseName()); err != nil {
			return nil, err
		}

		// Even for CDB check, use TNS connection to verify listener health.
		cs, pass, err := security.SetupConnStringOnServer(ctx, s, consts.SecurityUser, req.GetDatabaseName(), req.GetDbDomain())
		if err != nil {
			return nil, fmt.Errorf("dbdaemon/CheckDatabaseState: failed to alter user %s", consts.SecurityUser)
		}
		dbURL = fmt.Sprintf("user=%q password=%q connectString=%q standaloneConnection=true",
			consts.SecurityUser, pass, cs)
	} else {
		// A PDB that a Database Daemon is requested to operate on
		// must be part of the Server object (set based on the metadata).
		// (a "part of" is for a future support for multiple PDBs per CDB).
		if known, knownPDBs := s.isKnownPDB(ctx, reqDatabaseName, false, false); !known {
			return nil, fmt.Errorf("%q is not in the known PDB list: %v", reqDatabaseName, knownPDBs)
		}

		// Alter security password and if it's not been set yet.
		if s.pdbConnStr == "" {
			cs, err := security.SetupUserPwConnStringOnServer(ctx, s, consts.SecurityUser, reqDatabaseName, req.GetDbDomain())
			if err != nil {
				return nil, fmt.Errorf("dbdaemon/CheckDatabaseState: failed to alter user %s", consts.SecurityUser)
			}
			s.pdbConnStr = cs
		}
		// Use new PDB connection string to check PDB status.
		dbURL = s.pdbConnStr
	}

	db, err := sql.Open("godror", dbURL)
	if err != nil {
		klog.ErrorS(err, "dbdaemon/CheckDatabaseState: failed to open a database")
		return nil, err
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		klog.ErrorS(err, "dbdaemon/CheckDatabaseState: database not running")
		return nil, fmt.Errorf("cannot connect to database %s: %v", reqDatabaseName, err)
	}
	return &dbdpb.CheckDatabaseStateResponse{}, nil
}

// RunRMAN will run the script to execute RMAN and create a physical backup in the target directory, then back it up to GCS if requested
func (s *Server) RunRMAN(ctx context.Context, req *dbdpb.RunRMANRequest) (*dbdpb.RunRMANResponse, error) {
	// Required for local connections (when no SID is specified on connect string).
	// Add lock to protect server state "databaseSid" and os env variable "ORACLE_SID".
	// Only add lock in top level API to avoid deadlock.
	if req.GetSuppress() {
		klog.InfoS("RunRMAN", "request", "suppressed")
	} else {
		klog.InfoS("RunRMAN", "request", req)
	}

	s.databaseSid.RLock()
	defer s.databaseSid.RUnlock()
	if err := os.Setenv("ORACLE_SID", s.databaseSid.val); err != nil {
		return nil, fmt.Errorf("failed to set env variable: %v", err)
	}

	if req.GetTnsAdmin() != "" {
		if err := os.Setenv("TNS_ADMIN", req.GetTnsAdmin()); err != nil {
			return nil, fmt.Errorf("failed to set env variable: %v", err)
		}
		defer func() {
			if err := os.Unsetenv("TNS_ADMIN"); err != nil {
				klog.Warningf("failed to unset env variable: %v", err)
			}
		}()
	}

	scripts := req.GetScripts()
	if len(scripts) < 1 {
		return nil, fmt.Errorf("RunRMAN requires at least 1 script to run, provided: %d", len(scripts))
	}
	var res []string
	for _, script := range scripts {
		var args []string
		target := "/"
		if req.GetTarget() != "" {
			target = req.GetTarget()
		}

		if !req.GetWithoutTarget() {
			args = append(args, fmt.Sprintf("target=%s", target))
		}

		if req.GetAuxiliary() != "" {
			args = append(args, fmt.Sprintf("auxiliary=%s", req.Auxiliary))
		}

		args = append(args, "@/dev/stdin")

		cmd := exec.Command(rman(s.databaseHome), args...)
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("RunRMAN failed,\nscript: %q\nFailed with: %v\nErr: %v", script, string(out), err)
		}
		res = append(res, string(out))

		if req.GetGcsPath() != "" && req.GetGcsOp() == dbdpb.RunRMANRequest_UPLOAD {
			if err = s.uploadDirectoryContentsToGCS(ctx, consts.RMANStagingDir, req.GetGcsPath()); err != nil {
				klog.ErrorS(err, "GCS Upload error:")
				return nil, err
			}
		}
	}

	return &dbdpb.RunRMANResponse{Output: res}, nil
}

// RunRMANAsync turns RunRMAN into an async call.
func (s *Server) RunRMANAsync(ctx context.Context, req *dbdpb.RunRMANAsyncRequest) (*lropb.Operation, error) {
	job, err := lro.CreateAndRunLROJobWithID(ctx, req.GetLroInput().GetOperationId(), "RMAN", s.lroServer,
		func(ctx context.Context) (proto.Message, error) {
			return s.RunRMAN(ctx, req.SyncRequest)
		})

	if err != nil {
		klog.ErrorS(err, "dbdaemon/RunRMANAsync failed to create an LRO job", "request", req)
		return nil, err
	}

	return &lropb.Operation{Name: job.ID(), Done: false}, nil
}

// RunDataGuardBroker RPC call executes Oracle's Data Guard command line utility.
func (s *Server) RunDataGuard(ctx context.Context, req *dbdpb.RunDataGuardRequest) (*dbdpb.RunDataGuardResponse, error) {
	s.databaseSid.RLock()
	defer s.databaseSid.RUnlock()
	if err := os.Setenv("ORACLE_SID", s.databaseSid.val); err != nil {
		return nil, fmt.Errorf("failed to set env variable: %v", err)
	}
	if err := os.Setenv("ORACLE_HOME", s.databaseHome); err != nil {
		return nil, fmt.Errorf("failed to set env variable: %v", err)
	}

	scripts := req.GetScripts()
	if len(scripts) < 1 {
		return nil, fmt.Errorf("RunDataGuard requires at least 1 script to run, provided: %d", len(scripts))
	}
	var res []string
	for _, script := range scripts {
		target := "/"
		if req.GetTarget() != "" {
			target = req.GetTarget()
		}
		args := []string{"-silent", target}
		args = append(args, script)
		cmd := exec.CommandContext(ctx, dgmgrl(s.databaseHome), args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("RunDataGuard failed, script: %q\nFailed with: %v\nErr: %v", script, string(out), err)
		}
		res = append(res, string(out))
	}
	return &dbdpb.RunDataGuardResponse{Output: res}, nil
}

// TNSPing RPC call executes Oracle's tnsping utility.
func (s *Server) TNSPing(ctx context.Context, req *dbdpb.TNSPingRequest) (*dbdpb.TNSPingResponse, error) {
	cmd := exec.CommandContext(ctx, tnsping(s.databaseHome), req.GetConnectionString())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("tnsping failed \n Failed with: %v\nErr: %v", string(out), err)
	}
	return &dbdpb.TNSPingResponse{}, nil
}

func (s *Server) uploadDirectoryContentsToGCS(ctx context.Context, backupDir, gcsPath string) error {
	klog.InfoS("RunRMAN: uploadDirectoryContentsToGCS", "backupdir", backupDir, "gcsPath", gcsPath)
	err := filepath.Walk(backupDir, func(fpath string, info os.FileInfo, errInner error) error {
		klog.InfoS("RunRMAN: walking...", "fpath", fpath, "info", info, "errInner", errInner)
		if errInner != nil {
			return errInner
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(backupDir, fpath)
		if err != nil {
			return errors.Errorf("filepath.Rel(%s, %s) returned err: %s", backupDir, fpath, err)
		}
		gcsTarget, err := url.Parse(gcsPath)
		if err != nil {
			return errors.Errorf("invalid GcsPath err: %v", err)
		}
		gcsTarget.Path = path.Join(gcsTarget.Path, relPath)
		klog.InfoS("gcs", "target", gcsTarget)
		start := time.Now()
		err = s.gcsUtil.UploadFile(ctx, gcsTarget.String(), fpath, contentTypePlainText)
		if err != nil {
			return err
		}
		end := time.Now()
		rate := float64(info.Size()) / (end.Sub(start).Seconds())
		klog.InfoS("dbdaemon/uploadDirectoryContentsToGCS", "uploaded", gcsTarget.String(), "throughput", fmt.Sprintf("%f MB/s", rate/1024/1024))

		return nil
	})

	if err := os.RemoveAll(consts.RMANStagingDir); err != nil {
		klog.Warningf("uploadDirectoryContentsToGCS: can't cleanup staging dir from local disk.")
	}
	return err
}

// NID changes a database id and/or database name.
func (s *Server) NID(ctx context.Context, req *dbdpb.NIDRequest) (*dbdpb.NIDResponse, error) {
	params := []string{"target=/"}
	if req.GetSid() == "" {
		return nil, fmt.Errorf("dbdaemon/NID: missing sid for req: %v", req)
	}

	if err := os.Setenv("ORACLE_SID", req.GetSid()); err != nil {
		return nil, fmt.Errorf("dbdaemon/NID: set env ORACLE_SID failed: %v", err)
	}

	// Add lock to protect server state "databaseSid" and os env variable "ORACLE_SID".
	// When renaming the DB, DB is not ready to run cmds or SQLs, it seems to be ok to block all other APIs for now.
	s.databaseSid.Lock()
	defer s.databaseSid.Unlock()
	if req.GetDatabaseName() != "" {
		s.databaseSid.val = req.GetDatabaseName()
		params = append(params, fmt.Sprintf("dbname=%s", req.GetDatabaseName()))
	}

	params = append(params, "logfile=/home/oracle/nid.log")

	_, err := s.dbdClient.ProxyRunNID(ctx, &dbdpb.ProxyRunNIDRequest{Params: params, DestDbName: req.GetDatabaseName()})
	if err != nil {
		return nil, fmt.Errorf("nid failed: %v", err)
	}

	klog.InfoS("dbdaemon/NID: done", "req", req)
	return &dbdpb.NIDResponse{}, nil
}

// GetDatabaseType returns database type, eg. ORACLE_12_2_ENTERPRISE_NONCDB
func (s *Server) GetDatabaseType(ctx context.Context, req *dbdpb.GetDatabaseTypeRequest) (*dbdpb.GetDatabaseTypeResponse, error) {
	f, err := os.Open(consts.OraTab)
	if err != nil {
		return nil, fmt.Errorf("GetDatabaseType: failed to open %q", consts.OraTab)
	}
	defer func() {
		if err := f.Close(); err != nil {
			klog.Warningf("failed to close %v: %v", f, err)
		}
	}()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// The content of oratab is expected to be of the form:
		// # comments
		// <CDB name>:DatabaseHome:<Y/N>
		// # DATABASETYPE:ORACLE_12_2_ENTERPRISE_NONCDB
		if !strings.HasPrefix(line, "# DATABASETYPE") {
			continue
		}
		fragment := strings.Split(line, ":")
		if len(fragment) != 2 {
			return nil, fmt.Errorf("GetDatabaseType: failed to parse %q for database type(number of fields is %d, not 2)", consts.OraTab, len(fragment))
		}

		switch fragment[1] {
		case "ORACLE_12_2_ENTERPRISE":
			return &dbdpb.GetDatabaseTypeResponse{
				DatabaseType: dbdpb.GetDatabaseTypeResponse_ORACLE_12_2_ENTERPRISE,
			}, nil
		case "ORACLE_12_2_ENTERPRISE_NONCDB":
			return &dbdpb.GetDatabaseTypeResponse{
				DatabaseType: dbdpb.GetDatabaseTypeResponse_ORACLE_12_2_ENTERPRISE_NONCDB,
			}, nil
		default:
			return nil, fmt.Errorf("GetDatabaseType: failed to get valid database type from %q", consts.OraTab)
		}
	}

	// For backward compatibility, return ORACLE_12_2_ENTERPRISE by default
	return &dbdpb.GetDatabaseTypeResponse{
		DatabaseType: dbdpb.GetDatabaseTypeResponse_ORACLE_12_2_ENTERPRISE,
	}, nil
}

// GetDatabaseName returns database name.
func (s *Server) GetDatabaseName(ctx context.Context, req *dbdpb.GetDatabaseNameRequest) (*dbdpb.GetDatabaseNameResponse, error) {
	//databaseSid value will be set in dbdserver's constructor and NID API with write lock.
	//databaseSid is expected to be valid in dbdserver's life cycle.
	s.databaseSid.RLock()
	defer s.databaseSid.RUnlock()

	return &dbdpb.GetDatabaseNameResponse{DatabaseName: s.databaseSid.val}, nil
}

// BounceDatabase starts/stops request specified database.
func (s *Server) BounceDatabase(ctx context.Context, req *dbdpb.BounceDatabaseRequest) (*dbdpb.BounceDatabaseResponse, error) {
	s.databaseSid.RLock()
	defer s.databaseSid.RUnlock()
	klog.InfoS("BounceDatabase request delegated to proxy", "req", req)
	database, err := s.dbdClient.BounceDatabase(ctx, req)
	if err != nil {
		msg := "dbdaemon/BounceDatabase: error while bouncing database"
		klog.InfoS(msg, "err", err)
		return nil, fmt.Errorf("%s: %v", msg, err)
	}
	if req.Operation == dbdpb.BounceDatabaseRequest_STARTUP && !req.GetAvoidConfigBackup() {
		if err := s.BackupConfigFile(ctx, s.databaseSid.val); err != nil {
			msg := "dbdaemon/BounceDatabase: error while backing up config file: err"
			klog.InfoS(msg, "err", err)
			return nil, fmt.Errorf("%s: %v", msg, err)
		}
		klog.InfoS("dbdaemon/BounceDatabase start operation: config file backup successful")
	}
	return database, err
}

// BounceListener starts/stops request specified listener.
func (s *Server) BounceListener(ctx context.Context, req *dbdpb.BounceListenerRequest) (*dbdpb.BounceListenerResponse, error) {
	klog.InfoS("BounceListener request delegated to proxy", "req", req)
	return s.dbdClient.BounceListener(ctx, req)
}

func (s *Server) close() {
	if err := s.dbdClientClose(); err != nil {
		klog.Warningf("failed to close dbdaemon client: %v", err)
	}
}

// createCDB creates a database instance
func (s *Server) createCDB(ctx context.Context, req *dbdpb.CreateCDBRequest) (*dbdpb.CreateCDBResponse, error) {
	klog.InfoS("CreateCDB request invoked", "req", req)

	password, err := security.RandOraclePassword()
	if err != nil {
		return nil, fmt.Errorf("error generating temporary password")
	}
	characterSet := req.GetCharacterSet()
	sid := req.GetDatabaseName()
	memoryPercent := req.GetMemoryPercent()
	var initParams string

	if sid == "" {
		return nil, fmt.Errorf("dbdaemon/CreateCDB: DBname is empty")
	}
	if characterSet == "" {
		characterSet = "AL32UTF8"
	}
	if memoryPercent == 0 {
		memoryPercent = 25
	}
	if req.GetAdditionalParams() == nil {
		initParams = strings.Join(provision.MapToSlice(provision.GetDefaultInitParams(req.DatabaseName)), ",")
		if req.GetDbDomain() != "" {
			initParams = fmt.Sprintf("%s,DB_DOMAIN=%s", initParams, req.GetDbDomain())
		}
	} else {

		foundDBDomain := false
		for _, param := range req.GetAdditionalParams() {
			if strings.Contains(strings.ToUpper(param), "DB_DOMAIN=") {
				foundDBDomain = true
				break
			}
		}
		initParamsArr := req.GetAdditionalParams()
		if !foundDBDomain && req.GetDbDomain() != "" {
			initParamsArr = append(initParamsArr, fmt.Sprintf("DB_DOMAIN=%s", req.GetDbDomain()))
		}

		initParamsMap, err := provision.MergeInitParams(provision.GetDefaultInitParams(req.DatabaseName), initParamsArr)
		if err != nil {
			return nil, fmt.Errorf("error while merging user defined init params with default values, %v", err)
		}
		initParamsArr = provision.MapToSlice(initParamsMap)
		initParams = strings.Join(initParamsArr, ",")
	}

	params := []string{
		"-silent",
		"-createDatabase",
		"-templateName", "General_Purpose.dbc",
		"-gdbName", sid,
		"-responseFile", "NO_VALUE",
		"-createAsContainerDatabase", strconv.FormatBool(true),
		"-sid", sid,
		"-characterSet", characterSet,
		fmt.Sprintf("-memoryPercentage"), strconv.FormatInt(int64(memoryPercent), 10),
		"-emConfiguration", "NONE",
		"-datafileDestination", oraDataDir,
		"-storageType", "FS",
		"-initParams", initParams,
		"-databaseType", "MULTIPURPOSE",
		"-recoveryAreaDestination", "/u03/app/oracle/fast_recovery_area",
		"-sysPassword", password,
		"-systemPassword", password,
	}

	_, err = s.dbdClient.ProxyRunDbca(ctx, &dbdpb.ProxyRunDbcaRequest{OracleHome: s.databaseHome, DatabaseName: req.DatabaseName, Params: params})
	if err != nil {
		return nil, fmt.Errorf("error while running dbca command: %v", err)
	}
	klog.InfoS("dbdaemon/CreateCDB: CDB created successfully")

	if _, err := s.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
		DatabaseName: req.GetDatabaseName(),
	}); err != nil {
		return nil, fmt.Errorf("dbdaemon/CreateCDB: shutdown failed: %v", err)
	}

	klog.InfoS("dbdaemon/CreateCDB successfully completed")
	return &dbdpb.CreateCDBResponse{}, nil
}

// CreateCDBAsync turns CreateCDB into an async call.
func (s *Server) CreateCDBAsync(ctx context.Context, req *dbdpb.CreateCDBAsyncRequest) (*lropb.Operation, error) {
	job, err := lro.CreateAndRunLROJobWithID(ctx, req.GetLroInput().GetOperationId(), "CreateCDB", s.lroServer,
		func(ctx context.Context) (proto.Message, error) {
			return s.createCDB(ctx, req.SyncRequest)
		})

	if err != nil {
		klog.ErrorS(err, "dbdaemon/CreateCDBAsync failed to create an LRO job", "request", req)
		return nil, err
	}

	return &lropb.Operation{Name: job.ID(), Done: false}, nil
}

func setEnvNew(s *Server, home string, dbName string) error {
	s.databaseHome = home
	s.databaseSid.val = dbName
	if err := provision.RelinkConfigFiles(home, dbName); err != nil {
		return err
	}
	return nil
}

// markProvisioned creates a flag file to indicate that CDB provisioning completed successfully
func markProvisioned() error {
	f, err := os.Create(consts.ProvisioningDoneFile)
	if err != nil {
		return fmt.Errorf("could not create %s file: %v", consts.ProvisioningDoneFile, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			klog.Warningf("failed to close %v: %v", f, err)
		}
	}()
	return nil
}

// A user running this program should not be root and
// a primary group should be either dba or oinstall.
func oracleUserUIDGID(skipChecking bool) (uint32, uint32, error) {
	if skipChecking {
		klog.InfoS("oracleUserUIDGID: skipped by request")
		return 0, 0, nil
	}
	u, err := user.Lookup(consts.OraUser)
	if err != nil {
		return 0, 0, fmt.Errorf("oracleUserUIDGID: could not determine the current user: %v", err)
	}

	if u.Username == "root" {
		return 0, 0, fmt.Errorf("oracleUserUIDGID: this program is designed to run by the Oracle software installation owner (e.g. oracle), not %q", u.Username)
	}

	groups := consts.OraGroup
	var gids []string
	for _, group := range groups {
		g, err := user.LookupGroup(group)
		// Not both groups are mandatory, e.g. oinstall may not exist.
		klog.InfoS("group=%s, g=%v", group, g)
		if err != nil {
			continue
		}
		gids = append(gids, g.Gid)
	}
	for _, g := range gids {
		if u.Gid == g {
			usr, err := strconv.ParseUint(u.Uid, 10, 32)
			if err != nil {
				return 0, 0, err
			}
			grp, err := strconv.ParseUint(u.Gid, 10, 32)
			if err != nil {
				return 0, 0, err
			}
			return uint32(usr), uint32(grp), nil
		}
	}
	return 0, 0, fmt.Errorf("oracleUserUIDGID: current user's primary group (GID=%q) is not dba|oinstall (GID=%q)", u.Gid, gids)
}

// CreateListener create a new listener for the database.
func (s *Server) CreateListener(ctx context.Context, req *dbdpb.CreateListenerRequest) (*dbdpb.CreateListenerResponse, error) {
	domain := req.GetDbDomain()
	if req.GetDbDomain() != "" {
		domain = fmt.Sprintf(".%s", req.GetDbDomain())
	}
	// default CDB service name is <CDB Name>.<domain>
	cdbServiceName := req.GetDatabaseName() + domain
	if req.GetCdbServiceName() != "" {
		cdbServiceName = req.GetCdbServiceName()
	}
	uid, gid, err := oracleUserUIDGID(true)
	if err != nil {
		return nil, fmt.Errorf("initDBListeners: get uid gid failed: %v", err)
	}
	l := &provision.ListenerInput{
		DatabaseName:   req.DatabaseName,
		DatabaseBase:   consts.OracleBase,
		DatabaseHome:   s.databaseHome,
		DatabaseHost:   s.hostName,
		DBDomain:       domain,
		CDBServiceName: cdbServiceName,
	}

	if !req.GetExcludePdb() {
		pdbNames, err := s.fetchPDBNames(ctx)
		if err != nil {
			return nil, err
		}
		l.PluggableDatabaseNames = pdbNames
	}

	lType := consts.SECURE
	lDir := filepath.Join(listenerDir, lType)
	listenerFileContent, tnsFileContent, sqlNetContent, err := provision.LoadTemplateListener(l, lType, fmt.Sprint(req.Port), req.Protocol)
	if err != nil {
		return &dbdpb.CreateListenerResponse{}, fmt.Errorf("initDBListeners: loading template for listener %q failed: %v", req.DatabaseName, err)
	}

	if err != nil {
		return nil, fmt.Errorf("initDBListeners: error while fetching uid gid: %v", err)
	}
	if err := provision.MakeDirs(ctx, []string{lDir}, uid, gid); err != nil {
		return nil, fmt.Errorf("initDBListeners: making a listener directory %q failed: %v", lDir, err)
	}

	// Prepare listener.ora.
	if err := ioutil.WriteFile(filepath.Join(lDir, "listener.ora"), []byte(listenerFileContent), 0600); err != nil {
		return nil, fmt.Errorf("initDBListeners: creating a listener.ora file failed: %v", err)
	}

	// Prepare sqlnet.ora.
	if err := ioutil.WriteFile(filepath.Join(lDir, "sqlnet.ora"), []byte(sqlNetContent), 0600); err != nil {
		return nil, fmt.Errorf("initDBListeners: unable to write sqlnet: %v", err)
	}

	// Prepare tnsnames.ora.
	if err := ioutil.WriteFile(filepath.Join(lDir, "tnsnames.ora"), []byte(tnsFileContent), 0600); err != nil {
		return nil, fmt.Errorf("initDBListeners: creating a tnsnames.ora file failed: %v", err)
	}

	if _, err := s.BounceListener(ctx, &dbdpb.BounceListenerRequest{
		Operation:    dbdpb.BounceListenerRequest_STOP,
		ListenerName: lType,
		TnsAdmin:     lDir,
	}); err != nil {
		klog.ErrorS(err, "Listener stop failed", "name", lType, "lDir", lDir)
	}

	if _, err := s.BounceListener(ctx, &dbdpb.BounceListenerRequest{
		Operation:    dbdpb.BounceListenerRequest_START,
		ListenerName: lType,
		TnsAdmin:     lDir,
	}); err != nil {
		return nil, fmt.Errorf("listener %s startup failed: %s, %v", lType, lDir, err)
	}

	return &dbdpb.CreateListenerResponse{}, nil
}

func (s *Server) fetchPDBNames(ctx context.Context) ([]string, error) {
	sqlResp, err := s.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{consts.ListPluggableDatabaseExcludeSeedSQL},
		Suppress: false,
	})

	if err != nil {
		return nil, fmt.Errorf("BootstrapTask: query pdb names failed: %v", err)
	}

	pdbNames := sqlResp.GetMsg()
	knownPDBs := make([]string, len(pdbNames))
	for i, msg := range pdbNames {
		row := make(map[string]string)
		if err := json.Unmarshal([]byte(msg), &row); err != nil {
			return knownPDBs, err
		}
		if name, ok := row["PDB_NAME"]; ok {
			knownPDBs[i] = name
		}
	}
	klog.InfoS("BootstrapTask: Found known pdbs", "knownPDBs", knownPDBs)
	return knownPDBs, nil
}

// FileExists is used to check an existence of a file (e.g. useful for provisioning).
func (s *Server) FileExists(ctx context.Context, req *dbdpb.FileExistsRequest) (*dbdpb.FileExistsResponse, error) {
	host, err := os.Hostname()
	if err != nil {
		return &dbdpb.FileExistsResponse{}, fmt.Errorf("dbdaemon/FileExists: failed to get host name: %v", err)
	}

	file := req.GetName()

	if _, err := os.Stat(file); err == nil {
		klog.InfoS("dbdaemon/FileExists", "requested file", file, "result", "found")
		return &dbdpb.FileExistsResponse{Exists: true}, nil
	}

	if os.IsNotExist(err) {
		klog.InfoS("dbdaemon/FileExists", "requested file", file, "on host", host, "result", "NOT found")
		return &dbdpb.FileExistsResponse{Exists: false}, nil
	}

	// Something is wrong, return error.
	klog.Errorf("dbdaemon/FileExists: failed to determine the status of a requested file %q on host %q: %v", file, host, err)

	return &dbdpb.FileExistsResponse{}, err
}

// CreateDirs RPC call to create directories along with any necessary parents.
func (s *Server) CreateDirs(ctx context.Context, req *dbdpb.CreateDirsRequest) (*dbdpb.CreateDirsResponse, error) {
	for _, dirInfo := range req.GetDirs() {
		if err := os.MkdirAll(dirInfo.GetPath(), os.FileMode(dirInfo.GetPerm())); err != nil {
			return nil, fmt.Errorf("dbdaemon/CreateDirs failed on dir %s: %v", dirInfo.GetPath(), err)
		}
	}

	return &dbdpb.CreateDirsResponse{}, nil
}

// ReadDir RPC call to read the directory named by path and returns Fileinfos for the path and children.
func (s *Server) ReadDir(ctx context.Context, req *dbdpb.ReadDirRequest) (*dbdpb.ReadDirResponse, error) {
	if !strings.HasPrefix(req.GetPath(), "/") {
		return nil, fmt.Errorf("dbdaemon/ReadDir failed to read %v, only accept absolute path", req.GetPath())
	}
	currFileInfo, err := os.Stat(req.GetPath())
	if err != nil {
		return nil, fmt.Errorf("dbdaemon/ReadDir os.Stat(%v) failed: %v ", req.GetPath(), err)
	}
	rpcCurrFileInfo, err := convertToRpcFileInfo(currFileInfo, req.GetPath(), req.GetReadFileContent())
	if err != nil {
		return nil, fmt.Errorf("dbdaemon/ReadDir failed: %v ", err)
	}
	resp := &dbdpb.ReadDirResponse{
		CurrPath: rpcCurrFileInfo,
	}

	if !currFileInfo.IsDir() {
		// for a file, just return its fileInfo
		return resp, nil
	}

	if req.GetRecursive() {
		if err := filepath.Walk(req.GetPath(), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				// stop walking if we see any error.
				return fmt.Errorf("visit %v, %v failed: %v", path, info, err)
			}
			if len(resp.SubPaths) >= maxWalkFiles {
				return fmt.Errorf("visited more than %v files, try reduce the dir scope", maxWalkFiles)
			}
			if path == req.GetPath() {
				return nil
			}
			rpcInfo, err := convertToRpcFileInfo(info, path, req.GetReadFileContent())
			if err != nil {
				return fmt.Errorf("visit %v, %v failed: %v ", info, path, err)
			}
			resp.SubPaths = append(resp.SubPaths, rpcInfo)
			return nil
		}); err != nil {
			return nil, fmt.Errorf("dbdaemon/ReadDir filepath.Walk(%v) failed: %v ", req.GetPath(), err)
		}
	} else {
		subFileInfos, err := ioutil.ReadDir(req.GetPath())
		if err != nil {
			return nil, fmt.Errorf("dbdaemon/ReadDir ioutil.ReadDir(%v) failed: %v ", req.GetPath(), err)
		}
		for _, info := range subFileInfos {
			rpcInfo, err := convertToRpcFileInfo(info, filepath.Join(req.GetPath(), info.Name()), req.GetReadFileContent())
			if err != nil {
				return nil, fmt.Errorf("dbdaemon/ReadDir failed: %v ", err)
			}
			resp.SubPaths = append(resp.SubPaths, rpcInfo)
		}
	}

	return resp, nil
}

func convertToRpcFileInfo(info os.FileInfo, absPath string, readContent bool) (*dbdpb.ReadDirResponse_FileInfo, error) {
	timestampProto, err := ptypes.TimestampProto(info.ModTime())
	if err != nil {
		return nil, fmt.Errorf("convertToRpcFileInfo(%v) failed: %v", info, err)
	}

	var content []byte
	if readContent && !info.IsDir() {
		content, err = ioutil.ReadFile(absPath)
		if err != nil {
			return nil, fmt.Errorf("convertToRpcFileInfo(%v) failed reading file: %v", info, err)
		}
	}

	return &dbdpb.ReadDirResponse_FileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    uint32(info.Mode()),
		ModTime: timestampProto,
		IsDir:   info.IsDir(),
		AbsPath: absPath,
		Content: string(content),
	}, nil
}

// DeleteDir removes path and any children it contains.
func (s *Server) DeleteDir(ctx context.Context, req *dbdpb.DeleteDirRequest) (*dbdpb.DeleteDirResponse, error) {

	removeFun := os.Remove
	if req.GetForce() {
		removeFun = os.RemoveAll
	}
	if err := removeFun(req.GetPath()); err != nil {
		return nil, fmt.Errorf("dbdaemon/DeleteDir(%v) failed: %v", req, err)
	}
	return &dbdpb.DeleteDirResponse{}, nil
}

// BackupConfigFile converts the binary spfile to human readable pfile and
// creates a snapshot copy named pfile.lkws (lkws -> last known working state).
// This file will be used for recovery in the event of parameter update workflow
// failure due to bad static parameters.
func (s *Server) BackupConfigFile(ctx context.Context, cdbName string) error {
	configDir := fmt.Sprintf(consts.ConfigDir, consts.DataMount, cdbName)
	backupPFileLoc := fmt.Sprintf("%s/%s", configDir, "pfile.lkws")
	klog.InfoS("dbdaemon/BackupConfigFile: backup config file", "backupPFileLoc", backupPFileLoc)

	_, err := s.runSQLPlusHelper(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{fmt.Sprintf("create pfile='%s' from spfile", backupPFileLoc)}}, false)
	if err != nil {
		klog.InfoS("dbdaemon/BackupConfigFile: error while backing up config file", "err", err)
		return fmt.Errorf("BackupConfigFile: failed to create pfile due to error: %v", err)
	}
	klog.InfoS("dbdaemon/BackupConfigFile: Successfully backed up config file")
	return nil
}

// RecoverConfigFile generates the binary spfile from the human readable backup pfile
func (s *Server) RecoverConfigFile(ctx context.Context, req *dbdpb.RecoverConfigFileRequest) (*dbdpb.RecoverConfigFileResponse, error) {
	configDir := fmt.Sprintf(consts.ConfigDir, consts.DataMount, req.GetCdbName())
	backupPFileLoc := fmt.Sprintf("%s/%s", configDir, "pfile.lkws")
	spFileLoc := fmt.Sprintf("%s/%s", configDir, fmt.Sprintf("spfile%s.ora", req.CdbName))

	klog.InfoS("dbdaemon/RecoverConfigFile: recover config file", "backupPFileLoc", backupPFileLoc, "spFileLoc", spFileLoc)

	_, err := s.runSQLPlusHelper(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{fmt.Sprintf("create spfile='%s' from pfile='%s'", spFileLoc, backupPFileLoc)}}, false)
	if err != nil {
		klog.InfoS("dbdaemon/RecoverConfigFile: error while recovering config file", "err", err)
		return nil, fmt.Errorf("dbdaemon/RecoverConfigFile: error while recovering config file: %v", err)
	}
	klog.InfoS("dbdaemon/RecoverConfigFile: Successfully recovering config file")
	return &dbdpb.RecoverConfigFileResponse{}, nil
}

// New creates a new dbdaemon server.
func New(ctx context.Context, cdbNameFromYaml string) (*Server, error) {
	klog.InfoS("dbdaemon/New: Dialing dbdaemon proxy")
	conn, err := common.DatabaseDaemonDialSocket(ctx, consts.ProxyDomainSocketFile, grpc.WithBlock())
	if err != nil {
		return nil, fmt.Errorf("failed to dial to database daemon: %v", err)
	}
	klog.InfoS("dbdaemon/New: Successfully connected to dbdaemon proxy")

	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("failed to get hostname: %v", err)
	}

	s := &Server{
		hostName:       hostname,
		database:       &DB{},
		osUtil:         &osUtilImpl{},
		databaseSid:    &syncState{},
		dbdClient:      dbdpb.NewDatabaseDaemonProxyClient(conn),
		dbdClientClose: conn.Close,
		lroServer:      lro.NewServer(ctx),
		syncJobs:       &syncJobs{},
		gcsUtil:        &util.GCSUtilImpl{},
	}

	oracleHome := os.Getenv("ORACLE_HOME")
	if err := setEnvNew(s, oracleHome, cdbNameFromYaml); err != nil {
		return nil, fmt.Errorf("failed to setup environment: %v", err)
	}
	return s, nil
}

// DownloadDirectoryFromGCS downloads objects from GCS bucket using prefix
func (s *Server) DownloadDirectoryFromGCS(ctx context.Context, req *dbdpb.DownloadDirectoryFromGCSRequest) (*dbdpb.DownloadDirectoryFromGCSResponse, error) {

	klog.Infof("dbdaemon/DownloadDirectoryFromGCS: req %v", req)
	bucket, prefix, err := s.gcsUtil.SplitURI(req.GcsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse gcs path %s", err)
	}

	if req.GetAccessPermissionCheck() {
		klog.Info("dbdaemon/downloadDirectoryFromGCS: verify the access permission of the given GCS path")
	} else {
		klog.Infof("dbdaemon/downloadDirectoryFromGCS: destination path is %s", req.GetLocalPath())
	}

	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage.NewClient: %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(ctx, time.Second*3600)
	defer cancel()
	it := client.Bucket(bucket).Objects(ctx, &storage.Query{
		Prefix: prefix,
	})
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("Bucket(%q).Objects(): %v", bucket, err)
		}

		if req.GetAccessPermissionCheck() {
			reader, err := client.Bucket(bucket).Object(attrs.Name).NewRangeReader(ctx, 0, 1)
			if err != nil {
				return nil, fmt.Errorf("failed to read URL %s: %v", attrs.Name, err)
			}
			reader.Close()
		} else {
			if err := s.downloadFile(ctx, client, bucket, attrs.Name, prefix, req.GetLocalPath()); err != nil {
				return nil, fmt.Errorf("failed to download file %s", err)
			}
		}

	}
	return &dbdpb.DownloadDirectoryFromGCSResponse{}, nil
}

// FetchServiceImageMetaData fetches the image metadata via the dbdaemon proxy.
func (s *Server) FetchServiceImageMetaData(ctx context.Context, req *dbdpb.FetchServiceImageMetaDataRequest) (*dbdpb.FetchServiceImageMetaDataResponse, error) {
	proxyResponse, err := s.dbdClient.ProxyFetchServiceImageMetaData(ctx, &dbdpb.ProxyFetchServiceImageMetaDataRequest{})
	if err != nil {
		return &dbdpb.FetchServiceImageMetaDataResponse{}, err
	}
	return &dbdpb.FetchServiceImageMetaDataResponse{Version: proxyResponse.Version, CdbName: proxyResponse.CdbName, OracleHome: proxyResponse.OracleHome, SeededImage: proxyResponse.SeededImage}, nil
}

func (s *Server) downloadFile(ctx context.Context, c *storage.Client, bucket, gcsPath, baseDir, dest string) error {
	reader, err := c.Bucket(bucket).Object(gcsPath).NewReader(ctx)
	if err != nil {
		return fmt.Errorf("failed to read URL %s: %v", gcsPath, err)
	}
	defer reader.Close()

	relPath, err := filepath.Rel(baseDir, gcsPath)
	if err != nil {
		return fmt.Errorf("failed to parse relPath for gcsPath %s", gcsPath)
	}

	f := filepath.Join(dest, relPath)
	start := time.Now()
	if err := s.osUtil.createFile(f, reader); err != nil {
		return fmt.Errorf("failed to createFile for file %s, err %s", f, err)
	}
	end := time.Now()
	rate := float64(reader.Attrs.Size) / (end.Sub(start).Seconds())
	klog.InfoS("dbdaemon/downloadFile:", "downloaded", f, "throughput", fmt.Sprintf("(%f MB/s)", rate/1024/1024))
	return nil
}

// bootstrapDatabase invokes init_oracle on dbdaemon_proxy to perform bootstrap tasks for seeded image
func (s *Server) bootstrapDatabase(ctx context.Context, req *dbdpb.BootstrapDatabaseRequest) (*dbdpb.BootstrapDatabaseResponse, error) {
	var pga, sga int
	if requestedMem, err := controllers.RequestedMemoryInMi(); err == nil {
		klog.Info("Database requested memory in Mi", "memory", requestedMem)
		pga = requestedMem / 8
		sga = requestedMem / 2
	} else {
		pga = consts.DefaultPGAMB
		sga = consts.DefaultSGAMB
	}

	if _, err := s.dbdClient.ProxyRunInitOracle(ctx, &dbdpb.ProxyRunInitOracleRequest{
		Params: []string{
			fmt.Sprintf("--pga=%d", pga),
			fmt.Sprintf("--sga=%d", sga),
			fmt.Sprintf("--cdb_name=%s", req.GetCdbName()),
			fmt.Sprintf("--db_domain=%s", req.GetDbDomain()),
			"--logtostderr=true",
		},
	}); err != nil {
		klog.InfoS("dbdaemon/BootstrapDatabase: error while run init_oracle: err", "err", err)
		return nil, fmt.Errorf("dbdaemon/BootstrapDatabase: failed to bootstrap database due to: %v", err)
	}
	klog.InfoS("dbdaemon/BootstrapDatabase: bootstrap database successful")

	return &dbdpb.BootstrapDatabaseResponse{}, nil
}

func (s *Server) BootstrapDatabaseAsync(ctx context.Context, req *dbdpb.BootstrapDatabaseAsyncRequest) (*lropb.Operation, error) {
	job, err := lro.CreateAndRunLROJobWithID(ctx, req.GetLroInput().GetOperationId(), "BootstrapDatabase", s.lroServer,
		func(ctx context.Context) (proto.Message, error) {
			return s.bootstrapDatabase(ctx, req.SyncRequest)
		})

	if err != nil {
		klog.ErrorS(err, "dbdaemon/BootstrapDatabaseAsync failed to create an LRO job", "request", req)
		return nil, err
	}

	return &lropb.Operation{Name: job.ID(), Done: false}, nil
}

func (s *Server) SetDnfsState(ctx context.Context, req *dbdpb.SetDnfsStateRequest) (*dbdpb.SetDnfsStateResponse, error) {
	if _, err := s.dbdClient.SetDnfsState(ctx, &dbdpb.SetDnfsStateRequest{Enable: req.Enable}); err != nil {
		return nil, err
	}

	return &dbdpb.SetDnfsStateResponse{}, nil
}
