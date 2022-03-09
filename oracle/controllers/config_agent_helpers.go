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

package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common/sql"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/database/provision"
	secretmanagerpb "google.golang.org/genproto/googleapis/cloud/secretmanager/v1"
	lropb "google.golang.org/genproto/googleapis/longrunning"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

const (
	version      = "12.2"
	pdbAdmin     = "GPDB_ADMIN"
	gsmSecretStr = "projects/%s/secrets/%s/versions/%s"
)

var (
	newGsmClient = func(ctx context.Context) (*secretmanager.Client, func() error, error) {
		client, err := secretmanager.NewClient(ctx)
		if err != nil {
			return nil, func() error { return nil }, err
		}
		return client, client.Close, nil
	}
)

// GetLROOperation returns LRO operation for the specified namespace instance and operation id.
func GetLROOperation(ctx context.Context, dbClientFactory DatabaseClientFactory, r client.Reader, id, namespace, instName string) (*lropb.Operation, error) {
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, err
	}
	defer closeConn()

	req := &lropb.GetOperationRequest{Name: id}
	return dbClient.GetOperation(ctx, req)
}

// DeleteLROOperation deletes LRO operation for the specified namespace instance and operation id.
func DeleteLROOperation(ctx context.Context, dbClientFactory DatabaseClientFactory, r client.Reader, id, namespace, instName string) error {
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return err
	}
	defer closeConn()

	_, err = dbClient.DeleteOperation(ctx, &lropb.DeleteOperationRequest{Name: id})
	return err
}

// Check for LRO job status
// Return (true, nil) if LRO is done without errors.
// Return (true, err) if LRO is done with an error.
// Return (false, nil) if LRO still in progress.
// Return (false, err) if other error occurred.
func IsLROOperationDone(ctx context.Context, dbClientFactory DatabaseClientFactory, r client.Reader, id, namespace, instName string) (bool, error) {
	operation, err := GetLROOperation(ctx, dbClientFactory, r, id, namespace, instName)
	if err != nil {
		return false, err
	}
	if !operation.GetDone() {
		return false, nil
	}

	// handle case when remote LRO completed unsuccessfully
	if operation.GetError() != nil {
		return true, fmt.Errorf("Operation failed with err: %s. %v", operation.GetError().GetMessage(), err)
	}

	return true, nil
}

type CreateCDBRequest struct {
	OracleHome       string
	Sid              string
	DbUniqueName     string
	CharacterSet     string
	MemoryPercent    int32
	AdditionalParams []string
	Version          string
	DbDomain         string
	LroInput         *LROInput
}

type LROInput struct {
	OperationId string
}

// CreateCDB creates a CDB using dbca.
func CreateCDB(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req CreateCDBRequest) (*lropb.Operation, error) {
	klog.InfoS("CreateCDB", "namespace", namespace, "instName", instName, "sid", req.Sid)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("CreateCDB: failed to create database daemon dbdClient: %v", err)
	}
	defer closeConn()

	lro, err := dbClient.CreateCDBAsync(ctx, &dbdpb.CreateCDBAsyncRequest{
		SyncRequest: &dbdpb.CreateCDBRequest{
			OracleHome:       req.OracleHome,
			DatabaseName:     req.Sid,
			Version:          req.Version,
			DbUniqueName:     req.DbUniqueName,
			CharacterSet:     req.CharacterSet,
			MemoryPercent:    req.MemoryPercent,
			AdditionalParams: req.AdditionalParams,
			DbDomain:         req.DbDomain,
		},
		LroInput: &dbdpb.LROInput{OperationId: req.LroInput.OperationId},
	})
	if err != nil {
		return nil, fmt.Errorf("CreateCDB: failed to create CDB: %v", err)
	}

	klog.InfoS("CreateCDB successfully completed")
	return lro, nil
}

type BounceDatabaseRequest struct {
	Sid string
	// avoid_config_backup: by default we backup the config except for scenarios
	// when it isn't possible (like bootstrapping)
	AvoidConfigBackup bool
}

// BounceDatabase shutdown/startup the database as requested.
func BounceDatabase(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req BounceDatabaseRequest) error {
	klog.InfoS("BounceDatabase", "namespace", namespace, "instName", instName, "sid", req.Sid)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return err
	}
	defer closeConn()

	klog.InfoS("BounceDatabase", "client", dbClient)
	_, err = dbClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
		DatabaseName: req.Sid,
		Option:       "immediate",
	})
	if err != nil {
		return fmt.Errorf("BounceDatabase: error while shutting db: %v", err)
	}
	klog.InfoS("BounceDatabase: shutdown successful")

	_, err = dbClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:         dbdpb.BounceDatabaseRequest_STARTUP,
		DatabaseName:      req.Sid,
		AvoidConfigBackup: req.AvoidConfigBackup,
	})
	if err != nil {
		return fmt.Errorf("configagent/BounceDatabase: error while starting db: %v", err)
	}
	klog.InfoS("BounceDatabase: startup successful")
	return err
}

func RecoverConfigFile(ctx context.Context, dbClientFactory DatabaseClientFactory, r client.Reader, namespace, instName, cdbName string) error {
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return err
	}
	defer closeConn()

	if _, err := dbClient.RecoverConfigFile(ctx, &dbdpb.RecoverConfigFileRequest{CdbName: cdbName}); err != nil {
		klog.InfoS("configagent/RecoverConfigFile: error while recovering config file: err", "err", err)
		return fmt.Errorf("configagent/RecoverConfigFile: failed to recover config file due to: %v", err)
	}
	klog.InfoS("configagent/RecoverConfigFile: config file backup successful")
	return err
}

type CreateDatabaseRequest struct {
	CdbName string
	Name    string
	// only being used for plaintext password scenario.
	// GSM doesn't use this field.
	Password                  string
	DbDomain                  string
	AdminPasswordGsmSecretRef *GsmSecretReference
	// only being used for plaintext password scenario.
	// GSM doesn't use this field.
	LastPassword string
}

type CreateDatabaseResponse struct {
	Status       string
	ErrorMessage string
}

// CreateDatabase creates PDB as requested.
func CreateDatabase(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req CreateDatabaseRequest) (string, error) {
	klog.InfoS("CreateDatabase", "namespace", namespace, "instName", instName, "cdbName", req.CdbName, "pdbName", req.Name)

	var pwd string
	var err error

	toUpdatePlaintextAdminPwd := req.Password != "" && req.Password != req.LastPassword
	if toUpdatePlaintextAdminPwd {
		pwd = req.Password
	}

	toUpdateGsmAdminPwd := req.AdminPasswordGsmSecretRef != nil && (req.AdminPasswordGsmSecretRef.Version != req.AdminPasswordGsmSecretRef.LastVersion || req.AdminPasswordGsmSecretRef.Version == "latest")
	if toUpdateGsmAdminPwd {
		pwd, err = AccessSecretVersionFunc(ctx, fmt.Sprintf(gsmSecretStr, req.AdminPasswordGsmSecretRef.ProjectId, req.AdminPasswordGsmSecretRef.SecretId, req.AdminPasswordGsmSecretRef.Version))
		if err != nil {
			return "", fmt.Errorf("CreateDatabase: failed to retrieve secret from Google Secret Manager: %v", err)
		}
	}

	p, err := buildPDB(req.CdbName, req.Name, pwd, version, consts.ListenerNames, true)
	if err != nil {
		return "", err
	}

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return "", fmt.Errorf("CreateDatabase: failed to create database daemon dbdClient: %v", err)
	}
	defer closeConn()
	klog.InfoS("CreateDatabase", "dbClient", dbClient)

	_, err = dbClient.CheckDatabaseState(ctx, &dbdpb.CheckDatabaseStateRequest{IsCdb: true, DatabaseName: req.CdbName, DbDomain: req.DbDomain})
	if err != nil {
		return "", fmt.Errorf("CreateDatabase: failed to check a CDB state: %v", err)
	}
	klog.InfoS("CreateDatabase: pre-flight check#1: CDB is up and running")

	pdbCheckCmd := []string{fmt.Sprintf("select open_mode, restricted from v$pdbs where name = '%s'", sql.StringParam(p.pluggableDatabaseName))}
	resp, err := dbClient.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: pdbCheckCmd, Suppress: false})
	if err != nil {
		return "", fmt.Errorf("CreateDatabase: failed to check if a PDB called %s already exists: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("CreateDatabase pre-flight check#2", "pdb", p.pluggableDatabaseName, "resp", resp)

	if resp != nil && resp.Msg != nil {
		if toUpdateGsmAdminPwd || toUpdatePlaintextAdminPwd {
			sqls := append([]string{sql.QuerySetSessionContainer(p.pluggableDatabaseName)}, []string{sql.QueryAlterUser(pdbAdmin, pwd)}...)
			if _, err := dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
				Commands: sqls,
			}); err != nil {
				return "", fmt.Errorf("failed to alter user %s: %v", pdbAdmin, err)
			}
			klog.InfoS("CreateDatabase update pdb admin user succeeded", "user", pdbAdmin)
			return "AdminUserSyncCompleted", nil
		}
		klog.InfoS("CreateDatabase pre-flight check#2", "pdb", p.pluggableDatabaseName, "respMsg", resp.Msg)
		return "AlreadyExists", nil
	}
	klog.InfoS("CreateDatabase pre-flight check#2: pdb doesn't exist, proceeding to create", "pdb", p.pluggableDatabaseName)

	cdbDir := fmt.Sprintf(consts.DataDir, consts.DataMount, req.CdbName)
	pdbDir := filepath.Join(cdbDir, strings.ToUpper(req.Name))
	toCreate := []string{
		fmt.Sprintf("%s/data", pdbDir),
		fmt.Sprintf("%s/%s", pdbDir, consts.DpdumpDir.Linux),
		fmt.Sprintf("%s/rman", consts.OracleBase),
	}

	var dirs []*dbdpb.CreateDirsRequest_DirInfo
	for _, d := range toCreate {
		dirs = append(dirs, &dbdpb.CreateDirsRequest_DirInfo{
			Path: d,
			Perm: 0760,
		})
	}

	if _, err := dbClient.CreateDirs(ctx, &dbdpb.CreateDirsRequest{Dirs: dirs}); err != nil {
		return "", fmt.Errorf("failed to create PDB dirs: %v", err)
	}

	pdbCmd := []string{sql.QueryCreatePDB(p.pluggableDatabaseName, pdbAdmin, p.pluggableAdminPasswd, p.dataFilesDir, p.defaultTablespace, p.defaultTablespaceDatafile, p.fileConvertFrom, p.fileConvertTo)}
	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: pdbCmd, Suppress: false})
	if err != nil {
		return "", fmt.Errorf("CreateDatabase: failed to create a PDB %s: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("CreateDatabase create a PDB Done", "pdb", p.pluggableDatabaseName)

	pdbOpen := []string{fmt.Sprintf("alter pluggable database %s open read write", sql.MustBeObjectName(p.pluggableDatabaseName))}
	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: pdbOpen, Suppress: false})
	if err != nil {
		return "", fmt.Errorf("CreatePDBDatabase: PDB %s open failed: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("CreateDatabase PDB open", "pdb", p.pluggableDatabaseName)

	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{
		sql.QuerySetSessionContainer(p.pluggableDatabaseName),
		sql.QueryGrantPrivileges("create session, dba", pdbAdmin),
		sql.QueryGrantPrivileges("create session, resource, datapump_imp_full_database, datapump_exp_full_database, unlimited tablespace", consts.PDBLoaderUser),
	}, Suppress: false})
	if err != nil {
		// Until we have a proper error handling, just log an error here.
		klog.ErrorS(err, "CreateDatabase: failed to create a PDB_ADMIN user and/or PDB loader user")
	}
	klog.InfoS("CreateDatabase: created PDB_ADMIN and PDB Loader users")

	// Separate out the directory treatment for the ease of troubleshooting.
	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{
		sql.QuerySetSessionContainer(p.pluggableDatabaseName),
		sql.QueryCreateDir(consts.DpdumpDir.Oracle, filepath.Join(p.pathPrefix, consts.DpdumpDir.Linux)),
		sql.QueryGrantPrivileges(fmt.Sprintf("read,write on directory %s", consts.DpdumpDir.Oracle), consts.PDBLoaderUser),
	}, Suppress: false})
	if err != nil {
		klog.ErrorS(err, "CreateDatabase: failed to create a Data Pump directory", "datapumpDir", consts.DpdumpDir)
	}
	klog.InfoS("CreateDatabase: DONE", "pdb", p.pluggableDatabaseName)

	return "Ready", nil
}

type UsersChangedRequest struct {
	PdbName   string
	UserSpecs []*User
}

type UsersChangedResponse struct {
	Changed    bool
	Suppressed []*UsersChangedResponseSuppressed
}

type UsersChangedResponseSuppressed struct {
	SuppressType UsersChangedResponseType
	UserName     string
	// sql is the suppressed cmd which can update the user to the spec defined
	// state
	Sql string
}

type UsersChangedResponseType int32

const (
	UsersChangedResponse_UNKNOWN_TYPE UsersChangedResponseType = 0
	UsersChangedResponse_DELETE       UsersChangedResponseType = 1
	UsersChangedResponse_CREATE       UsersChangedResponseType = 2
)

// UsersChanged determines whether there is change on users (update/delete/create).
func UsersChanged(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req UsersChangedRequest) (*UsersChangedResponse, error) {
	klog.InfoS("UsersChanged", "namespace", namespace, "instName", instName, "pdbName", req.PdbName)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	us := newUsers(req.PdbName, req.UserSpecs)
	toCreate, toUpdate, toDelete, toUpdatePwd, err := us.diff(ctx, dbClient)
	if err != nil {
		return nil, fmt.Errorf("UsersChanged: failed to get difference between env and spec for users: %v", err)
	}
	var suppressed []*UsersChangedResponseSuppressed
	for _, du := range toDelete {
		suppressed = append(suppressed, &UsersChangedResponseSuppressed{
			SuppressType: UsersChangedResponse_DELETE,
			UserName:     du.userName,
			Sql:          du.delete(),
		})
	}
	for _, cu := range toCreate {
		if cu.newPassword == "" {
			suppressed = append(suppressed, &UsersChangedResponseSuppressed{
				SuppressType: UsersChangedResponse_CREATE,
				UserName:     cu.userName,
			})
		}
	}
	resp := &UsersChangedResponse{
		Changed:    len(toCreate) != 0 || len(toUpdate) != 0 || len(toUpdatePwd) != 0,
		Suppressed: suppressed,
	}
	klog.InfoS("UsersChanged: DONE", "resp", resp)
	return resp, nil
}

type UpdateUsersRequest struct {
	PdbName   string
	UserSpecs []*User
}

// UpdateUsers update/create users as requested.
func UpdateUsers(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req UpdateUsersRequest) error {
	klog.InfoS("UpdateUsers", "namespace", namespace, "instName", instName, "pdbName", req.PdbName)

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return fmt.Errorf("UpdateUsers: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	us := newUsers(req.PdbName, req.UserSpecs)
	toCreate, toUpdate, _, toUpdatePwd, err := us.diff(ctx, dbClient)
	if err != nil {
		return fmt.Errorf("UpdateUsers: failed to get difference between env and spec for users: %v", err)
	}
	foundErr := false
	for _, u := range toCreate {
		klog.InfoS("UpdateUsers", "creating user", u.userName)
		if err := u.create(ctx, dbClient); err != nil {
			klog.ErrorS(err, "failed to create user")
			foundErr = true
		}
	}

	for _, u := range toUpdate {
		klog.InfoS("UpdateUsers", "updating user", u.userName)
		// we found there is a scenario that role comes with privileges. For example
		// Grant dba role to a user will automatically give unlimited tablespace privilege.
		// Revoke dba role will automatically revoke  unlimited tablespace privilege.
		// thus user update will first update role and then update sys privi.
		if err := u.update(ctx, dbClient, us.databaseRoles); err != nil {
			klog.ErrorS(err, "failed to update user")
			foundErr = true
		}
	}

	for _, u := range toUpdatePwd {
		klog.InfoS("UpdateUsers", "updating user", u.userName)
		if err := u.updatePassword(ctx, dbClient); err != nil {
			klog.ErrorS(err, "failed to update user password")
			foundErr = true
		}
	}

	if foundErr {
		return errors.New("failed to update users")
	}
	klog.InfoS("UpdateUsers: DONE")
	return nil
}

// SetParameter sets database parameter as requested.
func SetParameter(ctx context.Context, dbClientFactory DatabaseClientFactory, r client.Reader, namespace, instName, key, value string) (bool, error) {
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return false, err
	}
	defer closeConn()

	// Fetch parameter type
	// The possible values are IMMEDIATE FALSE DEFERRED
	query := fmt.Sprintf("select issys_modifiable from v$parameter where name='%s'", sql.StringParam(key))
	paramType, err := fetchAndParseSingleResultQuery(ctx, dbClient, query)
	if err != nil {
		return false, fmt.Errorf("configagent/SetParameter: error while inferring parameter type: %v", err)
	}
	query = fmt.Sprintf("select type from v$parameter where name='%s'", sql.StringParam(key))
	paramDatatype, err := fetchAndParseSingleResultQuery(ctx, dbClient, query)
	if err != nil {
		return false, fmt.Errorf("configagent/SetParameter: error while inferring parameter data type: %v", err)
	}
	// string parameters need to be quoted,
	// those have type 2, see the link for the parameter types description
	// https://docs.oracle.com/database/121/REFRN/GUID-C86F3AB0-1191-447F-8EDF-4727D8693754.htm
	isStringParam := paramDatatype == "2"
	command, err := sql.QuerySetSystemParameterNoPanic(key, value, isStringParam)
	if err != nil {
		return false, fmt.Errorf("configagent/SetParameter: error constructing set parameter query: %v", err)
	}

	isStatic := false
	if paramType == "FALSE" {
		klog.InfoS("configagent/SetParameter", "parameter_type", "STATIC")
		command = fmt.Sprintf("%s scope=spfile", command)
		isStatic = true
	}

	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{command},
		Suppress: false,
	})
	if err != nil {
		return false, fmt.Errorf("configagent/SetParameter: error while executing parameter command: %q", command)
	}
	return isStatic, nil
}

// fetchAndParseSingleResultQuery is a utility method intended for running single result queries.
// It parses the single column JSON result-set (returned by runSQLPlus API) and returns a list.
func fetchAndParseSingleResultQuery(ctx context.Context, client dbdpb.DatabaseDaemonClient, query string) (string, error) {

	sqlRequest := &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{query},
		Suppress: false,
	}
	response, err := client.RunSQLPlusFormatted(ctx, sqlRequest)
	if err != nil {
		return "", fmt.Errorf("failed to run query %q; DSN: %q; error: %v", query, sqlRequest.GetDsn(), err)
	}
	result, err := parseSQLResponse(response)
	if err != nil {
		return "", fmt.Errorf("error while parsing query response: %q; error: %v", query, err)
	}

	var rows []string
	for _, row := range result {
		if len(row) != 1 {
			return "", fmt.Errorf("fetchAndParseSingleColumnMultiRowQueriesFromEM: # of cols returned by query != 1: %v", row)
		}
		for _, v := range row {
			rows = append(rows, v)
		}
	}
	return rows[0], nil
}

// parseSQLResponse parses the JSON result-set (returned by runSQLPlus API) and
// returns a list of rows with column-value mapping.
func parseSQLResponse(resp *dbdpb.RunCMDResponse) ([]map[string]string, error) {
	var rows []map[string]string
	for _, msg := range resp.GetMsg() {
		row := make(map[string]string)
		if err := json.Unmarshal([]byte(msg), &row); err != nil {
			return nil, fmt.Errorf("failed to parse %s: %v", msg, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

type CreateUsersRequest struct {
	CdbName        string
	PdbName        string
	CreateUsersCmd []string
	GrantPrivsCmd  []string
	DbDomain       string
	User           []*User
}

type User struct {
	Name string
	// only being used for plaintext password scenario.
	// GSM doesn't use this field.
	Password             string
	Privileges           []string
	PasswordGsmSecretRef *GsmSecretReference
	// only being used for plaintext password scenario.
	// GSM doesn't use this field.
	LastPassword string
}

type GsmSecretReference struct {
	ProjectId   string
	SecretId    string
	Version     string
	LastVersion string
}

// pdb represents a PDB database.
type pdb struct {
	containerDatabaseName     string
	dataFilesDir              string
	defaultTablespace         string
	defaultTablespaceDatafile string
	fileConvertFrom           string
	fileConvertTo             string
	hostName                  string
	listenerDir               string
	listeners                 map[string]*consts.Listener
	pathPrefix                string
	pluggableAdminPasswd      string
	pluggableDatabaseName     string
	skipUserCheck             bool
	version                   string
}

func buildPDB(cdbName, pdbName, pdbAdminPass, version string, listeners map[string]*consts.Listener, skipUserCheck bool) (*pdb, error) {
	// For consistency sake, keeping all PDB names uppercase.
	pdbName = strings.ToUpper(pdbName)
	host, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	return &pdb{
		pluggableDatabaseName:     pdbName,
		pluggableAdminPasswd:      pdbAdminPass,
		containerDatabaseName:     cdbName,
		dataFilesDir:              fmt.Sprintf(consts.PDBDataDir, consts.DataMount, cdbName, pdbName),
		defaultTablespace:         fmt.Sprintf("%s_USERS", pdbName),
		defaultTablespaceDatafile: fmt.Sprintf(consts.PDBDataDir+"/%s_users.dbf", consts.DataMount, cdbName, pdbName, strings.ToLower(pdbName)),
		pathPrefix:                fmt.Sprintf(consts.PDBPathPrefix, consts.DataMount, cdbName, pdbName),
		fileConvertFrom:           fmt.Sprintf(consts.PDBSeedDir, consts.DataMount, cdbName),
		fileConvertTo:             fmt.Sprintf(consts.PDBDataDir, consts.DataMount, cdbName, pdbName),
		listenerDir:               fmt.Sprintf(consts.ListenerDir, consts.DataMount),
		listeners:                 listeners,
		version:                   version,
		hostName:                  host,
		skipUserCheck:             skipUserCheck,
	}, nil
}

// CreateUsers creates users as requested.
func CreateUsers(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req CreateUsersRequest) (string, error) {
	// UsersChanged is called before this function by caller (db controller) to check if
	// the users requested are already existing.
	// Thus no duplicated list user check is performed here.
	klog.InfoS("CreateUsers", "namespace", namespace, "cdbName", req.CdbName, "pdbName", req.PdbName)

	p, err := buildPDB(req.CdbName, req.PdbName, "", version, consts.ListenerNames, true)
	if err != nil {
		return "", err
	}

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return "", fmt.Errorf("CreateUsers: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	klog.InfoS("CreateUsers", "client", dbClient)

	_, err = dbClient.CheckDatabaseState(ctx, &dbdpb.CheckDatabaseStateRequest{IsCdb: true, DatabaseName: req.CdbName, DbDomain: req.DbDomain})
	if err != nil {
		return "", fmt.Errorf("CreateUsers: failed to check a CDB state: %v", err)
	}
	klog.InfoS("CreateUsers: pre-flight check#: CDB is up and running")

	// Separate create users from grants to make troubleshooting easier.
	usersCmd := []string{sql.QuerySetSessionContainer(p.pluggableDatabaseName)}
	usersCmd = append(usersCmd, req.CreateUsersCmd...)
	for _, u := range req.User {
		if u.PasswordGsmSecretRef != nil && u.Name != "" {
			var pwd string
			pwd, err = AccessSecretVersionFunc(ctx, fmt.Sprintf(gsmSecretStr, u.PasswordGsmSecretRef.ProjectId, u.PasswordGsmSecretRef.SecretId, u.PasswordGsmSecretRef.Version))
			if err != nil {
				return "", fmt.Errorf("CreateUsers: failed to retrieve secret from Google Secret Manager: %v", err)
			}
			if _, err = sql.Identifier(pwd); err != nil {
				return "", fmt.Errorf("CreateUsers: Google Secret Manager contains an invalid password for user %q: %v", u.Name, err)
			}

			usersCmd = append(usersCmd, sql.QueryCreateUser(u.Name, pwd))
		}
	}
	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: usersCmd, Suppress: false})
	if err != nil {
		return "", fmt.Errorf("CreateUsers: failed to create users in a PDB %s: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("CreateUsers: create users in PDB DONE", "pdb", p.pluggableDatabaseName)

	privsCmd := []string{sql.QuerySetSessionContainer(p.pluggableDatabaseName)}
	privsCmd = append(privsCmd, req.GrantPrivsCmd...)
	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: privsCmd, Suppress: false})
	if err != nil {
		return "", fmt.Errorf("CreateUsers: failed to grant privileges in a PDB %s: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("CreateUsers: DONE", "pdb", p.pluggableDatabaseName)

	return "Ready", nil
}

// AccessSecretVersionFunc accesses the payload for the given secret version if one
// exists. The version can be a version number as a string (e.g. "5") or an
// alias (e.g. "latest").
var AccessSecretVersionFunc = func(ctx context.Context, name string) (string, error) {
	// Create the GSM client.
	client, closeConn, err := newGsmClient(ctx)
	if err != nil {
		return "", fmt.Errorf("configagent/AccessSecretVersionFunc: failed to create secretmanager client: %v", err)
	}
	defer closeConn()

	// Build the request.
	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: name,
	}

	// Call the API.
	result, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("configagent/AccessSecretVersionFunc: failed to access secret version: %v", err)
	}

	return string(result.Payload.Data[:]), nil
}

type BootstrapDatabaseRequest struct {
	CdbName      string
	Version      string
	Host         string
	DbUniqueName string
	Dbdomain     string
	Mode         BootstrapDatabaseRequestBootstrapMode
	LroInput     *LROInput
}

type BootstrapDatabaseRequestBootstrapMode int32

const (
	BootstrapDatabaseRequest_ProvisionUnseeded BootstrapDatabaseRequestBootstrapMode = 0
	BootstrapDatabaseRequest_ProvisionSeeded   BootstrapDatabaseRequestBootstrapMode = 1
	BootstrapDatabaseRequest_Restore           BootstrapDatabaseRequestBootstrapMode = 2
)

// BootstrapDatabase bootstrap a CDB after creation or restore.
func BootstrapDatabase(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req BootstrapDatabaseRequest) (*lropb.Operation, error) {
	klog.InfoS("BootstrapDatabase", "namespace", namespace, "instName", instName, "cdbName", req.CdbName)

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("BootstrapDatabase: failed to create database daemon client: %v", err)
	}
	defer closeConn()

	resp, err := dbClient.FileExists(ctx, &dbdpb.FileExistsRequest{Name: consts.ProvisioningDoneFile})
	if err != nil {
		return nil, fmt.Errorf("BootstrapDatabase: failed to check a provisioning file: %v", err)
	}

	if resp.Exists {
		klog.InfoS("BootstrapDatabase: provisioning file found, skip bootstrapping")
		return &lropb.Operation{Done: true}, nil
	}

	switch req.Mode {
	case BootstrapDatabaseRequest_ProvisionUnseeded:
		task := provision.NewBootstrapDatabaseTaskForUnseeded(req.CdbName, req.DbUniqueName, req.Dbdomain, dbClient)

		if err := task.Call(ctx); err != nil {
			return nil, fmt.Errorf("BootstrapDatabase: failed to bootstrap database : %v", err)
		}
	case BootstrapDatabaseRequest_ProvisionSeeded:
		lro, err := dbClient.BootstrapDatabaseAsync(ctx, &dbdpb.BootstrapDatabaseAsyncRequest{
			SyncRequest: &dbdpb.BootstrapDatabaseRequest{
				CdbName:  req.CdbName,
				DbDomain: req.Dbdomain,
			},
			LroInput: &dbdpb.LROInput{OperationId: req.LroInput.OperationId},
		})
		if err != nil {
			return nil, fmt.Errorf("BootstrapDatabase: error while call dbdaemon/BootstrapDatabase: %v", err)
		}
		return lro, nil
	default:
	}

	if _, err = dbClient.CreateListener(ctx, &dbdpb.CreateListenerRequest{
		DatabaseName: req.CdbName,
		Port:         consts.SecureListenerPort,
		Protocol:     "TCP",
		DbDomain:     req.Dbdomain,
	}); err != nil {
		return nil, fmt.Errorf("BootstrapDatabase: error while creating listener: %v", err)
	}

	if _, err = dbClient.CreateFile(ctx, &dbdpb.CreateFileRequest{
		Path: consts.ProvisioningDoneFile,
	}); err != nil {
		return nil, fmt.Errorf("BootstrapDatabase: error while creating provisioning done file: %v", err)
	}

	return &lropb.Operation{Done: true}, nil
}

type BootstrapStandbyRequest struct {
	CdbName  string
	Version  string
	Dbdomain string
}

type BootstrapStandbyResponseUser struct {
	UserName string
	Privs    []string
}

type BootstrapStandbyResponsePDB struct {
	PdbName string
	Users   []*BootstrapStandbyResponseUser
}

// BootstrapStandby performs bootstrap steps for standby instance.
func BootstrapStandby(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req BootstrapStandbyRequest) ([]*BootstrapStandbyResponsePDB, error) {
	klog.InfoS("BootstrapStandby", "namespace", namespace, "instName", instName, "cdbName", req.CdbName, "version", req.Version, "dbdomain", req.Dbdomain)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("BootstrapStandby: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	klog.InfoS("CreateUsers", "client", dbClient)

	// skip if already bootstrapped
	resp, err := dbClient.FileExists(ctx, &dbdpb.FileExistsRequest{Name: consts.ProvisioningDoneFile})
	if err != nil {
		return nil, fmt.Errorf("BootstrapStandby: failed to check a provisioning file: %v", err)
	}

	if resp.Exists {
		klog.InfoS("BootstrapStandby: standby is already provisioned")
		return nil, nil
	}

	task := provision.NewBootstrapDatabaseTaskForStandby(req.CdbName, req.Dbdomain, dbClient)

	if err := task.Call(ctx); err != nil {
		return nil, fmt.Errorf("BootstrapStandby: failed to bootstrap standby database : %v", err)
	}
	klog.InfoS("BootstrapStandby: bootstrap task completed successfully")

	// create listeners
	err = CreateListener(ctx, r, dbClientFactory, namespace, instName, &CreateListenerRequest{
		Name:     req.CdbName,
		Port:     consts.SecureListenerPort,
		Protocol: "TCP",
		DbDomain: req.Dbdomain,
	})
	if err != nil {
		return nil, fmt.Errorf("BootstrapStandby: failed to create listener: %v", err)
	}

	if _, err := dbClient.BootstrapStandby(ctx, &dbdpb.BootstrapStandbyRequest{
		CdbName: req.CdbName,
	}); err != nil {
		return nil, fmt.Errorf("BootstrapStandby: dbdaemon failed to bootstrap standby: %v", err)
	}
	klog.InfoS("BootstrapStandby: dbdaemon completed bootstrap standby successfully")

	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{consts.OpenPluggableDatabaseSQL}, Suppress: false})
	if err != nil {
		return nil, fmt.Errorf("BootstrapStandby: failed to open pluggable database: %v", err)
	}

	// fetch existing pdbs/users to create database resources for
	knownPDBsResp, err := dbClient.KnownPDBs(ctx, &dbdpb.KnownPDBsRequest{
		IncludeSeed: false,
		OnlyOpen:    false,
	})
	if err != nil {
		return nil, fmt.Errorf("BootstrapStandby: dbdaemon failed to get KnownPDBs: %v", err)
	}

	var migratedPDBs []*BootstrapStandbyResponsePDB
	for _, pdb := range knownPDBsResp.GetKnownPdbs() {
		us := newUsers(pdb, []*User{})
		_, _, existingUsers, _, err := us.diff(ctx, dbClient)
		if err != nil {
			return nil, fmt.Errorf("BootstrapStandby: failed to get existing users for pdb %v: %v", pdb, err)
		}
		var migratedUsers []*BootstrapStandbyResponseUser
		for _, u := range existingUsers {
			migratedUsers = append(migratedUsers, &BootstrapStandbyResponseUser{
				UserName: u.GetUserName(),
				Privs:    u.GetUserEnvPrivs(),
			})
		}
		migratedPDBs = append(migratedPDBs, &BootstrapStandbyResponsePDB{
			PdbName: strings.ToLower(pdb),
			Users:   migratedUsers,
		})
	}

	klog.InfoS("BootstrapStandby: fetch existing pdbs and users successfully.", "MigratedPDBs", migratedPDBs)
	return migratedPDBs, nil
}

type CreateListenerRequest struct {
	Name       string
	Port       int32
	Protocol   string
	OracleHome string
	DbDomain   string
}

// CreateListener invokes dbdaemon.CreateListener.
func CreateListener(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req *CreateListenerRequest) error {
	klog.InfoS("CreateListener", "namespace", namespace, "instName", instName, "listenerName", req.Name, "port", req.Port, "protocol", req.Protocol, "oracleHome", req.OracleHome, "dbDomain", req.DbDomain)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return fmt.Errorf("CreateListener: failed to create listener: %v", err)
	}
	defer closeConn()
	klog.InfoS("CreateListener", "dbClient", dbClient)

	_, err = dbClient.CreateListener(ctx, &dbdpb.CreateListenerRequest{
		DatabaseName: req.Name,
		Port:         req.Port,
		Protocol:     req.Protocol,
		OracleHome:   req.OracleHome,
		DbDomain:     req.DbDomain,
	})
	if err != nil {
		return fmt.Errorf("CreateListener: error while creating listener: %v", err)
	}
	return nil
}
