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

// Package dbdaemonproxy provides access to the database container.
// From the security standpoint only the following requests are honored:
//  - only requests from a localhost
//  - only requests against predefined database and listener(s)
//  - only for tightly controlled commands
//
// All requests are to be logged and audited.
//
// Only New and CheckDatabaseState functions of this package can be called
// at the instance (aka CDB) provisioning time. The rest of the functions
// are expected to be called only when a database (aka PDB) is provisioned.
package dbdaemonproxy

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/godror/godror" // Register database/sql driver
	"k8s.io/klog/v2"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/database/common"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/database/provision"
)

// Override library functions for the benefit of unit tests.
var (
	lsnrctl = func(databaseHome string) string {
		return filepath.Join(databaseHome, "bin", "lsnrctl")
	}
	rman = func(databaseHome string) string {
		return filepath.Join(databaseHome, "bin", "rman")
	}
	orapwd = func(databaseHome string) string {
		return filepath.Join(databaseHome, "bin", "orapwd")
	}
	dbca = func(databaseHome string) string {
		return filepath.Join(databaseHome, "bin", "dbca")
	}
	nid = func(databaseHome string) string {
		return filepath.Join(databaseHome, "bin", "nid")
	}
	sqlOpen = func(driverName, dataSourceName string) (database, error) {
		return sql.Open(driverName, dataSourceName)
	}
	godrorDriverConn = func(ctx context.Context, ex godror.Execer) (conn, error) {
		return godror.DriverConn(ctx, ex)
	}
)

// osUtil was defined for tests.
type osUtil interface {
	runCommand(bin string, params []string) error
}

type osUtilImpl struct {
}

func (o *osUtilImpl) runCommand(bin string, params []string) error {
	ohome := os.Getenv("ORACLE_HOME")
	klog.InfoS("executing command with args", "cmd", bin, "params", params, "ORACLE_SID", os.Getenv("ORACLE_SID"), "ORACLE_HOME", ohome, "TNS_ADMIN", os.Getenv("TNS_ADMIN"))
	switch bin {
	case lsnrctl(ohome), rman(ohome), orapwd(ohome), dbca(ohome), nid(ohome):
	default:
		return fmt.Errorf("command %q is not supported", bin)
	}
	cmd := exec.Command(bin)
	cmd.Args = append(cmd.Args, params...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("command %q failed at path %q with args %v: %v", bin, cmd.Path, cmd.Args, err)
	}
	return nil
}

// database defines the sql.DB APIs, which will be used in this package
type database interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	Close() error
}

// conn defines the godror.Conn APIs, which will be used in this package
type conn interface {
	Startup(godror.StartupMode) error
	Shutdown(godror.ShutdownMode) error
}

// Server holds a database config.
type Server struct {
	*dbdpb.UnimplementedDatabaseDaemonProxyServer
	hostName     string
	databaseSid  *syncState
	databaseHome string
	pdbConnStr   string
	osUtil       osUtil
	version      string
}

func (s Server) String() string {
	pdbConnStr := s.pdbConnStr
	if pdbConnStr != "" {
		pdbConnStr = "<REDACTED>"
	}
	return fmt.Sprintf("{hostName=%q, databaseSid=%+v, databaseHome=%q, pdbConnStr=%q}", s.hostName, s.databaseSid, s.databaseHome, pdbConnStr)
}

type syncState struct {
	sync.RWMutex
	val string
}

// shutdownDatabase performs a database shutdown in a requested <mode>.
// It always connects to the local database.
// Set ORACLE_HOME and ORACLE_SID in the env to control the target database.
// A caller may decide to ignore ORA-1034 and just log a warning
// if a database has already been down (or raise an error if appropriate)..
func (s *Server) shutdownDatabase(ctx context.Context, mode godror.ShutdownMode) error {
	// Consider allowing PRELIM mode connections for SHUTDOWN ABORT mode.
	// This is useful when the server has maxed out on connections.
	db, err := sqlOpen("godror", "oracle://?sysdba=1")
	if err != nil {
		klog.ErrorS(err, "dbdaemon/shutdownDatabase: failed to connect to a database")
		return err
	}
	defer db.Close()

	oraDB, err := godrorDriverConn(ctx, db)
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

	_, err = db.ExecContext(ctx, "alter database close normal")
	if err != nil && strings.Contains(err.Error(), "ORA-01507:") {
		klog.InfoS("dbdaemon/shutdownDatabase: database is already closed", "err", err)
		err = nil
	}
	if err != nil {
		return err
	}

	_, err = db.ExecContext(ctx, "alter database dismount")
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
func (s *Server) startupDatabase(ctx context.Context, mode godror.StartupMode, state string) error {
	// To startup a shutdown database, open a prelim connection.
	db, err := sqlOpen("godror", "oracle://?sysdba=1&prelim=1")
	if err != nil {
		return err
	}
	defer db.Close()

	oraDB, err := godrorDriverConn(ctx, db)
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
	db2, err := sqlOpen("godror", "oracle://?sysdba=1")
	if err != nil {
		return err
	}
	defer db2.Close()

	if _, err := db2.ExecContext(ctx, "alter database mount"); err != nil {
		return err
	}
	if strings.ToLower(state) == "mount" {
		return nil
	}
	_, err = db2.ExecContext(ctx, "alter database open")
	return err
}

// BounceDatabase is a Database Daemon method to start or stop a database.
func (s *Server) BounceDatabase(ctx context.Context, req *dbdpb.BounceDatabaseRequest) (*dbdpb.BounceDatabaseResponse, error) {
	klog.InfoS("dbdaemon/BounceDatabase", "req", req, "serverObj", s)
	reqDatabaseName := req.GetDatabaseName()
	var ls dbdpb.DatabaseState
	var operation string
	// Allowed commands: startup [nomount|mount|open|force_nomount] or shutdown [immediate|transactional|abort].
	validStartupOptions := map[string]bool{"nomount": true, "mount": true, "open": true, "force_nomount": true}
	// validShutdownOptions keys should match shutdownEnumMap below to prevent nil.
	validShutdownOptions := map[string]bool{"immediate": true, "transactional": true, "abort": true}
	switch req.Operation {
	case dbdpb.BounceDatabaseRequest_STARTUP:
		ls = dbdpb.DatabaseState_READY
		if req.Option != "" && !validStartupOptions[req.Option] {
			e := []string{fmt.Sprintf("illegal option %q requested for operation %q", req.Option, req.Operation)}
			return &dbdpb.BounceDatabaseResponse{
				DatabaseState: dbdpb.DatabaseState_DATABASE_STATE_ERROR,
				ErrorMsg:      e,
			}, nil
		}
		operation = "startup"
	case dbdpb.BounceDatabaseRequest_SHUTDOWN:
		ls = dbdpb.DatabaseState_STOPPED
		if req.Option != "" && !validShutdownOptions[req.Option] {
			e := []string{fmt.Sprintf("illegal option %q requested for operation %q", req.Option, req.Operation)}
			return &dbdpb.BounceDatabaseResponse{
				DatabaseState: dbdpb.DatabaseState_DATABASE_STATE_ERROR,
				ErrorMsg:      e,
			}, nil
		}
		operation = "shutdown"
	default:
		return nil, fmt.Errorf("illegal operation requested: %q", req.Operation)
	}

	// Add lock to protect server state "databaseSid" and os env variable "ORACLE_SID".
	// When bouncing the DB, DB is not ready to run cmds or SQLs, it seems to be ok to block all other APIs for now.
	s.databaseSid.Lock()
	defer s.databaseSid.Unlock()

	// Sets env to bounce a database, needed for start and shutdown.
	os.Setenv("ORACLE_SID", reqDatabaseName)
	os.Setenv("ORACLE_HOME", s.databaseHome)

	var err error
	shutdownEnumMap := map[string]godror.ShutdownMode{
		"immediate":     godror.ShutdownImmediate,
		"transactional": godror.ShutdownTransactional,
		"abort":         godror.ShutdownAbort,
	}
	if operation == "shutdown" {
		// shutdownEnumMap keys should match validShutdownOptions above to prevent nil.
		err = s.shutdownDatabase(ctx, shutdownEnumMap[req.Option])
		if err != nil && strings.Contains(err.Error(), "ORA-01034: ORACLE not available") {
			klog.InfoS("dbdaemon/shutdownDatabase: database is already down", "err", err)
			err = nil
		}
	} else { // startup
		switch req.Option {
		case "force_nomount":
			err = s.startupDatabase(ctx, godror.StartupForce, "nomount")
		default:
			err = s.startupDatabase(ctx, godror.StartupDefault, req.Option)
		}
	}

	return &dbdpb.BounceDatabaseResponse{
		DatabaseState: ls,
		ErrorMsg:      nil,
	}, err
}

func (s *Server) runCommand(bin string, params []string) error {
	// Sets env to bounce a database|listener.
	os.Setenv("ORACLE_SID", s.databaseSid.val)
	os.Setenv("ORACLE_HOME", s.databaseHome)

	return s.osUtil.runCommand(bin, params)
}

// BounceListener is a Database Daemon method to start or stop a listener.
func (s *Server) BounceListener(_ context.Context, req *dbdpb.BounceListenerRequest) (*dbdpb.BounceListenerResponse, error) {
	klog.InfoS("dbdaemon/BounceListener", "req", req, "serverObj", s)

	var ls dbdpb.ListenerState
	var operation string
	switch req.Operation {
	case dbdpb.BounceListenerRequest_START:
		ls = dbdpb.ListenerState_UP
		operation = "start"
	case dbdpb.BounceListenerRequest_STOP:
		ls = dbdpb.ListenerState_DOWN
		operation = "stop"
	default:
		return nil, fmt.Errorf("illegal operation %q requested for listener %q", req.Operation, req.ListenerName)
	}

	// Add lock to protect server state "databaseSid" and os env variable "ORACLE_SID".
	s.databaseSid.RLock()
	defer s.databaseSid.RUnlock()

	os.Setenv("TNS_ADMIN", req.TnsAdmin)
	bin := lsnrctl(s.databaseHome)
	params := []string{operation, req.ListenerName}
	if err := s.runCommand(bin, params); err != nil {
		return nil, fmt.Errorf(fmt.Sprintf("a listener %q command %q failed: %v", req.ListenerName, req.Operation, err))
	}

	klog.InfoS("dbdaemon/BounceListener done", "req", req)
	return &dbdpb.BounceListenerResponse{
		ListenerState: ls,
		ErrorMsg:      nil,
	}, nil
}

// ProxyRunDbca execute the command to create a database instance
func (s *Server) ProxyRunDbca(ctx context.Context, req *dbdpb.ProxyRunDbcaRequest) (*dbdpb.ProxyRunDbcaResponse, error) {
	if err := os.Setenv("ORACLE_HOME", req.GetOracleHome()); err != nil {
		return nil, fmt.Errorf("dbdaemon/ProxyRunDbca: set env ORACLE_HOME failed: %v", err)
	}
	s.databaseSid.Lock()
	defer s.databaseSid.Unlock()
	if err := s.osUtil.runCommand(dbca(req.GetOracleHome()), req.GetParams()); err != nil {
		return nil, fmt.Errorf("dbca cmd failed: %v", err)
	}

	klog.InfoS("proxy/ProxyRunDbca: Initializing environment for Oracle...")
	if err := initializeEnvironment(s, req.GetOracleHome(), req.GetDatabaseName()); err != nil {
		return nil, err
	}

	klog.InfoS("proxy/ProxyRunDbca: Moving Oracle config files...")
	if err := provision.MoveConfigFiles(req.GetOracleHome(), req.GetDatabaseName()); err != nil {
		return nil, err
	}

	klog.InfoS("proxy/ProxyRunDbca: Creating symlinks to Oracle config files...")
	if err := provision.RelinkConfigFiles(req.GetOracleHome(), req.GetDatabaseName()); err != nil {
		return nil, err
	}

	klog.InfoS("proxy/ProxyRunDbca: DONE")

	return &dbdpb.ProxyRunDbcaResponse{}, nil
}

// ProxyRunNID execute the command to rename a database instance
func (s *Server) ProxyRunNID(ctx context.Context, req *dbdpb.ProxyRunNIDRequest) (*dbdpb.ProxyRunNIDResponse, error) {
	s.databaseSid.Lock()
	defer s.databaseSid.Unlock()
	if err := s.osUtil.runCommand(nid(s.databaseHome), req.GetParams()); err != nil {
		return nil, fmt.Errorf("nid cmd failed: %v", err)
	}
	s.databaseSid.val = req.DestDbName
	// We need to regenerate the env file with the new db name
	if err := createDotEnv(s.databaseHome, s.version, s.databaseSid.val); err != nil {
		return nil, err
	}
	klog.InfoS("proxy/ProxyRunNID: DONE")

	return &dbdpb.ProxyRunNIDResponse{}, nil
}

// SetEnv moves/relink oracle config files
func (s *Server) SetEnv(ctx context.Context, req *dbdpb.SetEnvRequest) (*dbdpb.SetEnvResponse, error) {
	klog.InfoS("proxy/SetEnv", "req", req)
	oracleHome := req.GetOracleHome()
	cdbName := req.GetCdbName()
	spfile := req.GetSpfilePath()
	defaultSpfile := filepath.Join(fmt.Sprintf(consts.ConfigDir, consts.DataMount, cdbName), fmt.Sprintf("spfile%s.ora", cdbName))

	// move config files to default locations first
	if spfile != defaultSpfile {
		if err := provision.MoveFile(spfile, defaultSpfile); err != nil {
			return &dbdpb.SetEnvResponse{}, fmt.Errorf("Proxy/SetEnv: failed to move spfile to default location: %v", err)
		}
	}

	spfileLink := filepath.Join(oracleHome, "dbs", fmt.Sprintf("spfile%s.ora", cdbName))
	if _, err := os.Stat(spfileLink); err == nil {
		os.Remove(spfileLink)
	}
	if err := os.Symlink(defaultSpfile, spfileLink); err != nil {
		return &dbdpb.SetEnvResponse{}, fmt.Errorf("Proxy/SetEnv symlink creation failed for %s: %v", defaultSpfile, err)
	}
	return &dbdpb.SetEnvResponse{}, nil
}

//Sets Oracle specific environment variables and creates the .env file
func initializeEnvironment(s *Server, home string, dbName string) error {
	s.databaseHome = home
	s.databaseSid.val = dbName
	if err := os.Setenv("ORACLE_HOME", home); err != nil {
		return fmt.Errorf("dbdaemon/initializeEnvironment: set env ORACLE_HOME failed: %v", err)
	}
	if err := createDotEnv(home, s.version, dbName); err != nil {
		return err
	}
	return nil
}

func createDotEnv(dbHome, dbVersion, dbName string) error {
	dotEnvFileName := fmt.Sprintf("%s/%s.env", consts.OracleDir, dbName)
	dotEnvFile, err := os.Create(dotEnvFileName)
	if err != nil {
		return err
	}
	dotEnvFile.WriteString(fmt.Sprintf("export ORACLE_HOME=%s\n", dbHome))
	dotEnvFile.WriteString(fmt.Sprintf("ORACLE_BASE=%s\n", common.GetSourceOracleBase(dbVersion)))
	dotEnvFile.WriteString(fmt.Sprintf("export ORACLE_SID=%s\n", dbName))
	dotEnvFile.WriteString(fmt.Sprintf("export PATH=%s/bin:%s/OPatch:/usr/local/bin:/usr/local/sbin:/sbin:/bin:/usr/sbin:/usr/bin:/root/bin\n", dbHome, dbHome))
	dotEnvFile.WriteString(fmt.Sprintf("export LD_LIBRARY_PATH=%s/lib:/usr/lib\n", dbHome))
	return dotEnvFile.Close()
}

// New creates a new Database Daemon Server object.
// It first gets called on a CDB provisioning and at this time
// a PDB name is not known yet (to be supplied via a separate call).
func New(hostname, cdbNameFromYaml string) (*Server, error) {
	oracleHome, _, version, err := provision.FetchMetaDataFromImage(provision.MetaDataFile)
	s := &Server{hostName: hostname, osUtil: &osUtilImpl{}, databaseSid: &syncState{}, version: version}
	if err != nil {
		return nil, fmt.Errorf("error while fetching metadata from image: %v", err)
	}
	klog.Infof("Initializing environment for Oracle...")
	err = initializeEnvironment(s, oracleHome, cdbNameFromYaml)
	if err != nil {
		return nil, fmt.Errorf("an error occured while initializing the environment for Oracle: %v", err)
	}
	return s, nil
}
