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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/standbyhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/backup"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common/sql"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/standby"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/database/provision"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/util/secret"
	secretmanagerpb "google.golang.org/genproto/googleapis/cloud/secretmanager/v1"
	lropb "google.golang.org/genproto/googleapis/longrunning"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/api/resource"
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
	klog.InfoS("config_agent_helpers/CreateCDB", "namespace", namespace, "instName", instName, "sid", req.Sid)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/CreateCDB: failed to create database daemon dbdClient: %v", err)
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
		return nil, fmt.Errorf("config_agent_helpers/CreateCDB: failed to create CDB: %v", err)
	}

	klog.InfoS("config_agent_helpers/CreateCDB successfully completed")
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
	klog.InfoS("config_agent_helpers/BounceDatabase", "namespace", namespace, "instName", instName, "sid", req.Sid)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return err
	}
	defer closeConn()

	klog.InfoS("config_agent_helpers/BounceDatabase", "client", dbClient)
	_, err = dbClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
		DatabaseName: req.Sid,
		Option:       "immediate",
	})
	if err != nil {
		return fmt.Errorf("config_agent_helpers/BounceDatabase: error while shutting db: %v", err)
	}
	klog.InfoS("config_agent_helpers/BounceDatabase: shutdown successful")

	_, err = dbClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:         dbdpb.BounceDatabaseRequest_STARTUP,
		DatabaseName:      req.Sid,
		AvoidConfigBackup: req.AvoidConfigBackup,
	})
	if err != nil {
		return fmt.Errorf("config_agent_helpers/BounceDatabase: error while starting db: %v", err)
	}
	klog.InfoS("config_agent_helpers/BounceDatabase: startup successful")
	return err
}

func RecoverConfigFile(ctx context.Context, dbClientFactory DatabaseClientFactory, r client.Reader, namespace, instName, cdbName string) error {
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return err
	}
	defer closeConn()

	if _, err := dbClient.RecoverConfigFile(ctx, &dbdpb.RecoverConfigFileRequest{CdbName: cdbName}); err != nil {
		klog.InfoS("config_agent_helpers/configagent/RecoverConfigFile: error while recovering config file: err", "err", err)
		return fmt.Errorf("config_agent_helpers/RecoverConfigFile: failed to recover config file due to: %v", err)
	}
	klog.InfoS("config_agent_helpers/configagent/RecoverConfigFile: config file backup successful")
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
	klog.InfoS("config_agent_helpers/CreateDatabase", "namespace", namespace, "instName", instName, "cdbName", req.CdbName, "pdbName", req.Name)

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
			return "", fmt.Errorf("config_agent_helpers/CreateDatabase: failed to retrieve secret from Google Secret Manager: %v", err)
		}
	}

	p, err := buildPDB(req.CdbName, req.Name, pwd, version, consts.ListenerNames, true)
	if err != nil {
		return "", err
	}

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return "", fmt.Errorf("config_agent_helpers/CreateDatabase: failed to create database daemon dbdClient: %v", err)
	}
	defer closeConn()
	klog.InfoS("config_agent_helpers/CreateDatabase", "dbClient", dbClient)

	_, err = dbClient.CheckDatabaseState(ctx, &dbdpb.CheckDatabaseStateRequest{IsCdb: true, DatabaseName: req.CdbName, DbDomain: req.DbDomain})
	if err != nil {
		return "", fmt.Errorf("config_agent_helpers/CreateDatabase: failed to check a CDB state: %v", err)
	}
	klog.InfoS("config_agent_helpers/CreateDatabase: pre-flight check#1: CDB is up and running")

	pdbCheckCmd := []string{fmt.Sprintf("select open_mode, restricted from v$pdbs where name = '%s'", sql.StringParam(p.pluggableDatabaseName))}
	resp, err := dbClient.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: pdbCheckCmd, Suppress: false})
	if err != nil {
		return "", fmt.Errorf("config_agent_helpers/CreateDatabase: failed to check if a PDB called %s already exists: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("config_agent_helpers/CreateDatabase pre-flight check#2", "pdb", p.pluggableDatabaseName, "resp", resp)

	if resp != nil && resp.Msg != nil {
		if toUpdateGsmAdminPwd || toUpdatePlaintextAdminPwd {
			sqls := append([]string{sql.QuerySetSessionContainer(p.pluggableDatabaseName)}, []string{sql.QueryAlterUser(pdbAdmin, pwd)}...)
			if _, err := dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
				Commands: sqls,
			}); err != nil {
				return "", fmt.Errorf("failed to alter user %s: %v", pdbAdmin, err)
			}
			klog.InfoS("config_agent_helpers/CreateDatabase update pdb admin user succeeded", "user", pdbAdmin)
			return "AdminUserSyncCompleted", nil
		}
		klog.InfoS("config_agent_helpers/CreateDatabase pre-flight check#2", "pdb", p.pluggableDatabaseName, "respMsg", resp.Msg)
		return "AlreadyExists", nil
	}
	klog.InfoS("config_agent_helpers/CreateDatabase pre-flight check#2: pdb doesn't exist, proceeding to create", "pdb", p.pluggableDatabaseName)

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
		return "", fmt.Errorf("config_agent_helpers/CreateDatabase: failed to create a PDB %s: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("config_agent_helpers/CreateDatabase create a PDB Done", "pdb", p.pluggableDatabaseName)

	pdbOpen := []string{fmt.Sprintf("alter pluggable database %s open read write", sql.MustBeObjectName(p.pluggableDatabaseName))}
	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: pdbOpen, Suppress: false})
	if err != nil {
		return "", fmt.Errorf("config_agent_helpers/CreatePDBDatabase: PDB %s open failed: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("config_agent_helpers/CreateDatabase PDB open", "pdb", p.pluggableDatabaseName)

	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{
		sql.QuerySetSessionContainer(p.pluggableDatabaseName),
		sql.QueryGrantPrivileges("create session, dba", pdbAdmin),
		sql.QueryGrantPrivileges("create session, resource, datapump_imp_full_database, datapump_exp_full_database, unlimited tablespace", consts.PDBLoaderUser),
	}, Suppress: false})
	if err != nil {
		// Until we have a proper error handling, just log an error here.
		klog.ErrorS(err, "CreateDatabase: failed to create a PDB_ADMIN user and/or PDB loader user")
	}
	klog.InfoS("config_agent_helpers/CreateDatabase: created PDB_ADMIN and PDB Loader users")

	// Separate out the directory treatment for the ease of troubleshooting.
	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{
		sql.QuerySetSessionContainer(p.pluggableDatabaseName),
		sql.QueryCreateDir(consts.DpdumpDir.Oracle, filepath.Join(p.pathPrefix, consts.DpdumpDir.Linux)),
		sql.QueryGrantPrivileges(fmt.Sprintf("read,write on directory %s", consts.DpdumpDir.Oracle), consts.PDBLoaderUser),
	}, Suppress: false})
	if err != nil {
		klog.ErrorS(err, "CreateDatabase: failed to create a Data Pump directory", "datapumpDir", consts.DpdumpDir)
	}
	klog.InfoS("config_agent_helpers/CreateDatabase: DONE", "pdb", p.pluggableDatabaseName)

	return "Ready", nil
}

type DeleteDatabaseRequest struct {
	Name     string
	DbDomain string
}

// DeleteDatabase deletes the specified Database(PDB)
func DeleteDatabase(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req DeleteDatabaseRequest) error {
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return fmt.Errorf("config_agent_helpers/CreateDatabase: failed to create database daemon dbdClient: %v", err)
	}
	defer closeConn()

	pdbName := strings.ToUpper(req.Name)

	pdbCheckCmd := []string{fmt.Sprintf("select open_mode, restricted from v$pdbs where name = '%s'", sql.StringParam(pdbName))}
	resp, err := dbClient.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: pdbCheckCmd, Suppress: false})
	if err != nil {
		return fmt.Errorf("config_agent_helpers/DeleteDatabase: failed to check if a PDB named %s already exists: %v", pdbName, err)
	}

	if resp != nil && resp.Msg != nil {
		klog.InfoS("config_agent_helpers/DeleteDatabase completed pre-flight check. The PDB exists.", "pdb", pdbName, "resp", resp)
	} else {
		klog.InfoS(fmt.Sprintf("config_agent_helpers/DeleteDatabase: A PDB named %s was not found", pdbName))
		return nil
	}

	closePdbCmd := []string{fmt.Sprintf("alter pluggable database %s close immediate", sql.StringParam(pdbName))}
	_, err = dbClient.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: closePdbCmd, Suppress: false})
	if err != nil {
		return fmt.Errorf("config_agent_helpers/DeleteDatabase: failed to close the PDB named %s: %v", pdbName, err)
	}

	deletePdbCmd := []string{fmt.Sprintf("drop pluggable database %s including datafiles", sql.StringParam(pdbName))}
	_, err = dbClient.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: deletePdbCmd, Suppress: false})
	if err != nil {
		return fmt.Errorf("config_agent_helpers/DeleteDatabase: failed to delete the PDB named %s: %v", pdbName, err)
	}

	return nil
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
	klog.InfoS("config_agent_helpers/UsersChanged", "namespace", namespace, "instName", instName, "pdbName", req.PdbName)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	us := newUsers(req.PdbName, req.UserSpecs)
	toCreate, toUpdate, toDelete, toUpdatePwd, err := us.diff(ctx, dbClient)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/UsersChanged: failed to get difference between env and spec for users: %v", err)
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
	klog.InfoS("config_agent_helpers/UsersChanged: DONE", "resp", resp)
	return resp, nil
}

type UpdateUsersRequest struct {
	PdbName   string
	UserSpecs []*User
}

// UpdateUsers update/create users as requested.
func UpdateUsers(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req UpdateUsersRequest) error {
	klog.InfoS("config_agent_helpers/UpdateUsers", "namespace", namespace, "instName", instName, "pdbName", req.PdbName)

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return fmt.Errorf("config_agent_helpers/UpdateUsers: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	us := newUsers(req.PdbName, req.UserSpecs)
	toCreate, toUpdate, _, toUpdatePwd, err := us.diff(ctx, dbClient)
	if err != nil {
		return fmt.Errorf("config_agent_helpers/UpdateUsers: failed to get difference between env and spec for users: %v", err)
	}
	foundErr := false
	for _, u := range toCreate {
		klog.InfoS("config_agent_helpers/UpdateUsers", "creating user", u.userName)
		if err := u.create(ctx, dbClient); err != nil {
			klog.ErrorS(err, "failed to create user")
			foundErr = true
		}
	}

	for _, u := range toUpdate {
		klog.InfoS("config_agent_helpers/UpdateUsers", "updating user", u.userName)
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
		klog.InfoS("config_agent_helpers/UpdateUsers", "updating user", u.userName)
		if err := u.updatePassword(ctx, dbClient); err != nil {
			klog.ErrorS(err, "failed to update user password")
			foundErr = true
		}
	}

	if foundErr {
		return errors.New("failed to update users")
	}
	klog.InfoS("config_agent_helpers/UpdateUsers: DONE")
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
		return false, fmt.Errorf("config_agent_helpers/SetParameter: error while inferring parameter type: %v", err)
	}
	query = fmt.Sprintf("select type from v$parameter where name='%s'", sql.StringParam(key))
	paramDatatype, err := fetchAndParseSingleResultQuery(ctx, dbClient, query)
	if err != nil {
		return false, fmt.Errorf("config_agent_helpers/SetParameter: error while inferring parameter data type: %v", err)
	}
	// string parameters need to be quoted,
	// those have type 2, see the link for the parameter types description
	// https://docs.oracle.com/database/121/REFRN/GUID-C86F3AB0-1191-447F-8EDF-4727D8693754.htm
	isStringParam := paramDatatype == "2"
	command, err := sql.QuerySetSystemParameterNoPanic(key, value, isStringParam)
	if err != nil {
		return false, fmt.Errorf("config_agent_helpers/SetParameter: error constructing set parameter query: %v", err)
	}

	isStatic := false
	if paramType == "FALSE" {
		klog.InfoS("config_agent_helpers/SetParameter", "parameter_type", "STATIC")
		command = fmt.Sprintf("%s scope=spfile", command)
		isStatic = true
	}

	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{command},
		Suppress: false,
	})
	if err != nil {
		return false, fmt.Errorf("config_agent_helpers/SetParameter: error while executing parameter command: %q", command)
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
	if response == nil {
		return "", nil
	}
	result, err := parseSQLResponse(response)
	if err != nil {
		return "", fmt.Errorf("error while parsing query response: %q; error: %v", query, err)
	}

	var rows []string
	for _, row := range result {
		if len(row) != 1 {
			return "", fmt.Errorf("config_agent_helpers/fetchAndParseSingleColumnMultiRowQueriesFromEM: # of cols returned by query != 1: %v", row)
		}
		for _, v := range row {
			rows = append(rows, v)
		}
	}
	if len(rows) < 1 {
		return "", nil
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
	klog.InfoS("config_agent_helpers/CreateUsers", "namespace", namespace, "cdbName", req.CdbName, "pdbName", req.PdbName)

	p, err := buildPDB(req.CdbName, req.PdbName, "", version, consts.ListenerNames, true)
	if err != nil {
		return "", err
	}

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return "", fmt.Errorf("config_agent_helpers/CreateUsers: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	klog.InfoS("config_agent_helpers/CreateUsers", "client", dbClient)

	_, err = dbClient.CheckDatabaseState(ctx, &dbdpb.CheckDatabaseStateRequest{IsCdb: true, DatabaseName: req.CdbName, DbDomain: req.DbDomain})
	if err != nil {
		return "", fmt.Errorf("config_agent_helpers/CreateUsers: failed to check a CDB state: %v", err)
	}
	klog.InfoS("config_agent_helpers/CreateUsers: pre-flight check#: CDB is up and running")

	// Separate create users from grants to make troubleshooting easier.
	usersCmd := []string{sql.QuerySetSessionContainer(p.pluggableDatabaseName)}
	usersCmd = append(usersCmd, req.CreateUsersCmd...)
	for _, u := range req.User {
		if u.PasswordGsmSecretRef != nil && u.Name != "" {
			var pwd string
			pwd, err = AccessSecretVersionFunc(ctx, fmt.Sprintf(gsmSecretStr, u.PasswordGsmSecretRef.ProjectId, u.PasswordGsmSecretRef.SecretId, u.PasswordGsmSecretRef.Version))
			if err != nil {
				return "", fmt.Errorf("config_agent_helpers/CreateUsers: failed to retrieve secret from Google Secret Manager: %v", err)
			}
			if _, err = sql.Identifier(pwd); err != nil {
				return "", fmt.Errorf("config_agent_helpers/CreateUsers: Google Secret Manager contains an invalid password for user %q: %v", u.Name, err)
			}

			usersCmd = append(usersCmd, sql.QueryCreateUser(u.Name, pwd))
		}
	}
	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: usersCmd, Suppress: false})
	if err != nil {
		return "", fmt.Errorf("config_agent_helpers/CreateUsers: failed to create users in a PDB %s: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("config_agent_helpers/CreateUsers: create users in PDB DONE", "pdb", p.pluggableDatabaseName)

	privsCmd := []string{sql.QuerySetSessionContainer(p.pluggableDatabaseName)}
	privsCmd = append(privsCmd, req.GrantPrivsCmd...)
	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: privsCmd, Suppress: false})
	if err != nil {
		return "", fmt.Errorf("config_agent_helpers/CreateUsers: failed to grant privileges in a PDB %s: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("config_agent_helpers/CreateUsers: DONE", "pdb", p.pluggableDatabaseName)

	return "Ready", nil
}

// AccessSecretVersionFunc accesses the payload for the given secret version if one
// exists. The version can be a version number as a string (e.g. "5") or an
// alias (e.g. "latest").
var AccessSecretVersionFunc = func(ctx context.Context, name string) (string, error) {
	// Create the GSM client.
	client, closeConn, err := newGsmClient(ctx)
	if err != nil {
		return "", fmt.Errorf("config_agent_helpers/AccessSecretVersionFunc: failed to create secretmanager client: %v", err)
	}
	defer closeConn()

	// Build the request.
	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: name,
	}

	// Call the API.
	result, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("config_agent_helpers/AccessSecretVersionFunc: failed to access secret version: %v", err)
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
	klog.InfoS("config_agent_helpers/BootstrapDatabase", "namespace", namespace, "instName", instName, "cdbName", req.CdbName)

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/BootstrapDatabase: failed to create database daemon client: %v", err)
	}
	defer closeConn()

	resp, err := dbClient.FileExists(ctx, &dbdpb.FileExistsRequest{Name: consts.ProvisioningDoneFile})
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/BootstrapDatabase: failed to check a provisioning file: %v", err)
	}

	if resp.Exists {
		klog.InfoS("config_agent_helpers/BootstrapDatabase: provisioning file found, skip bootstrapping")
		return &lropb.Operation{Done: true}, nil
	}

	switch req.Mode {
	case BootstrapDatabaseRequest_ProvisionUnseeded:
		task := provision.NewBootstrapDatabaseTaskForUnseeded(req.CdbName, req.DbUniqueName, req.Dbdomain, dbClient)

		if err := task.Call(ctx); err != nil {
			return nil, fmt.Errorf("config_agent_helpers/BootstrapDatabase: failed to bootstrap database : %v", err)
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
			return nil, fmt.Errorf("config_agent_helpers/BootstrapDatabase: error while call dbdaemon/BootstrapDatabase: %v", err)
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
		return nil, fmt.Errorf("config_agent_helpers/BootstrapDatabase: error while creating listener: %v", err)
	}

	if _, err = dbClient.CreateFile(ctx, &dbdpb.CreateFileRequest{
		Path: consts.ProvisioningDoneFile,
	}); err != nil {
		return nil, fmt.Errorf("config_agent_helpers/BootstrapDatabase: error while creating provisioning done file: %v", err)
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
	klog.InfoS("config_agent_helpers/BootstrapStandby", "namespace", namespace, "instName", instName, "cdbName", req.CdbName, "version", req.Version, "dbdomain", req.Dbdomain)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/BootstrapStandby: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	klog.InfoS("BootstrapStandby", "client", dbClient)
	if err := standby.BootstrapStandby(ctx, dbClient); err != nil {
		return nil, fmt.Errorf("config_agent_helpers/BootstrapStandby: failed to bootstrap standby database : %v", err)
	}
	klog.InfoS("config_agent_helpers/BootstrapStandby: bootstrap task completed successfully")

	// fetch existing pdbs/users to create database resources for
	knownPDBsResp, err := dbClient.KnownPDBs(ctx, &dbdpb.KnownPDBsRequest{
		IncludeSeed: false,
		OnlyOpen:    false,
	})
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/BootstrapStandby: dbdaemon failed to get KnownPDBs: %v", err)
	}

	var migratedPDBs []*BootstrapStandbyResponsePDB
	for _, pdb := range knownPDBsResp.GetKnownPdbs() {
		us := newUsers(pdb, []*User{})
		_, _, existingUsers, _, err := us.diff(ctx, dbClient)
		if err != nil {
			return nil, fmt.Errorf("config_agent_helpers/BootstrapStandby: failed to get existing users for pdb %v: %v", pdb, err)
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

	klog.InfoS("config_agent_helpers/BootstrapStandby: fetch existing pdbs and users successfully.", "MigratedPDBs", migratedPDBs)
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
	klog.InfoS("config_agent_helpers/CreateListener", "namespace", namespace, "instName", instName, "listenerName", req.Name, "port", req.Port, "protocol", req.Protocol, "oracleHome", req.OracleHome, "dbDomain", req.DbDomain)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return fmt.Errorf("config_agent_helpers/CreateListener: failed to create listener: %v", err)
	}
	defer closeConn()
	klog.InfoS("config_agent_helpers/CreateListener", "dbClient", dbClient)

	_, err = dbClient.CreateListener(ctx, &dbdpb.CreateListenerRequest{
		DatabaseName: req.Name,
		Port:         req.Port,
		Protocol:     req.Protocol,
		OracleHome:   req.OracleHome,
		DbDomain:     req.DbDomain,
	})
	if err != nil {
		return fmt.Errorf("config_agent_helpers/CreateListener: error while creating listener: %v", err)
	}
	return nil
}

type VerifyPhysicalBackupRequest struct {
	GcsPath string
}

type VerifyPhysicalBackupResponse struct {
	ErrMsgs []string
}

// VerifyPhysicalBackup verifies the existence of physical backup.
func VerifyPhysicalBackup(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req VerifyPhysicalBackupRequest) (*VerifyPhysicalBackupResponse, error) {
	klog.InfoS("config_agent_helpers/VerifyPhysicalBackup", "namespace", namespace, "instName", instName, "gcsPath", req.GcsPath)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/VerifyPhysicalBackup: failed to create a database daemon dbdClient: %v", err)
	}
	defer closeConn()
	if _, err := dbClient.DownloadDirectoryFromGCS(ctx, &dbdpb.DownloadDirectoryFromGCSRequest{
		GcsPath:               req.GcsPath,
		AccessPermissionCheck: true,
	}); err != nil {
		return &VerifyPhysicalBackupResponse{ErrMsgs: []string{err.Error()}}, nil
	}
	return &VerifyPhysicalBackupResponse{}, nil
}

type PhysicalBackupRequest struct {
	BackupSubType PhysicalBackupRequest_Type
	BackupItems   []string
	Backupset     bool
	Compressed    bool
	CheckLogical  bool
	// DOP = degree of parallelism for physical backup.
	Dop         int32
	Level       int32
	Filesperset int32
	SectionSize int32
	LocalPath   string
	GcsPath     string
	LroInput    *LROInput
	BackupTag   string
}

type PhysicalBackupRequest_Type int32

const (
	PhysicalBackupRequest_UNKNOWN_TYPE PhysicalBackupRequest_Type = 0
	PhysicalBackupRequest_INSTANCE     PhysicalBackupRequest_Type = 1
	PhysicalBackupRequest_DATABASE     PhysicalBackupRequest_Type = 2
	PhysicalBackupRequest_TABLESPACE   PhysicalBackupRequest_Type = 3
	PhysicalBackupRequest_DATAFILE     PhysicalBackupRequest_Type = 4
)

// PhysicalBackup starts an RMAN backup and stores it in the GCS bucket provided.
func PhysicalBackup(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req PhysicalBackupRequest) (*lropb.Operation, error) {
	klog.InfoS("config_agent_helpers/PhysicalBackup", "namespace", namespace, "instName", instName, "gcsPath", req.GcsPath, "localPath", req.LocalPath)
	var granularity string
	switch req.BackupSubType {
	case PhysicalBackupRequest_INSTANCE:
		granularity = "database"
	case PhysicalBackupRequest_DATABASE:
		if req.BackupItems == nil {
			return &lropb.Operation{}, fmt.Errorf("config_agent_helpers/PhysicalBackup: failed a pre-flight check: a PDB backup is requested, but no PDB name(s) given")
		}

		granularity = "pluggable database "
		for i, pdb := range req.BackupItems {
			if i == 0 {
				granularity += pdb
			} else {
				granularity += ", "
				granularity += pdb
			}
		}
	default:
		return &lropb.Operation{}, fmt.Errorf("config_agent_helpers/PhysicalBackup: unsupported in this release sub backup type of %v", req.BackupSubType)
	}
	klog.InfoS("config_agent_helpers/PhysicalBackup", "granularity", granularity)

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/PhysicalBackup: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	klog.InfoS("config_agent_helpers/PhysicalBackup", "dbClient", dbClient)

	sectionSize := resource.NewQuantity(int64(req.SectionSize), resource.DecimalSI)
	return backup.PhysicalBackup(ctx, &backup.Params{
		Client:       dbClient,
		Granularity:  granularity,
		Backupset:    req.Backupset,
		CheckLogical: req.CheckLogical,
		Compressed:   req.Compressed,
		DOP:          req.Dop,
		Level:        req.Level,
		Filesperset:  req.Filesperset,
		SectionSize:  *sectionSize,
		LocalPath:    req.LocalPath,
		GCSPath:      req.GcsPath,
		BackupTag:    req.BackupTag,
		OperationID:  req.LroInput.OperationId,
	})
}

type PhysicalRestoreRequest struct {
	InstanceName string
	CdbName      string
	// DOP = degree of parallelism for a restore from a physical backup.
	Dop               int32
	LocalPath         string
	GcsPath           string
	LroInput          *LROInput
	LogGcsPath        string
	Incarnation       string
	BackupIncarnation string
	StartTime         *timestamppb.Timestamp
	EndTime           *timestamppb.Timestamp
	StartScn          int64
	EndScn            int64
}

// PhysicalRestore restores an RMAN backup (downloaded from GCS).
func PhysicalRestore(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req PhysicalRestoreRequest) (*lropb.Operation, error) {
	klog.InfoS("config_agent_helpers/PhysicalRestore", "namespace", namespace, "instName", instName, "gcsPath", req.GcsPath, "localPath", req.LocalPath)

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/PhysicalRestore: failed to create database daemon client: %v", err)
	}
	defer closeConn()

	return backup.PhysicalRestore(ctx, &backup.Params{
		Client:            dbClient,
		InstanceName:      req.InstanceName,
		CDBName:           req.CdbName,
		DOP:               req.Dop,
		LocalPath:         req.LocalPath,
		GCSPath:           req.GcsPath,
		OperationID:       req.LroInput.OperationId,
		LogGcsDir:         req.LogGcsPath,
		Incarnation:       req.Incarnation,
		BackupIncarnation: req.BackupIncarnation,
		StartTime:         req.StartTime,
		EndTime:           req.EndTime,
		StartSCN:          req.StartScn,
		EndSCN:            req.EndScn,
	})
}

type CheckStatusRequest struct {
	Name            string
	CdbName         string
	CheckStatusType CheckStatusRequest_Type
	DbDomain        string
}

type CheckStatusRequest_Type int32

const (
	CheckStatusRequest_UNKNOWN_TYPE CheckStatusRequest_Type = 0
	CheckStatusRequest_INSTANCE     CheckStatusRequest_Type = 1
)

type CheckStatusResponse struct {
	Status       string
	ErrorMessage string
}

// CheckStatus runs a requested set of state checks.
// The Instance state check consists of:
//   - checking the provisioning done file.
//   - running a CDB connection test via DB Daemon.
func CheckStatus(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req CheckStatusRequest) (*CheckStatusResponse, error) {
	klog.InfoS("config_agent_helpers/CheckStatus", "namespace", namespace, "instName", instName, "name", req.Name, "cdbName", req.CdbName, "checkStatusType", req.CheckStatusType)

	switch req.CheckStatusType {
	case CheckStatusRequest_INSTANCE:
		klog.InfoS("config_agent_helpers/CheckStatus: running a Database Instance status check...")
	default:
		return &CheckStatusResponse{}, fmt.Errorf("config_agent_helpers/CheckStatus: unsupported in this release check status type of %v", req.CheckStatusType)
	}

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/CheckStatus: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	klog.V(1).InfoS("config_agent_helpers/CheckStatus", "client", dbClient)

	resp, err := dbClient.FileExists(ctx, &dbdpb.FileExistsRequest{Name: consts.ProvisioningDoneFile})
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/CheckStatus: failed to check a provisioning file: %v", err)
	}

	if !resp.Exists {
		klog.InfoS("config_agent_helpers/CheckStatus: provisioning file NOT found")
		return &CheckStatusResponse{Status: "InProgress"}, nil
	}
	klog.InfoS("config_agent_helpers/CheckStatus: provisioning file found")

	if _, err = dbClient.CheckDatabaseState(ctx, &dbdpb.CheckDatabaseStateRequest{IsCdb: true, DatabaseName: req.CdbName, DbDomain: req.DbDomain}); err != nil {
		return nil, fmt.Errorf("config_agent_helpers/CheckStatus: failed to check a Database Instance state: %v", err)
	}
	klog.InfoS("config_agent_helpers/CheckStatus: Database Instance is up and running")

	pdbCheckCmd := []string{"select open_mode, restricted from v$pdbs"}
	resp2, err := dbClient.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: pdbCheckCmd, Suppress: false})
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/CheckStatus: failed to get a list of available PDBs: %v", err)
	}
	klog.InfoS("config_agent_helpers/CheckStatus", "PDB query response", resp2)

	return &CheckStatusResponse{Status: "Ready"}, nil
}

type DataPumpImportRequest struct {
	PdbName  string
	DbDomain string
	// GCS path to input dump file
	GcsPath string
	// GCS path to output log file
	GcsLogPath string
	LroInput   *LROInput
}

// DataPumpImport imports data dump file provided in GCS path.
func DataPumpImport(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req DataPumpImportRequest) (*lropb.Operation, error) {
	klog.InfoS("config_agent_helpers/DataPumpImport", "namespace", namespace, "instName", instName, "pdbName", req.PdbName, "dbDomain", req.DbDomain, "gcsPath", req.GcsPath)

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/DataPumpImport: failed to create database daemon client: %v", err)
	}
	defer func() { _ = closeConn() }()

	return dbClient.DataPumpImportAsync(ctx, &dbdpb.DataPumpImportAsyncRequest{
		SyncRequest: &dbdpb.DataPumpImportRequest{
			PdbName:    req.PdbName,
			DbDomain:   req.DbDomain,
			GcsPath:    req.GcsPath,
			GcsLogPath: req.GcsLogPath,
			CommandParams: []string{
				"FULL=YES",
				"METRICS=YES",
				"LOGTIME=ALL",
			},
		},
		LroInput: &dbdpb.LROInput{
			OperationId: req.LroInput.OperationId,
		},
	})
}

type DataPumpExportRequest struct {
	PdbName       string
	DbDomain      string
	ObjectType    string
	Objects       string
	GcsPath       string
	GcsLogPath    string
	LroInput      *LROInput
	FlashbackTime string
}

// DataPumpExport exports data pump file to GCS path provided.
func DataPumpExport(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req DataPumpExportRequest) (*lropb.Operation, error) {
	klog.InfoS("config_agent_helpers/DataPumpExport", "namespace", namespace, "instName", instName, "pdbName", req.PdbName, "dbDomain", req.DbDomain, "objects", req.Objects, "gcsPath", req.GcsPath)

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/DataPumpExport: failed to create database daemon client: %v", err)
	}
	defer func() { _ = closeConn() }()

	return dbClient.DataPumpExportAsync(ctx, &dbdpb.DataPumpExportAsyncRequest{
		SyncRequest: &dbdpb.DataPumpExportRequest{
			PdbName:       req.PdbName,
			DbDomain:      req.DbDomain,
			ObjectType:    req.ObjectType,
			Objects:       req.Objects,
			GcsPath:       req.GcsPath,
			GcsLogPath:    req.GcsLogPath,
			FlashbackTime: req.FlashbackTime,
			CommandParams: []string{
				"METRICS=YES",
				"LOGTIME=ALL",
			},
		},
		LroInput: &dbdpb.LROInput{
			OperationId: req.LroInput.OperationId,
		},
	})
}

type GetParameterTypeValueRequest struct {
	Keys []string
}

type GetParameterTypeValueResponse struct {
	Types  []string
	Values []string
}

// GetParameterTypeValue returns parameters' type and value by querying DB.
func GetParameterTypeValue(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req GetParameterTypeValueRequest) (*GetParameterTypeValueResponse, error) {
	klog.InfoS("config_agent_helpers/GetParameterTypeValue", "namespace", namespace, "instName", instName, "keys", req.Keys)

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/GetParameterTypeValue: failed to create database daemon client: %v", err)
	}
	defer closeConn()

	types := []string{}
	values := []string{}

	for _, key := range req.Keys {
		query := fmt.Sprintf("select issys_modifiable from v$parameter where name='%s'", sql.StringParam(key))
		value, err := fetchAndParseSingleResultQuery(ctx, dbClient, query)
		if err != nil {
			return nil, fmt.Errorf("config_agent_helpers/GetParameterTypeValue: error while fetching type for %v: %v", key, err)
		}
		types = append(types, value)
	}
	for _, key := range req.Keys {
		query := fmt.Sprintf("select value from v$parameter where name='%s'", sql.StringParam(key))
		value, err := fetchAndParseSingleResultQuery(ctx, dbClient, query)
		if err != nil {
			return nil, fmt.Errorf("config_agent_helpers/GetParameterTypeValue: error while fetching value for %v: %v", key, err)
		}
		values = append(values, value)
	}

	return &GetParameterTypeValueResponse{Types: types, Values: values}, nil
}

type PhysicalBackupDeleteRequest struct {
	BackupTag string
	LocalPath string
	GcsPath   string
}

// PhysicalBackupDelete deletes backup data on local or GCS.
func PhysicalBackupDelete(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req PhysicalBackupDeleteRequest) error {
	klog.InfoS("config_agent_helpers/PhysicalBackupDelete", "namespace", namespace, "instName", instName, "backupTag", req.BackupTag, "localPath", req.LocalPath, "gcsPath", req.GcsPath)

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return fmt.Errorf("config_agent_helpers/PhysicalBackupDelete: failed to create database daemon client: %v", err)
	}
	defer closeConn()

	if err := backup.PhysicalBackupDelete(ctx, &backup.Params{
		Client:    dbClient,
		LocalPath: req.LocalPath,
		GCSPath:   req.GcsPath,
		BackupTag: req.BackupTag,
	}); err != nil {
		return fmt.Errorf("config_agent_helpers/PhysicalBackupDelete: failed to delete physical backup: %v", err)
	}

	return nil
}

type PhysicalBackupMetadataRequest struct {
	BackupTag string
}

type PhysicalBackupMetadataResponse struct {
	BackupScn         string
	BackupIncarnation string
	BackupTimestamp   *timestamppb.Timestamp
}

// PhysicalBackupMetadata fetches backup scn/timestamp/incarnation with provided backup tag.
func PhysicalBackupMetadata(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req PhysicalBackupMetadataRequest) (*PhysicalBackupMetadataResponse, error) {
	klog.InfoS("config_agent_helpers/PhysicalBackupMetadata", "namespace", namespace, "instName", instName, "backupTag", req.BackupTag)

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/PhysicalBackupMetadata: failed to create database daemon client: %v", err)
	}
	defer closeConn()

	// Find the max "Next SCN" in current archivelog backup, this will be the backup scn.
	// Example of list backup of archivelog output:
	//  Thrd Seq     Low SCN    Low Time  Next SCN   Next Time
	//  ---- ------- ---------- --------- ---------- ---------
	//  1    1       1527386    30-JUL-21 1530961    30-JUL-21
	listArchiveLogBackupCmd := "list backup of archivelog all tag '%s';"
	res, err := dbClient.RunRMAN(ctx, &dbdpb.RunRMANRequest{Scripts: []string{fmt.Sprintf(listArchiveLogBackupCmd, req.BackupTag)}})
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/PhysicalBackupMetadata: failed to list backup of archivelog: %v", err)
	}

	var threeLinesBuffer [3]string
	maxSCN := int64(-1)
	scanner := bufio.NewScanner(strings.NewReader(res.GetOutput()[0]))
	for scanner.Scan() {
		threeLinesBuffer[0] = threeLinesBuffer[1]
		threeLinesBuffer[1] = threeLinesBuffer[2]
		threeLinesBuffer[2] = scanner.Text()

		if strings.Contains(threeLinesBuffer[0], "Next SCN") {
			fields := strings.Fields(threeLinesBuffer[2])
			if len(fields) != 6 {
				return nil, fmt.Errorf("config_agent_helpers/PhysicalBackupMetadata: unexpected number of fields: %v", threeLinesBuffer[2])
			}
			currentSCN, err := strconv.ParseInt(fields[4], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("config_agent_helpers/PhysicalBackupMetadata: failed to parse 'Next SCN' %v: %v", fields[2], err)
			}
			if currentSCN > maxSCN {
				maxSCN = currentSCN
			}
		}
	}

	if maxSCN < 0 {
		return nil, fmt.Errorf("config_agent_helpers/PhysicalBackupMetadata: failed to find backup scn")
	}

	scnToTimestampSQL := "select to_char(scn_to_timestamp(%s) at time zone 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') as backuptime from dual"
	backupTimeResp, err := fetchAndParseSingleResultQuery(ctx, dbClient, fmt.Sprintf(scnToTimestampSQL, strconv.FormatInt(maxSCN, 10)))
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/PhysicalBackupMetadata: failed to query backup time: %s", err)
	}
	if backupTimeResp == "" {
		return nil, nil
	}
	backupTime, err := time.Parse(time.RFC3339, backupTimeResp)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/PhysicalBackupMetadata: failed to parse backup time: %s", err)
	}

	incResp, err := FetchDatabaseIncarnation(ctx, r, dbClientFactory, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/PhysicalBackupMetadata: failed to query database incarnation: %s", err)
	}

	klog.InfoS("config_agent_helpers/PhysicalBackupMetadata", "backup incarnation", incResp.Incarnation, "backup scn", maxSCN, "backup time", backupTime)
	return &PhysicalBackupMetadataResponse{
		BackupIncarnation: incResp.Incarnation,
		BackupScn:         strconv.FormatInt(maxSCN, 10),
		BackupTimestamp:   timestamppb.New(backupTime),
	}, nil
}

type FetchDatabaseIncarnationResponse struct {
	Incarnation string
}

// FetchDatabaseIncarnation fetches the database incarnation number.
func FetchDatabaseIncarnation(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string) (*FetchDatabaseIncarnationResponse, error) {
	klog.InfoS("config_agent_helpers/FetchDatabaseIncarnation", "namespace", namespace, "instName", instName)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	defer func() { _ = closeConn() }()
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/FetchDatabaseIncarnation: failed to create database daemon client: %w", err)
	}
	inc, err := fetchAndParseSingleResultQuery(ctx, dbClient, consts.GetDatabaseIncarnationSQL)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/FetchDatabaseIncarnation: failed to query database incarnation: %s", err)
	}
	return &FetchDatabaseIncarnationResponse{Incarnation: inc}, nil
}

type VerifyStandbySettingsRequest struct {
	PrimaryHost         string
	PrimaryPort         int32
	PrimaryService      string
	PrimaryUser         string
	PrimaryCredential   *Credential
	StandbyDbUniqueName string
	StandbyCdbName      string
	BackupGcsPath       string
	PasswordFileGcsPath string
	StandbyVersion      string
}

type VerifyStandbySettingsResponse struct {
	Errors []*standbyhelpers.StandbySettingErr
}

type Credential struct {
	// Types that are assignable to Source:
	//	*Credential_GsmSecretReference
	Source isCredentialSource
}

func (x *Credential) GetGsmSecretReference() *GsmSecretReference {
	if x, ok := x.Source.(*CredentialGsmSecretReference); ok {
		return x.GsmSecretReference
	}
	return nil
}

type isCredentialSource interface {
	isCredentialSource()
}

type CredentialGsmSecretReference struct {
	GsmSecretReference *GsmSecretReference
}

func (*CredentialGsmSecretReference) isCredentialSource() {}

// VerifyStandbySettings does preflight checks on standby settings.
func VerifyStandbySettings(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req VerifyStandbySettingsRequest) (*VerifyStandbySettingsResponse, error) {
	klog.InfoS("config_agent_helpers/VerifyStandbySettings", "namespace", namespace, "instName", instName, "primaryHost", req.PrimaryHost, "standbyDbUniqueName", req.StandbyDbUniqueName)

	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/VerifyStandbySettings: failed to create database daemon dbdClient: %v", err)
	}
	defer closeConn()

	sa := secret.NewGSMSecretAccessor(
		req.PrimaryCredential.GetGsmSecretReference().ProjectId,
		req.PrimaryCredential.GetGsmSecretReference().SecretId,
		req.PrimaryCredential.GetGsmSecretReference().Version,
	)
	defer sa.Clear()

	primaryDB := &standby.Primary{
		Host:             req.PrimaryHost,
		Port:             int(req.PrimaryPort),
		Service:          req.PrimaryService,
		User:             req.PrimaryUser,
		PasswordAccessor: sa,
	}

	standbyDB := &standby.Standby{
		CDBName:      req.StandbyCdbName,
		DBUniqueName: req.StandbyDbUniqueName,
		Version:      req.StandbyVersion,
	}

	settingErrs := standby.VerifyStandbySettings(ctx, primaryDB, standbyDB, req.PasswordFileGcsPath, req.BackupGcsPath, dbClient)
	//the returned error is always nil because all the errors that occurred during the verification have been added in settingErrs.
	return &VerifyStandbySettingsResponse{
		Errors: settingErrs,
	}, nil
}

type CreateStandbyRequest struct {
	PrimaryHost         string
	PrimaryPort         int32
	PrimaryService      string
	PrimaryUser         string
	PrimaryCredential   *Credential
	StandbyDbUniqueName string
	StandbyLogDiskSize  int64
	StandbyDbDomain     string
	BackupGcsPath       string
	LroInput            *LROInput
}

// CreateStandby creates a standby database.
func CreateStandby(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req CreateStandbyRequest) (*lropb.Operation, error) {
	klog.InfoS("config_agent_helpers/CreateStandby",
		"namespace", namespace,
		"instName", instName,
		"primaryHost", req.PrimaryHost,
		"primaryPort", req.PrimaryPort,
		"primaryService", req.PrimaryService,
		"primaryUser", req.PrimaryUser,
		"standbyDbUniqueName", req.StandbyDbUniqueName,
	)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/CreateStandby: failed to create database daemon dbdClient: %v", err)
	}
	defer closeConn()

	sa := secret.NewGSMSecretAccessor(
		req.PrimaryCredential.GetGsmSecretReference().ProjectId,
		req.PrimaryCredential.GetGsmSecretReference().SecretId,
		req.PrimaryCredential.GetGsmSecretReference().Version,
	)
	defer sa.Clear()

	primaryDB := &standby.Primary{
		Host:             req.PrimaryHost,
		Port:             int(req.PrimaryPort),
		Service:          req.PrimaryService,
		User:             req.PrimaryUser,
		PasswordAccessor: sa,
	}

	standbyDB := &standby.Standby{
		DBUniqueName: req.StandbyDbUniqueName,
		Port:         consts.SecureListenerPort,
		DBDomain:     req.StandbyDbDomain,
		LogDiskSize:  req.StandbyLogDiskSize,
	}

	lro, err := standby.CreateStandby(ctx, primaryDB, standbyDB, req.BackupGcsPath, req.LroInput.OperationId, dbClient)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/CreateStandby: failed to create standby: %v", err)
	}

	return lro, nil
}

type SetUpDataGuardRequest struct {
	PrimaryHost         string
	PrimaryPort         int32
	PrimaryService      string
	PrimaryUser         string
	PrimaryCredential   *Credential
	StandbyDbUniqueName string
	StandbyHost         string
	PasswordFileGcsPath string
}

// SetUpDataGuard updates Data Guard configuration.
func SetUpDataGuard(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req SetUpDataGuardRequest) error {
	klog.InfoS("config_agent_helpers/SetupDataGuard",
		"namespace", namespace,
		"instName", instName,
		"primaryHost", req.PrimaryHost,
		"primaryPort", req.PrimaryPort,
		"primaryService", req.PrimaryService,
		"primaryUser", req.PrimaryUser,
		"standbyDbUniqueName", req.StandbyDbUniqueName,
		"standbyHost", req.StandbyHost,
	)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return fmt.Errorf("config_agent_helpers/SetupDataGuard: failed to create database daemon dbdClient: %v", err)
	}
	defer closeConn()

	sa := secret.NewGSMSecretAccessor(
		req.PrimaryCredential.GetGsmSecretReference().ProjectId,
		req.PrimaryCredential.GetGsmSecretReference().SecretId,
		req.PrimaryCredential.GetGsmSecretReference().Version,
	)
	defer sa.Clear()

	primaryDB := &standby.Primary{
		Host:             req.PrimaryHost,
		Port:             int(req.PrimaryPort),
		Service:          req.PrimaryService,
		User:             req.PrimaryUser,
		PasswordAccessor: sa,
	}

	standbyDB := &standby.Standby{
		DBUniqueName: req.StandbyDbUniqueName,
		Host:         req.StandbyHost,
		Port:         consts.SecureListenerPort,
	}

	if err := standby.SetUpDataGuard(ctx, primaryDB, standbyDB, req.PasswordFileGcsPath, dbClient); err != nil {
		return fmt.Errorf("failed to set up Data Guard: %v", err)
	}

	return nil
}

type PromoteStandbyRequest struct {
	PrimaryHost         string
	PrimaryPort         int32
	PrimaryService      string
	PrimaryUser         string
	PrimaryCredential   *Credential
	StandbyDbUniqueName string
	StandbyHost         string
}

// PromoteStandby promotes standby database to primary.
func PromoteStandby(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req PromoteStandbyRequest) error {
	klog.InfoS("config_agent_helpers/PromoteStandby",
		"namespace", namespace,
		"instName", instName,
		"primaryHost", req.PrimaryHost,
		"primaryPort", req.PrimaryPort,
		"primaryService", req.PrimaryService,
		"primaryUser", req.PrimaryUser,
		"standbyDbUniqueName", req.StandbyDbUniqueName,
		"standbyHost", req.StandbyHost,
	)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return fmt.Errorf("config_agent_helpers/PromoteStandby: failed to create database daemon dbdClient: %v", err)
	}
	defer closeConn()

	sa := secret.NewGSMSecretAccessor(
		req.PrimaryCredential.GetGsmSecretReference().ProjectId,
		req.PrimaryCredential.GetGsmSecretReference().SecretId,
		req.PrimaryCredential.GetGsmSecretReference().Version,
	)
	defer sa.Clear()

	primaryDB := &standby.Primary{
		Host:             req.PrimaryHost,
		Port:             int(req.PrimaryPort),
		Service:          req.PrimaryService,
		User:             req.PrimaryUser,
		PasswordAccessor: sa,
	}

	standbyDB := &standby.Standby{
		DBUniqueName: req.StandbyDbUniqueName,
	}

	if err := standby.PromoteStandby(ctx, primaryDB, standbyDB, dbClient); err != nil {
		return fmt.Errorf("failed to promote standby: %v", err)
	}

	return nil
}

type DataGuardStatusRequest struct {
	StandbyDbUniqueName string
}

type DataGuardStatusResponse struct {
	Output []string
}

// DataGuardStatus returns Data Guard configuration status and standby DB status.
func DataGuardStatus(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req DataGuardStatusRequest) (*DataGuardStatusResponse, error) {
	klog.InfoS("config_agent_helpers/DataGuardStatus", "namespace", namespace, "instName", instName, "standbyDbUniqueName", req.StandbyDbUniqueName)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, fmt.Errorf("config_agent_helpers/DataGuardStatus: failed to create database daemon dbdClient: %v", err)
	}
	defer closeConn()

	output, err := standby.DataGuardStatus(ctx, req.StandbyDbUniqueName, dbClient)
	return &DataGuardStatusResponse{
		Output: output,
	}, err
}
