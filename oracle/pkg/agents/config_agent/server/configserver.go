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
	"fmt"
	"os"
	"strings"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
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
