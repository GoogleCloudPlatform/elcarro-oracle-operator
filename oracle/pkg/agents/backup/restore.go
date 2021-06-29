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

// Package backup this file provides the restore and recovery functions from a
// physical backup and is intended to be called from a Config Agent gRPC
// server.
package backup

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	lropb "google.golang.org/genproto/googleapis/longrunning"
	"k8s.io/klog/v2"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

const (
	maxSCNquery = `
				select max(next_change#) as scn
				from v$archived_log
				where resetlogs_id=(
					select resetlogs_id
					from v$database_incarnation
					where status='CURRENT'
				)
	`

	restoreStmtTemplate = `run {
				startup force nomount;
				restore spfile to '%s' from '%s';
				shutdown immediate;
				startup nomount;
				restore controlfile from '%s';
				startup mount;
				%s
				restore database;
				delete foreign archivelog all;
		}
	`

	recoverStmtTemplate = `run {
				recover database until scn %d;
				alter database open resetlogs;
				alter pluggable database all open;
		}
	`
)

type fileTime struct {
	name    string
	modTime time.Time
}

// PhysicalRestore runs an RMAN restore and recovery.
// Presently the recovery process goes up to the last SCN in the last
// archived redo log.
func PhysicalRestore(ctx context.Context, params *Params) (*lropb.Operation, error) {
	klog.InfoS("oracle/PhysicalRestore", "params", params)

	var channels string
	for i := 1; i <= int(params.DOP); i++ {
		channels += fmt.Sprintf(allocateChannel, i)
	}
	klog.InfoS("oracle/PhysicalRestore", "channels", channels)

	backupDir := consts.DefaultRMANDir
	if params.LocalPath != "" {
		backupDir = params.LocalPath
	}

	if params.GCSPath != "" {
		backupDir = consts.RMANStagingDir
		downloadReq := &dbdpb.DownloadDirectoryFromGCSRequest{
			GcsPath:   params.GCSPath,
			LocalPath: backupDir,
		}
		klog.InfoS("oracle/PhysicalRestore", "restore from gcs, downloadReq", downloadReq)

		if _, err := params.Client.DownloadDirectoryFromGCS(ctx, downloadReq); err != nil {
			return nil, fmt.Errorf("PhysicalRestore: failed to download rman backup from GCS bucket %s", err)
		}
	}
	klog.InfoS("oracle/PhysicalRestore", "backupDir", backupDir)

	resp, err := params.Client.ReadDir(ctx, &dbdpb.ReadDirRequest{
		Path:      backupDir,
		Recursive: true,
	})
	if err != nil {
		return nil, fmt.Errorf("PhysicalRestore: failed to read backup dir: %v", err)
	}

	// Files stored in default format:
	// "nnsnf" is used to locate spfile backup piece;
	// "ncnnf" is used to locate control file backup piece;
	latestSpfileBackup, err := findLatestBackupPiece(resp, "nnsnf")
	if err != nil {
		return nil, fmt.Errorf("PhysicalRestore: failed to find latest spfile backup piece: %v", err)
	}
	latestControlfileBackup, err := findLatestBackupPiece(resp, "ncnnf")
	if err != nil {
		return nil, fmt.Errorf("PhysicalRestore: failed to find latest control file backup piece: %v", err)
	}

	// Delete spfile and datafiles.
	if err := deleteFilesForRestore(ctx, params.Client, params.CDBName); err != nil {
		klog.ErrorS(err, "PhysicalRestore: failed to delete the spfile and datafiles before restore")
	}

	// Ensures all required dirs are exist.
	if err := createDirsForRestore(ctx, params.Client, params.CDBName); err != nil {
		return nil, fmt.Errorf("PhysicalRestore: failed to createDirsForRestore: %v", err)
	}

	spfileLoc := filepath.Join(
		fmt.Sprintf(consts.ConfigDir, consts.DataMount, params.CDBName),
		fmt.Sprintf("spfile%s.ora", params.CDBName),
	)
	restoreStmt := fmt.Sprintf(restoreStmtTemplate, spfileLoc, latestSpfileBackup, latestControlfileBackup, channels)

	operation, err := params.Client.PhysicalRestoreAsync(ctx, &dbdpb.PhysicalRestoreAsyncRequest{
		SyncRequest: &dbdpb.PhysicalRestoreRequest{
			RestoreStatement:          restoreStmt,
			LatestRecoverableScnQuery: maxSCNquery,
			RecoverStatementTemplate:  recoverStmtTemplate,
		},
		LroInput: &dbdpb.LROInput{OperationId: params.OperationID},
	})

	if err != nil {
		return nil, fmt.Errorf("oracle/PhysicalRestore: failed to create database restore request: %v", err)
	}
	return operation, nil
}

// findLatestBackupPiece finds the latest modified backup piece whose name contains substr.
func findLatestBackupPiece(readDirResp *dbdpb.ReadDirResponse, substr string) (string, error) {
	var fileTimes []fileTime
	for _, fileInfo := range readDirResp.SubPaths {
		if !fileInfo.IsDir && strings.Contains(fileInfo.Name, substr) {
			if err := fileInfo.ModTime.CheckValid(); err != nil {
				return "", fmt.Errorf("findLatestBackupPiece: failed to convert timestamp: %v", err)
			}
			modTime := fileInfo.ModTime.AsTime()
			fileTimes = append(fileTimes, fileTime{name: fileInfo.AbsPath, modTime: modTime})
		}
	}
	if len(fileTimes) < 1 {
		return "", fmt.Errorf("findLatestBackupPiece: failed to find candidates for substr %s: %d", substr, len(fileTimes))
	}

	for i, t := range fileTimes {
		klog.InfoS(fmt.Sprintf("%s time", substr), "index", i, "name", t.name, "modTime", t.modTime)
	}

	sort.Slice(fileTimes, func(i, j int) bool {
		return fileTimes[i].modTime.After(fileTimes[j].modTime)
	})

	klog.InfoS(fmt.Sprintf("findLatestBackupPiece: sorted %s files", substr), "fileTimes", fileTimes)

	return fileTimes[0].name, nil
}

// Delete spfile and datafiles.
func deleteFilesForRestore(ctx context.Context, dbdClient dbdpb.DatabaseDaemonClient, cdbName string) error {
	spfileLoc := filepath.Join(
		fmt.Sprintf(consts.ConfigDir, consts.DataMount, cdbName),
		fmt.Sprintf("spfile%s.ora", cdbName),
	)

	if _, err := dbdClient.DeleteDir(ctx, &dbdpb.DeleteDirRequest{Path: spfileLoc}); err != nil {
		klog.ErrorS(err, "deleteDirsForRestore: failed to delete the spfile before restore")
	}

	dataDir := filepath.Join(consts.OracleBase, "oradata", "*")
	if _, err := dbdClient.DeleteDir(ctx, &dbdpb.DeleteDirRequest{Path: dataDir}); err != nil {
		klog.ErrorS(err, "deleteDirsForRestore: failed to delete the data files before restore", "dataDir", dataDir)
	}

	return nil
}

// createDirsForRestore ensures all required dirs exist for restore.
func createDirsForRestore(ctx context.Context, dbdClient dbdpb.DatabaseDaemonClient, cdbName string) error {
	toCreate := []string{
		// adump dir
		fmt.Sprintf(consts.OracleBase, "admin", cdbName, "adump"),
		// configfiles dir
		fmt.Sprintf(consts.ConfigDir, consts.DataMount, cdbName),
		// datafiles dir
		fmt.Sprintf(consts.DataDir, consts.DataMount, cdbName),
		// flash dir
		fmt.Sprintf(consts.RecoveryAreaDir, consts.LogMount, cdbName),
	}

	for _, d := range toCreate {
		if _, err := dbdClient.CreateDir(ctx, &dbdpb.CreateDirRequest{
			Path: d,
			Perm: 0760,
		}); err != nil {
			return fmt.Errorf("createDirsForRestore: failed to create dir %q: %v", d, err)
		}
	}
	return nil
}
