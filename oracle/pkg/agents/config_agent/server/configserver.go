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

// Package configagent implements Config Agent gRPC interface.
package configagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"github.com/golang/protobuf/ptypes/empty"
	secretmanagerpb "google.golang.org/genproto/googleapis/cloud/secretmanager/v1"
	lropb "google.golang.org/genproto/googleapis/longrunning"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/backup"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common/sql"
	pb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/database/provision"
)

const (
	version      = "12.2"
	pdbAdmin     = "GPDB_ADMIN"
	gsmSecretStr = "projects/%s/secrets/%s/versions/%s"
)

var (
	newDBDClient = func(ctx context.Context, server *ConfigServer) (dbdpb.DatabaseDaemonClient, func() error, error) {
		conn, err := common.DatabaseDaemonDialService(ctx, fmt.Sprintf("%s:%d", server.DBService, server.DBPort), grpc.WithBlock())
		if err != nil {
			return nil, func() error { return nil }, err
		}
		return dbdpb.NewDatabaseDaemonClient(conn), conn.Close, nil
	}

	newGsmClient = func(ctx context.Context) (*secretmanager.Client, func() error, error) {
		client, err := secretmanager.NewClient(ctx)
		if err != nil {
			return nil, func() error { return nil }, err
		}
		return client, client.Close, nil
	}
)

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

// ConfigServer represents a ConfigAgentServer
type ConfigServer struct {
	*pb.UnimplementedConfigAgentServer
	DBService string
	DBPort    int
}

// CheckStatus runs a requested set of state checks.
// The Instance state check consists of:
//   - checking the provisioning done file.
//   - running a CDB connection test via DB Daemon.
func (s *ConfigServer) CheckStatus(ctx context.Context, req *pb.CheckStatusRequest) (*pb.CheckStatusResponse, error) {
	klog.InfoS("configagent/CheckStatus", "req", req)

	switch req.GetCheckStatusType() {
	case pb.CheckStatusRequest_INSTANCE:
		klog.InfoS("configagent/CheckStatus: running a Database Instance status check...")
	default:
		return &pb.CheckStatusResponse{}, fmt.Errorf("configagent/CheckStatus: unsupported in this release check status type of %v", req.GetCheckStatusType())
	}

	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/CheckStatus: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	klog.V(1).InfoS("configagent/CheckStatus", "client", client)

	resp, err := client.FileExists(ctx, &dbdpb.FileExistsRequest{Name: consts.ProvisioningDoneFile})
	if err != nil {
		return nil, fmt.Errorf("configagent/CheckStatus: failed to check a provisioning file: %v", err)
	}

	if !resp.Exists {
		klog.InfoS("configagent/CheckStatus: provisioning file NOT found")
		return &pb.CheckStatusResponse{Status: "InProgress"}, nil
	}
	klog.InfoS("configagent/CheckStatus: provisioning file found")

	if _, err = client.CheckDatabaseState(ctx, &dbdpb.CheckDatabaseStateRequest{IsCdb: true, DatabaseName: req.GetCdbName(), DbDomain: req.GetDbDomain()}); err != nil {
		return nil, fmt.Errorf("configagent/CheckStatus: failed to check a Database Instance state: %v", err)
	}
	klog.InfoS("configagent/CheckStatus: Database Instance is up and running")

	pdbCheckCmd := []string{"select open_mode, restricted from v$pdbs"}
	resp2, err := client.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: pdbCheckCmd, Suppress: false})
	if err != nil {
		return nil, fmt.Errorf("configagent/CheckStatus: failed to get a list of available PDBs: %v", err)
	}
	klog.InfoS("configagent/CheckStatus", "PDB query response", resp2)

	return &pb.CheckStatusResponse{Status: "Ready"}, nil
}

// PhysicalRestore restores an RMAN backup (downloaded from GCS).
func (s *ConfigServer) PhysicalRestore(ctx context.Context, req *pb.PhysicalRestoreRequest) (*lropb.Operation, error) {
	klog.InfoS("configagent/PhysicalRestore", "req", req)

	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/PhysicalRestore: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	klog.InfoS("configagent/PhysicalRestore", "client", client)

	return backup.PhysicalRestore(ctx, &backup.Params{
		Client:       client,
		InstanceName: req.GetInstanceName(),
		CDBName:      req.CdbName,
		DOP:          req.GetDop(),
		LocalPath:    req.GetLocalPath(),
		GCSPath:      req.GetGcsPath(),
		OperationID:  req.GetLroInput().GetOperationId(),
	})
}

// VerifyPhysicalBackup verifies the existence of physical backup.
func (s *ConfigServer) VerifyPhysicalBackup(ctx context.Context, req *pb.VerifyPhysicalBackupRequest) (*pb.VerifyPhysicalBackupResponse, error) {
	klog.InfoS("configagent/VerifyPhysicalBackup", "req", req)
	dbdClient, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/VerifyPhysicalBackup: failed to create a database daemon dbdClient: %v", err)
	}
	defer closeConn()
	if _, err := dbdClient.DownloadDirectoryFromGCS(ctx, &dbdpb.DownloadDirectoryFromGCSRequest{
		GcsPath:               req.GetGcsPath(),
		AccessPermissionCheck: true,
	}); err != nil {
		return &pb.VerifyPhysicalBackupResponse{ErrMsgs: []string{err.Error()}}, nil
	}
	return &pb.VerifyPhysicalBackupResponse{}, nil
}

// PhysicalBackup starts an RMAN backup and stores it in the GCS bucket provided.
func (s *ConfigServer) PhysicalBackup(ctx context.Context, req *pb.PhysicalBackupRequest) (*lropb.Operation, error) {
	klog.InfoS("configagent/PhysicalBackup", "req", req)

	var granularity string
	switch req.BackupSubType {
	case pb.PhysicalBackupRequest_INSTANCE:
		granularity = "database"
	case pb.PhysicalBackupRequest_DATABASE:
		if req.GetBackupItems() == nil {
			return &lropb.Operation{}, fmt.Errorf("configagent/PhysicalBackup: failed a pre-flight check: a PDB backup is requested, but no PDB name(s) given")
		}

		granularity = "pluggable database "
		for i, pdb := range req.GetBackupItems() {
			if i == 0 {
				granularity += pdb
			} else {
				granularity += ", "
				granularity += pdb
			}
		}
	default:
		return &lropb.Operation{}, fmt.Errorf("configagent/PhysicalBackup: unsupported in this release sub backup type of %v", req.BackupSubType)
	}
	klog.InfoS("configagent/PhysicalBackup", "granularity", granularity)

	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/PhysicalBackup: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	klog.InfoS("configagent/PhysicalBackup", "client", client)

	sectionSize := resource.NewQuantity(int64(req.GetSectionSize()), resource.DecimalSI)
	return backup.PhysicalBackup(ctx, &backup.Params{
		Client:       client,
		Granularity:  granularity,
		Backupset:    req.GetBackupset(),
		CheckLogical: req.GetCheckLogical(),
		Compressed:   req.GetCompressed(),
		DOP:          req.GetDop(),
		Level:        req.GetLevel(),
		Filesperset:  req.GetFilesperset(),
		SectionSize:  *sectionSize,
		LocalPath:    req.GetLocalPath(),
		GCSPath:      req.GetGcsPath(),
		OperationID:  req.GetLroInput().GetOperationId(),
	})
}

// CreateCDB creates a CDB using dbca.
func (s *ConfigServer) CreateCDB(ctx context.Context, req *pb.CreateCDBRequest) (*lropb.Operation, error) {
	klog.InfoS("configagent/CreateCDB", "req", req)
	dbdClient, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateCDB: failed to create database daemon dbdClient: %v", err)
	}
	defer closeConn()

	lro, err := dbdClient.CreateCDBAsync(ctx, &dbdpb.CreateCDBAsyncRequest{
		SyncRequest: &dbdpb.CreateCDBRequest{
			OracleHome:       req.GetOracleHome(),
			DatabaseName:     req.GetSid(),
			Version:          req.GetVersion(),
			DbUniqueName:     req.GetDbUniqueName(),
			CharacterSet:     req.GetCharacterSet(),
			MemoryPercent:    req.GetMemoryPercent(),
			AdditionalParams: req.GetAdditionalParams(),
			DbDomain:         req.GetDbDomain(),
		},
		LroInput: &dbdpb.LROInput{OperationId: req.GetLroInput().GetOperationId()},
	})
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateCDB: failed to create CDB: %v", err)
	}

	klog.InfoS("configagent/CreateCDB successfully completed")
	return lro, nil
}

// CreateListener invokes dbdaemon.CreateListener.
func (s *ConfigServer) CreateListener(ctx context.Context, req *pb.CreateListenerRequest) (*pb.CreateListenerResponse, error) {
	klog.InfoS("configagent/CreateListener", "req", req)
	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateListener: failed to create listener: %v", err)
	}
	defer closeConn()
	klog.InfoS("configagent/CreateListener", "client", client)

	_, err = client.CreateListener(ctx, &dbdpb.CreateListenerRequest{
		DatabaseName: req.Name,
		Port:         int32(req.Port),
		Protocol:     req.GetProtocol(),
		OracleHome:   req.GetOracleHome(),
		DbDomain:     req.GetDbDomain(),
	})
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateListener: error while creating listener: %v", err)
	}
	return &pb.CreateListenerResponse{}, nil
}

// CreateDatabase creates PDB as requested.
func (s *ConfigServer) CreateDatabase(ctx context.Context, req *pb.CreateDatabaseRequest) (*pb.CreateDatabaseResponse, error) {
	klog.InfoS("configagent/CreateDatabase", "req", req)

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
			return nil, fmt.Errorf("configagent/CreateDatabase: failed to retrieve secret from Google Secret Manager: %v", err)
		}
	}

	p, err := buildPDB(req.CdbName, req.Name, pwd, version, consts.ListenerNames, true)
	if err != nil {
		return nil, err
	}

	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateDatabase: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	klog.InfoS("configagent/CreateDatabase", "client", client)

	_, err = client.CheckDatabaseState(ctx, &dbdpb.CheckDatabaseStateRequest{IsCdb: true, DatabaseName: req.GetCdbName(), DbDomain: req.GetDbDomain()})
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateDatabase: failed to check a CDB state: %v", err)
	}
	klog.InfoS("configagent/CreateDatabase: pre-flight check#1: CDB is up and running")

	pdbCheckCmd := []string{fmt.Sprintf("select open_mode, restricted from v$pdbs where name = '%s'", sql.StringParam(p.pluggableDatabaseName))}
	resp, err := client.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: pdbCheckCmd, Suppress: false})
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateDatabase: failed to check if a PDB called %s already exists: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("configagent/CreateDatabase pre-flight check#2", "pdb", p.pluggableDatabaseName, "resp", resp)

	if resp.Msg != nil {
		if toUpdateGsmAdminPwd || toUpdatePlaintextAdminPwd {
			sqls := append([]string{sql.QuerySetSessionContainer(p.pluggableDatabaseName)}, []string{sql.QueryAlterUser(pdbAdmin, pwd)}...)
			if _, err := client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
				Commands: sqls,
			}); err != nil {
				return nil, fmt.Errorf("failed to alter user %s: %v", pdbAdmin, err)
			}
			klog.InfoS("configagent/CreateDatabase update pdb admin user succeeded", "user", pdbAdmin)
			return &pb.CreateDatabaseResponse{Status: "AdminUserSyncCompleted"}, nil
		}
		klog.InfoS("configagent/CreateDatabase pre-flight check#2", "pdb", p.pluggableDatabaseName, "respMsg", resp.Msg)
		return &pb.CreateDatabaseResponse{Status: "AlreadyExists"}, nil
	}
	klog.InfoS("configagent/CreateDatabase pre-flight check#2: pdb doesn't exist, proceeding to create", "pdb", p.pluggableDatabaseName)

	cdbDir := fmt.Sprintf(consts.DataDir, consts.DataMount, req.GetCdbName())
	pdbDir := filepath.Join(cdbDir, strings.ToUpper(req.GetName()))
	toCreate := []string{
		fmt.Sprintf("%s/data", pdbDir),
		fmt.Sprintf("%s/%s", pdbDir, consts.DpdumpDir.Linux),
		fmt.Sprintf("%s/rman", consts.OracleBase),
	}
	for _, d := range toCreate {
		if _, err := client.CreateDir(ctx, &dbdpb.CreateDirRequest{
			Path: d,
			Perm: 0760,
		}); err != nil {
			return nil, fmt.Errorf("failed to create a PDB dir %q: %v", d, err)
		}
	}

	pdbCmd := []string{sql.QueryCreatePDB(p.pluggableDatabaseName, pdbAdmin, p.pluggableAdminPasswd, p.dataFilesDir, p.defaultTablespace, p.defaultTablespaceDatafile, p.fileConvertFrom, p.fileConvertTo)}
	_, err = client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: pdbCmd, Suppress: false})
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateDatabase: failed to create a PDB %s: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("configagent/CreateDatabase create a PDB Done", "pdb", p.pluggableDatabaseName)

	pdbOpen := []string{fmt.Sprintf("alter pluggable database %s open read write", sql.MustBeObjectName(p.pluggableDatabaseName))}
	_, err = client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: pdbOpen, Suppress: false})
	if err != nil {
		return nil, fmt.Errorf("configagent/CreatePDBDatabase: PDB %s open failed: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("configagent/CreateDatabase PDB open", "pdb", p.pluggableDatabaseName)

	_, err = client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{
		sql.QuerySetSessionContainer(p.pluggableDatabaseName),
		sql.QueryGrantPrivileges("create session, dba", pdbAdmin),
		sql.QueryGrantPrivileges("create session, resource, datapump_imp_full_database, datapump_exp_full_database, unlimited tablespace", consts.PDBLoaderUser),
	}, Suppress: false})
	if err != nil {
		// Until we have a proper error handling, just log an error here.
		klog.ErrorS(err, "configagent/CreateDatabase: failed to create a PDB_ADMIN user and/or PDB loader user")
	}
	klog.InfoS("configagent/CreateDatabase: created PDB_ADMIN and PDB Loader users")

	// Separate out the directory treatment for the ease of troubleshooting.
	_, err = client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{
		sql.QuerySetSessionContainer(p.pluggableDatabaseName),
		sql.QueryCreateDir(consts.DpdumpDir.Oracle, filepath.Join(p.pathPrefix, consts.DpdumpDir.Linux)),
		sql.QueryGrantPrivileges(fmt.Sprintf("read,write on directory %s", consts.DpdumpDir.Oracle), consts.PDBLoaderUser),
	}, Suppress: false})
	if err != nil {
		klog.ErrorS(err, "configagent/CreateDatabase: failed to create a Data Pump directory", "datapumpDir", consts.DpdumpDir)
	}
	klog.InfoS("configagent/CreateDatabase: DONE", "pdb", p.pluggableDatabaseName)

	return &pb.CreateDatabaseResponse{Status: "Ready"}, nil
}

// CreateUsers creates users as requested.
func (s *ConfigServer) CreateUsers(ctx context.Context, req *pb.CreateUsersRequest) (*pb.CreateUsersResponse, error) {
	// UsersChanged is called before this function by caller (db controller) to check if
	// the users requested are already existing.
	// Thus no duplicated list user check is performed here.
	klog.InfoS("configagent/CreateUsers", "req", req)

	p, err := buildPDB(req.GetCdbName(), req.GetPdbName(), "", version, consts.ListenerNames, true)
	if err != nil {
		return nil, err
	}

	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateUsers: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	klog.InfoS("configagent/CreateUsers", "client", client)

	_, err = client.CheckDatabaseState(ctx, &dbdpb.CheckDatabaseStateRequest{IsCdb: true, DatabaseName: req.GetCdbName(), DbDomain: req.GetDbDomain()})
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateUsers: failed to check a CDB state: %v", err)
	}
	klog.InfoS("configagent/CreateUsers: pre-flight check#: CDB is up and running")

	// Separate create users from grants to make troubleshooting easier.
	usersCmd := []string{sql.QuerySetSessionContainer(p.pluggableDatabaseName)}
	usersCmd = append(usersCmd, req.CreateUsersCmd...)
	for _, u := range req.GetUser() {
		if u.PasswordGsmSecretRef != nil && u.Name != "" {
			var pwd string
			pwd, err = AccessSecretVersionFunc(ctx, fmt.Sprintf(gsmSecretStr, u.PasswordGsmSecretRef.ProjectId, u.PasswordGsmSecretRef.SecretId, u.PasswordGsmSecretRef.Version))
			if err != nil {
				return nil, fmt.Errorf("configagent/CreateUsers: failed to retrieve secret from Google Secret Manager: %v", err)
			}
			if _, err = sql.Identifier(pwd); err != nil {
				return nil, fmt.Errorf("configagent/CreateUsers: Google Secret Manager contains an invalid password for user %q: %v", u.Name, err)
			}

			usersCmd = append(usersCmd, sql.QueryCreateUser(u.Name, pwd))
		}
	}
	_, err = client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: usersCmd, Suppress: false})
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateUsers: failed to create users in a PDB %s: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("configagent/CreateUsers: create users in PDB DONE", "pdb", p.pluggableDatabaseName)

	privsCmd := []string{sql.QuerySetSessionContainer(p.pluggableDatabaseName)}
	privsCmd = append(privsCmd, req.GrantPrivsCmd...)
	_, err = client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: privsCmd, Suppress: false})
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateUsers: failed to grant privileges in a PDB %s: %v", p.pluggableDatabaseName, err)
	}
	klog.InfoS("configagent/CreateUsers: DONE", "pdb", p.pluggableDatabaseName)

	return &pb.CreateUsersResponse{Status: "Ready"}, nil
}

// UsersChanged determines whether there is change on users (update/delete/create).
func (s *ConfigServer) UsersChanged(ctx context.Context, req *pb.UsersChangedRequest) (*pb.UsersChangedResponse, error) {
	klog.InfoS("configagent/UsersChanged", "req", req)
	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/UsersChanged: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	us := newUsers(req.GetPdbName(), req.GetUserSpecs())
	toCreate, toUpdate, toDelete, toUpdatePwd, err := us.diff(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("configagent/UsersChanged: failed to get difference between env and spec for users: %v", err)
	}
	var suppressed []*pb.UsersChangedResponse_Suppressed
	for _, du := range toDelete {
		suppressed = append(suppressed, &pb.UsersChangedResponse_Suppressed{
			SuppressType: pb.UsersChangedResponse_DELETE,
			UserName:     du.userName,
			Sql:          du.delete(),
		})
	}
	for _, cu := range toCreate {
		if cu.newPassword == "" {
			suppressed = append(suppressed, &pb.UsersChangedResponse_Suppressed{
				SuppressType: pb.UsersChangedResponse_CREATE,
				UserName:     cu.userName,
			})
		}
	}
	resp := &pb.UsersChangedResponse{
		Changed:    len(toCreate) != 0 || len(toUpdate) != 0 || len(toUpdatePwd) != 0,
		Suppressed: suppressed,
	}
	klog.InfoS("configagent/UsersChanged: DONE", "resp", resp)
	return resp, nil
}

// UpdateUsers update/create users as requested.
func (s *ConfigServer) UpdateUsers(ctx context.Context, req *pb.UpdateUsersRequest) (*pb.UpdateUsersResponse, error) {
	klog.InfoS("configagent/UpdateUsers", "req", req)

	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/UpdateUsers: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	us := newUsers(req.GetPdbName(), req.GetUserSpecs())
	toCreate, toUpdate, _, toUpdatePwd, err := us.diff(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("configagent/UpdateUsers: failed to get difference between env and spec for users: %v", err)
	}
	foundErr := false
	for _, u := range toCreate {
		klog.InfoS("configagent/UpdateUsers", "creating user", u.userName)
		if err := u.create(ctx, client); err != nil {
			klog.ErrorS(err, "failed to create user")
			foundErr = true
		}
	}

	for _, u := range toUpdate {
		klog.InfoS("configagent/UpdateUsers", "updating user", u.userName)
		// we found there is a scenario that role comes with privileges. For example
		// Grant dba role to a user will automatically give unlimited tablespace privilege.
		// Revoke dba role will automatically revoke  unlimited tablespace privilege.
		// thus user update will first update role and then update sys privi.
		if err := u.update(ctx, client, us.databaseRoles); err != nil {
			klog.ErrorS(err, "failed to update user")
			foundErr = true
		}
	}

	for _, u := range toUpdatePwd {
		klog.InfoS("configagent/UpdateUsers", "updating user", u.userName)
		if err := u.updatePassword(ctx, client); err != nil {
			klog.ErrorS(err, "failed to update user password")
			foundErr = true
		}
	}

	if foundErr {
		return nil, errors.New("failed to update users")
	}
	klog.InfoS("configagent/UpdateUsers: DONE")
	return &pb.UpdateUsersResponse{}, nil
}

// GetOperation fetches corresponding lro given operation name.
func (s *ConfigServer) GetOperation(ctx context.Context, req *lropb.GetOperationRequest) (*lropb.Operation, error) {
	klog.InfoS("configagent/GetOperation", "req", req)
	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/GetOperation: failed to create database daemon client: %v", err)
	}
	defer func() { _ = closeConn() }()
	klog.InfoS("configagent/GetOperation", "client", client)

	return client.GetOperation(ctx, req)
}

// DeleteOperation deletes lro given by name.
func (s *ConfigServer) DeleteOperation(ctx context.Context, req *lropb.DeleteOperationRequest) (*empty.Empty, error) {
	klog.InfoS("configagent/DeleteOperation", "req", req)
	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/DeleteOperation: failed to create database daemon client: %v", err)
	}
	defer func() { _ = closeConn() }()
	klog.InfoS("configagent/DeleteOperation", "client", client)

	return client.DeleteOperation(ctx, req)
}

// CreateCDBUser creates CDB user as requested.
func (s *ConfigServer) CreateCDBUser(ctx context.Context, req *pb.CreateCDBUserRequest) (*pb.CreateCDBUserResponse, error) {
	klog.InfoS("configagent/CreateCDBUser", "req", req)

	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateCDBUser: failed to create database daemon client: %v", err)
	}
	defer closeConn()
	klog.InfoS("configagent/CreateCDBUser", "client", client)

	// Separate create users from grants to make troubleshooting easier.
	usersCmd := []string{}
	usersCmd = append(usersCmd, req.CreateUsersCmd...)
	_, err = client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: usersCmd, Suppress: false})
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateDatabase: failed to create users in CDB %s: %v", req.CdbName, err)
	}
	klog.InfoS("configagent/CreateCDBUsers: create users in CDB DONE", req.CdbName)

	privsCmd := []string{}
	privsCmd = append(privsCmd, req.GrantPrivsCmd...)
	_, err = client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: privsCmd, Suppress: false})
	if err != nil {
		return nil, fmt.Errorf("configagent/CreateDatabase: failed to grant privileges in CDB %s: %v", req.CdbName, err)
	}
	klog.InfoS("configagent/CreateCDBUsers: DONE", "pdb", req.CdbName)

	return &pb.CreateCDBUserResponse{Status: "Ready"}, nil
}

// BootstrapStandby performs bootstrap steps for standby instance.
func (s *ConfigServer) BootstrapStandby(ctx context.Context, req *pb.BootstrapStandbyRequest) (*pb.BootstrapStandbyResponse, error) {
	klog.InfoS("configagent/BootstrapStandby", "req", req)
	dbdClient, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/BootstrapStandby: failed to create database daemon client: %v", err)
	}
	defer closeConn()

	// skip if already bootstrapped
	resp, err := dbdClient.FileExists(ctx, &dbdpb.FileExistsRequest{Name: consts.ProvisioningDoneFile})
	if err != nil {
		return nil, fmt.Errorf("configagent/BootstrapStandby: failed to check a provisioning file: %v", err)
	}

	if resp.Exists {
		klog.InfoS("configagent/BootstrapStandby: standby is already provisioned")
		return &pb.BootstrapStandbyResponse{}, nil
	}

	task := provision.NewBootstrapDatabaseTaskForStandby(req.GetCdbName(), req.GetDbdomain(), dbdClient)

	if err := task.Call(ctx); err != nil {
		return nil, fmt.Errorf("configagent/BootstrapStandby: failed to bootstrap standby database : %v", err)
	}
	klog.InfoS("configagent/BootstrapStandby: bootstrap task completed successfully")

	// create listeners
	_, err = s.CreateListener(ctx, &pb.CreateListenerRequest{
		Name:     req.GetCdbName(),
		Port:     consts.SecureListenerPort,
		Protocol: "TCP",
		DbDomain: req.GetDbdomain(),
	})
	if err != nil {
		return nil, fmt.Errorf("configagent/BootstrapStandby: failed to create listener: %v", err)
	}

	if _, err := dbdClient.BootstrapStandby(ctx, &dbdpb.BootstrapStandbyRequest{
		CdbName: req.GetCdbName(),
	}); err != nil {
		return nil, fmt.Errorf("configagent/BootstrapStandby: dbdaemon failed to bootstrap standby: %v", err)
	}
	klog.InfoS("configagent/BootstrapStandby: dbdaemon completed bootstrap standby successfully")

	_, err = dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{consts.OpenPluggableDatabaseSQL}, Suppress: false})
	if err != nil {
		return nil, fmt.Errorf("configagent/BootstrapStandby: failed to open pluggable database: %v", err)
	}

	// fetch existing pdbs/users to create database resources for
	knownPDBsResp, err := dbdClient.KnownPDBs(ctx, &dbdpb.KnownPDBsRequest{
		IncludeSeed: false,
		OnlyOpen:    false,
	})
	if err != nil {
		return nil, fmt.Errorf("configagent/BootstrapStandby: dbdaemon failed to get KnownPDBs: %v", err)
	}

	var migratedPDBs []*pb.BootstrapStandbyResponse_PDB
	for _, pdb := range knownPDBsResp.GetKnownPdbs() {
		us := newUsers(pdb, []*pb.User{})
		_, _, existingUsers, _, err := us.diff(ctx, dbdClient)
		if err != nil {
			return nil, fmt.Errorf("configagent/BootstrapStandby: failed to get existing users for pdb %v: %v", pdb, err)
		}
		var migratedUsers []*pb.BootstrapStandbyResponse_User
		for _, u := range existingUsers {
			migratedUsers = append(migratedUsers, &pb.BootstrapStandbyResponse_User{
				UserName: u.GetUserName(),
				Privs:    u.GetUserEnvPrivs(),
			})
		}
		migratedPDBs = append(migratedPDBs, &pb.BootstrapStandbyResponse_PDB{
			PdbName: strings.ToLower(pdb),
			Users:   migratedUsers,
		})
	}

	klog.InfoS("configagent/BootstrapStandby: fetch existing pdbs and users successfully.", "MigratedPDBs", migratedPDBs)
	return &pb.BootstrapStandbyResponse{Pdbs: migratedPDBs}, nil
}

// BootstrapDatabase bootstrap a CDB after creation or restore.
func (s *ConfigServer) BootstrapDatabase(ctx context.Context, req *pb.BootstrapDatabaseRequest) (*lropb.Operation, error) {
	klog.InfoS("configagent/BootstrapDatabase", "req", req)

	dbdClient, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/BootstrapDatabase: failed to create database daemon client: %v", err)
	}
	defer closeConn()

	resp, err := dbdClient.FileExists(ctx, &dbdpb.FileExistsRequest{Name: consts.ProvisioningDoneFile})
	if err != nil {
		return nil, fmt.Errorf("configagent/BootstrapDatabase: failed to check a provisioning file: %v", err)
	}

	if resp.Exists {
		klog.InfoS("configagent/BootstrapDatabase: provisioning file found, skip bootstrapping")
		return &lropb.Operation{Done: true}, nil
	}

	switch req.GetMode() {
	case pb.BootstrapDatabaseRequest_ProvisionUnseeded:
		task := provision.NewBootstrapDatabaseTaskForUnseeded(req.CdbName, req.DbUniqueName, req.Dbdomain, dbdClient)

		if err := task.Call(ctx); err != nil {
			return nil, fmt.Errorf("configagent/BootstrapDatabase: failed to bootstrap database : %v", err)
		}
	case pb.BootstrapDatabaseRequest_ProvisionSeeded:
		lro, err := dbdClient.BootstrapDatabaseAsync(ctx, &dbdpb.BootstrapDatabaseAsyncRequest{
			SyncRequest: &dbdpb.BootstrapDatabaseRequest{
				CdbName:  req.GetCdbName(),
				DbDomain: req.GetDbdomain(),
			},
			LroInput: &dbdpb.LROInput{OperationId: req.GetLroInput().GetOperationId()},
		})
		if err != nil {
			return nil, fmt.Errorf("configagent/BootstrapDatabase: error while call dbdaemon/BootstrapDatabase: %v", err)
		}
		return lro, nil
	default:
	}

	if _, err = dbdClient.CreateListener(ctx, &dbdpb.CreateListenerRequest{
		DatabaseName: req.GetCdbName(),
		Port:         consts.SecureListenerPort,
		Protocol:     "TCP",
		DbDomain:     req.GetDbdomain(),
	}); err != nil {
		return nil, fmt.Errorf("configagent/BootstrapDatabase: error while creating listener: %v", err)
	}

	if _, err = dbdClient.CreateFile(ctx, &dbdpb.CreateFileRequest{
		Path: consts.ProvisioningDoneFile,
	}); err != nil {
		return nil, fmt.Errorf("configagent/BootstrapDatabase: error while creating provisioning done file: %v", err)
	}

	return &lropb.Operation{Done: true}, nil
}

// DataPumpImport imports data dump file provided in GCS path.
func (s *ConfigServer) DataPumpImport(ctx context.Context, req *pb.DataPumpImportRequest) (*lropb.Operation, error) {
	klog.InfoS("configagent/DataPumpImport", "req", req)

	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/DataPumpImport: failed to create database daemon client: %v", err)
	}
	defer func() { _ = closeConn() }()

	return client.DataPumpImportAsync(ctx, &dbdpb.DataPumpImportAsyncRequest{
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
			OperationId: req.GetLroInput().GetOperationId(),
		},
	})
}

// DataPumpExport exports data pump file to GCS path provided.
func (s *ConfigServer) DataPumpExport(ctx context.Context, req *pb.DataPumpExportRequest) (*lropb.Operation, error) {

	klog.InfoS("configagent/DataPumpExport", "req", req)

	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/DataPumpExport: failed to create database daemon client: %v", err)
	}
	defer func() { _ = closeConn() }()

	return client.DataPumpExportAsync(ctx, &dbdpb.DataPumpExportAsyncRequest{
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
			OperationId: req.GetLroInput().GetOperationId(),
		},
	})
}

// SetParameter sets database parameter as requested.
func (s *ConfigServer) SetParameter(ctx context.Context, req *pb.SetParameterRequest) (*pb.SetParameterResponse, error) {
	klog.InfoS("configagent/SetParameter", "req", req)
	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/SetParameter: failed to create dbdClient: %v", err)
	}
	defer closeConn()
	klog.InfoS("configagent/SetParameter", "client", client)

	// Fetch parameter type
	// The possible values are IMMEDIATE FALSE DEFERRED
	query := fmt.Sprintf("select issys_modifiable from v$parameter where name='%s'", sql.StringParam(req.Key))
	paramType, err := fetchAndParseSingleResultQuery(ctx, client, query)
	if err != nil {
		return nil, fmt.Errorf("configagent/SetParameter: error while inferring parameter type: %v", err)
	}
	query = fmt.Sprintf("select type from v$parameter where name='%s'", sql.StringParam(req.Key))
	paramDatatype, err := fetchAndParseSingleResultQuery(ctx, client, query)
	if err != nil {
		return nil, fmt.Errorf("configagent/SetParameter: error while inferring parameter data type: %v", err)
	}
	// string parameters need to be quoted,
	// those have type 2, see the link for the parameter types description
	// https://docs.oracle.com/database/121/REFRN/GUID-C86F3AB0-1191-447F-8EDF-4727D8693754.htm
	isStringParam := paramDatatype == "2"
	command, err := sql.QuerySetSystemParameterNoPanic(req.Key, req.Value, isStringParam)
	if err != nil {
		return nil, fmt.Errorf("configagent/SetParameter: error constructing set parameter query: %v", err)
	}

	isStatic := false
	if paramType == "FALSE" {
		klog.InfoS("configagent/SetParameter", "parameter_type", "STATIC")
		command = fmt.Sprintf("%s scope=spfile", command)
		isStatic = true
	}

	_, err = client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{command},
		Suppress: false,
	})
	if err != nil {
		return nil, fmt.Errorf("configagent/SetParameter: error while executing parameter command: %q", command)
	}
	return &pb.SetParameterResponse{Static: isStatic}, nil
}

// GetParameterTypeValue returns parameters' type and value by querying DB.
func (s *ConfigServer) GetParameterTypeValue(ctx context.Context, req *pb.GetParameterTypeValueRequest) (*pb.GetParameterTypeValueResponse, error) {
	klog.InfoS("configagent/GetParameterTypeValue", "req", req)
	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/GetParameterTypeValue: failed to create dbdClient: %v", err)
	}
	defer closeConn()
	klog.InfoS("configagent/GetParameterTypeValue", "client", client)

	types := []string{}
	values := []string{}

	for _, key := range req.GetKeys() {
		query := fmt.Sprintf("select issys_modifiable from v$parameter where name='%s'", sql.StringParam(key))
		value, err := fetchAndParseSingleResultQuery(ctx, client, query)
		if err != nil {
			return nil, fmt.Errorf("configagent/GetParameterTypeValue: error while fetching type for %v: %v", key, err)
		}
		types = append(types, value)
	}
	for _, key := range req.GetKeys() {
		query := fmt.Sprintf("select value from v$parameter where name='%s'", sql.StringParam(key))
		value, err := fetchAndParseSingleResultQuery(ctx, client, query)
		if err != nil {
			return nil, fmt.Errorf("configagent/GetParameterTypeValue: error while fetching value for %v: %v", key, err)
		}
		values = append(values, value)
	}

	return &pb.GetParameterTypeValueResponse{Types: types, Values: values}, nil
}

// BounceDatabase shutdown/startup the database as requested.
func (s *ConfigServer) BounceDatabase(ctx context.Context, req *pb.BounceDatabaseRequest) (*pb.BounceDatabaseResponse, error) {
	klog.InfoS("configagent/BounceDatabase", "req", req)
	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/BounceDatabase: failed to create dbdClient: %v", err)
	}
	defer closeConn()

	klog.InfoS("configagent/BounceDatabase", "client", client)
	_, err = client.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
		DatabaseName: req.Sid,
		Option:       "immediate",
	})
	if err != nil {
		return nil, fmt.Errorf("configagent/BounceDatabase: error while shutting db: %v", err)
	}
	klog.InfoS("configagent/BounceDatabase: shutdown successful")

	_, err = client.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:         dbdpb.BounceDatabaseRequest_STARTUP,
		DatabaseName:      req.Sid,
		AvoidConfigBackup: req.AvoidConfigBackup,
	})
	if err != nil {
		return nil, fmt.Errorf("configagent/BounceDatabase: error while starting db: %v", err)
	}
	klog.InfoS("configagent/BounceDatabase: startup successful")
	return &pb.BounceDatabaseResponse{}, err
}

// RecoverConfigFile generates the binary spfile from the human readable backup pfile.
func (s *ConfigServer) RecoverConfigFile(ctx context.Context, req *pb.RecoverConfigFileRequest) (*pb.RecoverConfigFileResponse, error) {
	klog.InfoS("configagent/RecoverConfigFile", "req", req)
	client, closeConn, err := newDBDClient(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("configagent/RecoverConfigFile: failed to create dbdClient: %v", err)
	}
	defer closeConn()

	if _, err := client.RecoverConfigFile(ctx, &dbdpb.RecoverConfigFileRequest{CdbName: req.CdbName}); err != nil {
		klog.InfoS("configagent/RecoverConfigFile: error while recovering config file: err", "err", err)
		return nil, fmt.Errorf("configagent/RecoverConfigFile: failed to recover config file due to: %v", err)
	}
	klog.InfoS("configagent/RecoverConfigFile: config file backup successful")

	return &pb.RecoverConfigFileResponse{}, err
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

// FetchServiceImageMetaData fetches the image metadata from the service image.
func (s *ConfigServer) FetchServiceImageMetaData(ctx context.Context, req *pb.FetchServiceImageMetaDataRequest) (*pb.FetchServiceImageMetaDataResponse, error) {
	dbdClient, closeConn, err := newDBDClient(ctx, s)
	defer func() { _ = closeConn() }()
	if err != nil {
		return nil, fmt.Errorf("configagent/FetchServiceImageMetaData: failed to create database daemon client: %w", err)
	}
	metaData, err := dbdClient.FetchServiceImageMetaData(ctx, &dbdpb.FetchServiceImageMetaDataRequest{})
	if err != nil {
		return &pb.FetchServiceImageMetaDataResponse{}, nil
	}
	return &pb.FetchServiceImageMetaDataResponse{Version: metaData.Version, CdbName: metaData.CdbName, OracleHome: metaData.OracleHome, SeededImage: metaData.SeededImage}, nil
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
