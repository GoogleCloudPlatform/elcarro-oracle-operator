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

// Package backup provides physical database backup utility functions intended
// to be called from a Config Agent gRPC server.
package backup

import (
	"context"
	"fmt"

	lropb "google.golang.org/genproto/googleapis/longrunning"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

const (
	allocateChannel = "allocate channel disk%d device type disk;\n"

	// The format of the backup statement template is:
	// 	run {
	//		<initialization statements>
	//		channels
	//		backup
	//			as <compressed> <backupset|image copy>
	//			<check logical>
	//			<filesperset X>
	//			<section size Y>
	//			incremental level <Z>
	//			to destination '<W>'
	// 			<granularity: (database|pluggable database pdb1,pdb2)>
	//		backup...
	//	}
	backupStmtTemplate = `run {
			%s
			%s
			backup
				as %s %s
				%s
				%s
				%s
				incremental level %d
				to destination '%s'
				(%s);
			backup
				to destination '%s'
				(spfile) (current controlfile)
				plus archivelog;
		}
	`
)

// Params that can be passed to PhysicalBackup.
type Params struct {
	InstanceName string
	CDBName      string
	Client       dbdpb.DatabaseDaemonClient
	Granularity  string
	Backupset    bool
	DOP          int32
	CheckLogical bool
	Compressed   bool
	Level        int32
	Filesperset  int32
	SectionSize  resource.Quantity
	LocalPath    string
	GCSPath      string
	OperationID  string
}

// PhysicalBackup takes a physical backup of the oracle database.
func PhysicalBackup(ctx context.Context, params *Params) (*lropb.Operation, error) {
	klog.InfoS("oracle/PhysicalBackup", "params", params)

	var channels string
	for i := 1; i <= int(params.DOP); i++ {
		channels += fmt.Sprintf(allocateChannel, i)
	}
	klog.InfoS("oracle/PhysicalBackup", "channels", channels)

	granularity := "database"
	if params.Granularity != "" {
		granularity = params.Granularity
	}

	backupDir := consts.DefaultRMANDir
	if params.LocalPath != "" {
		backupDir = params.LocalPath
	}
	// for RMAN backup to GCS bucket, first backup to a staging location. Remove staging dir when upload finishes.
	if params.GCSPath != "" {
		backupDir = consts.RMANStagingDir
	}
	klog.InfoS("oracle/PhysicalBackup", "backupDir", backupDir)

	dirInfo := []*dbdpb.CreateDirsRequest_DirInfo{
		{
			Path: backupDir,
			Perm: 0760,
		},
	}

	// Check/create the destination dir if it's different from the default.
	if _, err := params.Client.CreateDirs(ctx, &dbdpb.CreateDirsRequest{
		Dirs: dirInfo,
	}); err != nil {
		return nil, fmt.Errorf("failed to create a backup dir %q: %v", backupDir, err)
	}

	if params.Compressed && !params.Backupset {
		return nil, fmt.Errorf("oracle/PhysicalBackup: failed a pre-flight check: Image Copy type of backup is not compatible with a Compress setting")
	}

	var compressed string
	if params.Compressed {
		compressed = "compressed"
	}

	var backupset string
	if params.Backupset {
		backupset = "backupset"
	} else {
		backupset = "copy"
	}
	klog.InfoS("oracle/PhysicalBackup", "backupset", backupset)

	checklogical := "check logical"
	if !params.CheckLogical {
		checklogical = ""
	}
	klog.InfoS("oracle/PhysicalBackup", "checkLogical", checklogical)

	filesperset := ""
	if params.Filesperset != 0 {
		filesperset = fmt.Sprintf("filesperset %d", params.Filesperset)
	}
	klog.InfoS("oracle/PhysicalBackup", "filesperset", filesperset)

	sectionSize := sectionSize(params.SectionSize)
	klog.InfoS("oracle/PhysicalBackup", "sectionSize", sectionSize)

	// Change the location of RMAN control file snapshot from
	// the container image filesystem to the data disk (<backupDir>/snapcf_<CDB>.f)
	// The default location is '/u01/app/oracle/product/<VERSION>/db/dbs/snapcf_<CDB>.f'
	// and it causes flaky behaviour (ORA-00246) in Oracle 19.3
	initStatement := fmt.Sprintf("CONFIGURE SNAPSHOT CONTROLFILE NAME TO '%s/snapcf_%s.f';", backupDir, params.CDBName)

	backupStmt := fmt.Sprintf(backupStmtTemplate, initStatement, channels, compressed, backupset, checklogical, filesperset, sectionSize, params.Level, backupDir, granularity, backupDir)
	klog.InfoS("oracle/PhysicalBackup", "finalBackupRequest", backupStmt)

	backupReq := &dbdpb.RunRMANAsyncRequest{
		SyncRequest: &dbdpb.RunRMANRequest{Scripts: []string{backupStmt}, GcsPath: params.GCSPath, LocalPath: params.LocalPath, Cmd: consts.RMANBackup},
		LroInput:    &dbdpb.LROInput{OperationId: params.OperationID},
	}
	klog.InfoS("oracle/PhysicalBackup", "backupReq", backupReq)

	operation, err := params.Client.RunRMANAsync(ctx, backupReq)
	if err != nil {
		return nil, fmt.Errorf("oracle/PhysicalBackup: failed to create database backup request: %v", err)
	}
	return operation, nil
}

func sectionSize(sectionSize resource.Quantity) string {
	if sectionSize.IsZero() {
		return ""
	}
	sectionSizeInt64, ok := sectionSize.AsInt64()
	if !ok {
		return ""
	}
	if scaledValue := sectionSizeInt64 / 1_000_000_000; scaledValue > 0 {
		return fmt.Sprintf("section size %dG", scaledValue)
	}
	if scaledValue := sectionSizeInt64 / 1_000_000; scaledValue > 0 {
		return fmt.Sprintf("section size %dM", scaledValue)
	}
	if scaledValue := sectionSizeInt64 / 1_000; scaledValue > 0 {
		return fmt.Sprintf("section size %dK", scaledValue)
	}
	return fmt.Sprintf("section size %d", sectionSizeInt64)
}
