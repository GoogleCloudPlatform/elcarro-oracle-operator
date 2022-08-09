// Copyright 2022 Google LLC
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

package standby

import (
	"bufio"
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	connect "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/util/task"
	lropb "google.golang.org/genproto/googleapis/longrunning"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

const (
	// dbMetaDataListSQL returns the required metadata for creating a EM replica.
	dbMetaDataListSQL = "select value from V$parameter where name in ('compatible', 'db_domain', 'db_name', 'db_unique_name', 'enable_pluggable_database')"

	// dataFilePathListSQL returns the Data file directory list.
	dataFilePathListSQL = "select distinct substr(name,1,length(name)-instr(reverse(name),'/')) as DIRS from V$datafile"

	// logFilePathListSQL returns the Log file directory list.
	logFilePathListSQL = "select distinct substr(member,1,length(member)-instr(reverse(member),'/')) as DIRS from V$logfile"

	createSPFileSQL = "create spfile='%s' from pfile"

	dataFileDirPrefix = "data"

	logFileDirPrefix = "log"
)

type createStandbyTask struct {
	tasks              *task.Tasks
	primary            *Primary
	standby            *Standby
	primaryMetadata    *primaryMetadata
	dataFileDirMapping map[string]string
	logFileDirMapping  map[string]string
	dbdClient          dbdpb.DatabaseDaemonClient
	backupGcsPath      string
	operationId        string
	lro                *lropb.Operation
}

type primaryMetadata struct {
	compatibility string
	dbDomain      string
	dbName        string
	dbUniqueName  string
	enablePDB     string
}

// initPrimaryMetadata queries the external primary for metadata required for creating a standby.
func (task *createStandbyTask) initPrimaryMetadata(ctx context.Context) error {
	metaDataList, err := fetchAndParseSingleColumnMultiRowQueries(ctx, task.primary, task.dbdClient, dbMetaDataListSQL)
	if err != nil {
		return status.Errorf(codes.Unavailable, "initPrimaryData: Error while fetching meta data: %v", err)
	}

	if len(metaDataList) != 5 {
		return status.Errorf(codes.Internal, "initPrimaryData: Error while while fetching meta data: %v", err)
	}
	task.primaryMetadata.compatibility = metaDataList[0]
	task.primaryMetadata.dbDomain = metaDataList[1]
	task.primaryMetadata.dbName = metaDataList[2]
	task.primaryMetadata.dbUniqueName = metaDataList[3]
	task.primaryMetadata.enablePDB = metaDataList[4]

	return nil
}

// initFileDirMapping queries the primary for data file and log file directories, and create map of remote dirs to local dirs.
// The mapping will be fed to "db_file_name_convert" and "log_file_name_convert" parameters.
func (task *createStandbyTask) initFileDirMapping(ctx context.Context) error {
	dataFileDirs, err := fetchAndParseSingleColumnMultiRowQueries(ctx, task.primary, task.dbdClient, dataFilePathListSQL)
	if err != nil {
		return status.Errorf(codes.Unavailable, "initFileDirMapping: Error while executing dataFilePathListSQL: %v", err)
	}
	logFileDirs, err := fetchAndParseSingleColumnMultiRowQueries(ctx, task.primary, task.dbdClient, logFilePathListSQL)
	if err != nil {
		return status.Errorf(codes.Unavailable, "initFileDirMapping: Error while executing logFilePathListSQL: %v", err)
	}

	task.dataFileDirMapping = make(map[string]string)
	task.logFileDirMapping = make(map[string]string)

	fileDir := fmt.Sprintf(consts.DataDir, consts.DataMount, task.primaryMetadata.dbName)

	// If the remote data file dir is /w/x/y/z/GCLOUD, we create the following mapping
	// /w/x/y/z/GCLOUD -> /u02/app/oracle/oradata/<DB_NAME>/data1
	for i, remoteDirPath := range dataFileDirs {
		task.dataFileDirMapping[remoteDirPath] = filepath.Join(fileDir, dataFileDirPrefix+strconv.Itoa(i+1))
	}

	// If the remote log file dir is /w/x/y/z/GCLOUD, we create the following mapping
	// /w/x/y/z/GCLOUD -> /u02/app/oracle/oradata/<DB_NAME>/log1
	for i, remoteDirPath := range logFileDirs {
		task.logFileDirMapping[remoteDirPath] = filepath.Join(fileDir, logFileDirPrefix+strconv.Itoa(i+1))
	}

	return nil
}

// createDataAndLogDirs creates required data and logs dirs on standby.
func (task *createStandbyTask) createDataAndLogDirs(ctx context.Context) error {
	var dirs []string
	for _, targetDir := range task.dataFileDirMapping {
		dirs = append(dirs, targetDir)
	}
	for _, targetDir := range task.logFileDirMapping {
		dirs = append(dirs, targetDir)
	}

	recoveryAreaDir := fmt.Sprintf(consts.RecoveryAreaDir, consts.LogMount, task.primaryMetadata.dbName)
	dirs = append(dirs, recoveryAreaDir)

	var dirsInfo []*dbdpb.CreateDirsRequest_DirInfo
	for _, d := range dirs {
		dirsInfo = append(dirsInfo, &dbdpb.CreateDirsRequest_DirInfo{
			Path: d,
			Perm: 0750,
		})
	}

	if _, err := task.dbdClient.CreateDirs(ctx, &dbdpb.CreateDirsRequest{Dirs: dirsInfo}); err != nil {
		return status.Errorf(codes.Internal, "createDataAndLogDirs: Error while creating dirs: %v", err)
	}

	return nil
}

// createPasswordFile creates a password file from primary password.
func (task *createStandbyTask) createPasswordFile(ctx context.Context) error {
	passwd, err := task.primary.PasswordAccessor.Get(ctx)
	if err != nil {
		return err
	}
	request := &dbdpb.CreatePasswordFileRequest{
		DatabaseName: task.primaryMetadata.dbName,
		SysPassword:  passwd,
		Dir:          filepath.Join(configBaseDir, task.primaryMetadata.dbName),
	}
	if _, err := task.dbdClient.CreatePasswordFile(ctx, request); err != nil {
		return err
	}
	return nil
}

// getDBHostFreeMem read /proc/meminfo on DB node to get free memory size.
// This will be used to calculate pga_aggregate_target and sga_target in init.ora.
func (task *createStandbyTask) getDBHostFreeMem(ctx context.Context) (int, error) {
	resp, err := task.dbdClient.ReadDir(ctx, &dbdpb.ReadDirRequest{
		Path:            "/proc/meminfo",
		Recursive:       false,
		ReadFileContent: true,
	})
	if err != nil {
		return -1, status.Errorf(codes.Internal, "getDBHostFreeMem: Error while reading /proc/meminfo: %v", err)
	}
	reader := strings.NewReader(resp.GetCurrPath().GetContent())
	scanner := bufio.NewScanner(reader)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		// An example MemAvailable info line looks as follows
		// MemAvailable:         1094508 kB
		if ndx := strings.Index(scanner.Text(), "MemAvailable:"); ndx >= 0 {
			s := strings.Split(scanner.Text(), ":")
			if len(s) != 2 {
				return -1, status.Errorf(codes.Internal, "getDBHostFreeMem: Error while parsing available memory info")
			}
			line := strings.TrimSpace(s[1])
			// Discard the last 3 characters in the line
			if mem, err := strconv.Atoi(line[:len(line)-3]); err == nil {
				klog.InfoS("Available memory size is ", "MemAvailable in KB", mem)
				return mem, nil
			}
			break
		}
	}
	return -1, status.Errorf(codes.Internal, "getDBHostFreeMem: Didn't find MemAvailable in /proc/meminfo")
}

// createStandbyInitOraFile creates an init ora file for standby database.
func (task *createStandbyTask) createStandbyInitOraFile(ctx context.Context) error {
	dataFileDirListAsString := task.dirMappingsToSPFileFormat(task.dataFileDirMapping)
	logFileDirListAsString := task.dirMappingsToSPFileFormat(task.logFileDirMapping)

	freeMem, err := task.getDBHostFreeMem(ctx)
	if err != nil {
		return status.Errorf(codes.Internal, "createStandbyInitOraFile: Error while getting free memory size %v", err)
	}

	i := standbyInitOraInput{
		PrimaryHost:          task.primary.Host,
		PrimaryPort:          task.primary.Port,
		PrimaryDbName:        task.primaryMetadata.dbName,
		PrimaryDbUniqueName:  task.primaryMetadata.dbUniqueName,
		PrimaryCompatibility: task.primaryMetadata.compatibility,
		PrimaryEnablePDB:     task.primaryMetadata.enablePDB,
		StandbyDbDomain:      task.standby.DBDomain,
		StandbyDBUniqueName:  task.standby.DBUniqueName,
		StandbyLogDiskSize:   task.standby.LogDiskSize,
		StandbySgaSizeKB:     freeMem / 2,
		StandbyPgaSizeKB:     freeMem / 8,
		LogFileDirList:       logFileDirListAsString,
		DataFileDirList:      dataFileDirListAsString,
	}

	initOraFileContent, err := i.loadInitOraTemplate()
	if err != nil {
		return status.Errorf(codes.Internal, "createStandbyInitOraFile: Error while loading init ora file content: %v", err)
	}

	initFileName := fmt.Sprintf("init%s.ora", task.primaryMetadata.dbName)
	if _, err := task.dbdClient.CreateFile(ctx, &dbdpb.CreateFileRequest{
		Path:    filepath.Join(configBaseDir, task.primaryMetadata.dbName, initFileName),
		Content: initOraFileContent,
	}); err != nil {
		return status.Errorf(codes.Internal, "createStandbyInitOraFile: Error while creating init ora file content: %v", err)
	}
	return nil
}

// dirMappingsToSPFileFormat is a utility function to convert directory mappings.
// to a format required for the init.ora file.
func (task *createStandbyTask) dirMappingsToSPFileFormat(dirMapping map[string]string) string {
	var dataFileDirs []string
	for k, v := range dirMapping {
		dataFileDirs = append(dataFileDirs, fmt.Sprintf("'%s'", k))
		dataFileDirs = append(dataFileDirs, fmt.Sprintf("'%s'", v))
	}
	dataFileDirListAsString := strings.Join(dataFileDirs, ",")
	return dataFileDirListAsString
}

// startupNomount bounce database in NOMOUNT mode.
func (task *createStandbyTask) startupNomount(ctx context.Context) error {
	if _, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		DatabaseName:      task.primaryMetadata.dbName,
		Operation:         dbdpb.BounceDatabaseRequest_STARTUP,
		Option:            "nomount",
		AvoidConfigBackup: true,
	}); err != nil {
		return status.Errorf(codes.Internal, "startupNomount: Error while startup database in nomount: %v", err)
	}
	return nil
}

// createSPFile creates SPFile from init ora file for standby database.
func (task *createStandbyTask) createSPFile(ctx context.Context) error {
	spfileDir := filepath.Join(configBaseDir, task.primaryMetadata.dbName)
	spfileName := fmt.Sprintf("spfile%s.ora", task.primaryMetadata.dbName)
	sql := fmt.Sprintf(createSPFileSQL, filepath.Join(spfileDir, spfileName))

	if _, err := task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands:    []string{sql},
		Suppress:    false,
		ConnectInfo: &dbdpb.RunSQLPlusCMDRequest_DatabaseName{DatabaseName: task.primaryMetadata.dbName},
	}); err != nil {
		return err
	}
	return nil
}

// bounceDatabase shutdown and startup nomount database, spfile will be used after bouncing.
func (task *createStandbyTask) bounceDatabase(ctx context.Context) error {
	if _, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		DatabaseName:      task.primaryMetadata.dbName,
		Operation:         dbdpb.BounceDatabaseRequest_SHUTDOWN,
		Option:            "immediate",
		AvoidConfigBackup: true,
	}); err != nil {
		return status.Errorf(codes.Internal, "bounceDatabase: Error while shutting down database: %v", err)
	}
	if _, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		DatabaseName:      task.primaryMetadata.dbName,
		Operation:         dbdpb.BounceDatabaseRequest_STARTUP,
		Option:            "nomount",
		AvoidConfigBackup: true,
	}); err != nil {
		return status.Errorf(codes.Internal, "bounceDatabase: Error while startup database in nomount: %v", err)
	}
	return nil
}

func (task *createStandbyTask) setupListener(ctx context.Context) error {
	if _, err := task.dbdClient.CreateListener(ctx, &dbdpb.CreateListenerRequest{
		DatabaseName:   task.primaryMetadata.dbName,
		Port:           consts.SecureListenerPort,
		Protocol:       "TCP",
		ExcludePdb:     true,
		CdbServiceName: task.primary.Service,
	}); err != nil {
		return status.Errorf(codes.Internal, "setupListener: Error while while creating the listener: %v", err)
	}
	return nil
}

// downloadBackup downloads RMAN backup file if backupGcsPath is provided.
func (task *createStandbyTask) downloadBackup(ctx context.Context) error {
	if task.backupGcsPath == "" {
		return nil
	}

	downloadReq := &dbdpb.DownloadDirectoryFromGCSRequest{
		GcsPath:   task.backupGcsPath,
		LocalPath: consts.RMANStagingDir,
	}

	if _, err := task.dbdClient.DownloadDirectoryFromGCS(ctx, downloadReq); err != nil {
		return status.Errorf(codes.Internal, "downloadBackup: Error while downloading backup from GCS: %v", err)
	}
	return nil
}

// clonePrimary executes the required RMAN command for cloning the primary.
func (task *createStandbyTask) clonePrimary(ctx context.Context) error {
	passwd, err := task.primary.PasswordAccessor.Get(ctx)
	if err != nil {
		return err
	}
	primaryConn := connect.EZ(task.primary.User, passwd, task.primary.Host, strconv.Itoa(task.primary.Port), task.primary.Service, false)
	standbyConn := connect.EZ(task.primary.User, passwd, "127.0.0.1", strconv.Itoa(consts.SecureListenerPort), task.primary.Service, false)

	rmanScript := `
		run {
		duplicate target database
		for standby
		from active database
		dorecover
		using compressed backupset
		section size 500M
		nofilenamecheck;
		}`

	rmanReq := &dbdpb.RunRMANRequest{
		Scripts:   []string{fmt.Sprintf(rmanScript)},
		Target:    primaryConn,
		Auxiliary: standbyConn,
		TnsAdmin:  filepath.Join(fmt.Sprintf(consts.ListenerDir, consts.DataMount), consts.SECURE),
		Suppress:  true,
	}

	if task.backupGcsPath != "" {
		rmanScript = fmt.Sprintf(`
		run {
		duplicate target database
		for standby
		backup location '%s'
		dorecover
		nofilenamecheck;
		}`, consts.RMANStagingDir)
		klog.InfoS("clonePrimary", "rmanScript", rmanScript)

		rmanReq = &dbdpb.RunRMANRequest{Scripts: []string{fmt.Sprintf(rmanScript)}, Auxiliary: standbyConn, WithoutTarget: true}
	}

	rmanAsyncReq := &dbdpb.RunRMANAsyncRequest{
		SyncRequest: rmanReq,
		LroInput:    &dbdpb.LROInput{OperationId: task.operationId},
	}

	lro, err := task.dbdClient.RunRMANAsync(ctx, rmanAsyncReq)
	if err != nil {
		return fmt.Errorf("error restoring database: %v", err)
	}
	task.lro = lro

	return nil
}

// newCreateStandbyTask creates a standby by cloning the primary database.
func newCreateStandbyTask(ctx context.Context, primary *Primary, standby *Standby, backupGcsPath, operationId string, dbdClient dbdpb.DatabaseDaemonClient) *createStandbyTask {
	t := &createStandbyTask{
		tasks:           task.NewTasks(ctx, "createStandby"),
		dbdClient:       dbdClient,
		primary:         primary,
		standby:         standby,
		primaryMetadata: &primaryMetadata{},
		backupGcsPath:   backupGcsPath,
		operationId:     operationId,
	}

	t.tasks.AddTask("initPrimaryMetadata", t.initPrimaryMetadata)
	t.tasks.AddTask("initFileDirMapping", t.initFileDirMapping)
	t.tasks.AddTask("createDataAndLogDirs", t.createDataAndLogDirs)
	t.tasks.AddTask("createPasswordFile", t.createPasswordFile)
	t.tasks.AddTask("createStandbyInitOraFile", t.createStandbyInitOraFile)
	t.tasks.AddTask("startupNomount", t.startupNomount)
	t.tasks.AddTask("createSPFile", t.createSPFile)
	t.tasks.AddTask("bounceDatabase", t.bounceDatabase)
	t.tasks.AddTask("setupListener", t.setupListener)
	t.tasks.AddTask("downloadBackup", t.downloadBackup)
	t.tasks.AddTask("clonePrimary", t.clonePrimary)

	return t
}
