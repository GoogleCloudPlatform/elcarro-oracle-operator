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
	klog.InfoS("oracle/PhysicalRestore", "backupDir", backupDir)

	if params.GCSPath != "" {
		backupDir = consts.RMANStagingDir
		downloadReq := &dbdpb.DownloadDirectoryFromGCSRequest{
			GcsPath:   params.GCSPath,
			LocalPath: backupDir,
		}
		klog.InfoS("oracle/PhysicalRestore", "restore from gcs, downloadReq", downloadReq)

		if _, err := params.Client.DownloadDirectoryFromGCS(ctx, downloadReq); err != nil {
			return nil, fmt.Errorf("failed to download rman backup from GCS bucket %s", err)
		}
	}

	// Files stored in default format
	// /u03/app/oracle/rman/id/DB_UNIQUE_NAME/backupset/2020_03_13/o1_mf_nnsnf_TAG20200313T214926_h6qzz6g6_.bkp
	resp, err := params.Client.ReadDir(ctx, &dbdpb.ReadDirRequest{
		Path:      backupDir,
		Recursive: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read backup dir: %v", err)
	}
	var spfilesTime []fileTime
	for _, fileInfo := range resp.SubPaths {
		if !fileInfo.IsDir && strings.Contains(fileInfo.Name, "nnsnf") {
			if err := fileInfo.ModTime.CheckValid(); err != nil {
				return nil, fmt.Errorf("failed to convert timestamp: %v", err)
			}
			modTime := fileInfo.ModTime.AsTime()
			spfilesTime = append(spfilesTime, fileTime{name: fileInfo.AbsPath, modTime: modTime})
		}
	}
	if len(spfilesTime) < 1 {
		return nil, fmt.Errorf("failed to find spfile candidates: %d", len(spfilesTime))
	}

	for i, t := range spfilesTime {
		klog.InfoS("spfiles time", "index", i, "name", t.name, "modTime", t.modTime)
	}

	sort.Slice(spfilesTime, func(i, j int) bool {
		return spfilesTime[i].modTime.After(spfilesTime[j].modTime)
	})

	klog.InfoS("oracle/PhysicalRestore: sorted spfiles", "spfilesTime", spfilesTime)

	var ctlfilesTime []fileTime
	for _, fileInfo := range resp.SubPaths {
		if !fileInfo.IsDir && strings.Contains(fileInfo.Name, "ncnnf") {
			if err := fileInfo.ModTime.CheckValid(); err != nil {
				return nil, fmt.Errorf("failed to convert timestamp: %v", err)
			}
			modTime := fileInfo.ModTime.AsTime()
			ctlfilesTime = append(ctlfilesTime, fileTime{name: fileInfo.AbsPath, modTime: modTime})
		}
	}
	if len(ctlfilesTime) < 1 {
		return nil, fmt.Errorf("failed to find controlfile candidates: %d", len(ctlfilesTime))
	}

	sort.Slice(ctlfilesTime, func(i, j int) bool {
		return ctlfilesTime[i].modTime.After(ctlfilesTime[j].modTime)
	})
	klog.InfoS("oracle/PhysicalRestore sorted control files", "ctlFilesTime", ctlfilesTime)

	// Clear spfile and datafile dir.

	spfileLoc := filepath.Join(
		fmt.Sprintf(consts.ConfigDir, consts.DataMount, params.CDBName),
		fmt.Sprintf("spfile%s.ora", params.CDBName),
	)

	if _, err := params.Client.DeleteDir(ctx, &dbdpb.DeleteDirRequest{Path: spfileLoc}); err != nil {
		klog.ErrorS(err, "failed to delete the spfile before restore")
	}

	dataDir := filepath.Join(consts.OracleBase, "oradata", "*")
	if _, err := params.Client.DeleteDir(ctx, &dbdpb.DeleteDirRequest{Path: dataDir}); err != nil {
		klog.ErrorS(err, "failed to delete the data files before restore", "dataDir", dataDir)
	}

	restoreStmt := fmt.Sprintf(restoreStmtTemplate, spfileLoc, spfilesTime[0].name, ctlfilesTime[0].name, channels)

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
