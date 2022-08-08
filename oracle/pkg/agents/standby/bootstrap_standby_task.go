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
	"context"
	"fmt"
	"path/filepath"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/database/provision"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/util/task"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	standbyMetadataSQL = "select value from V$parameter where name in ('db_domain', 'db_name')"
)

type standbyMetadata struct {
	dbDomain string
	dbName   string
}

type bootstrapStandbyTask struct {
	tasks           *task.Tasks
	standbyMetadata *standbyMetadata
	dbdClient       dbdpb.DatabaseDaemonClient
}

// initStandbyMetadata queries the standby for metadata required for bootstrapping a standby.
func (task *bootstrapStandbyTask) initStandbyMetadata(ctx context.Context) error {
	metaDataList, err := fetchAndParseSingleColumnMultiRowQueriesLocal(ctx, task.dbdClient, standbyMetadataSQL)
	if err != nil {
		return status.Errorf(codes.Unavailable, "initStandbyMetadata: Error while fetching meta data: %v", err)
	}

	if len(metaDataList) != 2 {
		return status.Errorf(codes.Internal, "initStandbyMetadata: Error while while fetching meta data: %v", err)
	}

	task.standbyMetadata.dbDomain = metaDataList[0]
	task.standbyMetadata.dbName = metaDataList[1]
	return nil
}

// setupUsers create internal users.
func (task *bootstrapStandbyTask) setupUsers(ctx context.Context) error {
	t := provision.NewSetupUsersTaskForStandby(task.standbyMetadata.dbName, task.dbdClient)

	if err := t.Call(ctx); err != nil {
		return status.Errorf(codes.Internal, "setupUsers: Error while setup users: %v", err)
	}

	return nil
}

// createListener create standard listener for standby database.
func (task *bootstrapStandbyTask) createListener(ctx context.Context) error {
	_, err := task.dbdClient.CreateListener(ctx, &dbdpb.CreateListenerRequest{
		DatabaseName: task.standbyMetadata.dbName,
		Port:         consts.SecureListenerPort,
		Protocol:     "TCP",
		DbDomain:     task.standbyMetadata.dbDomain,
	})
	if err != nil {
		return status.Errorf(codes.Internal, "createListener: Error while create listener: %v", err)
	}
	return nil
}

// createSPFile creates SPFile and bounces database to use spfile if needed.
func (task *bootstrapStandbyTask) createSPFile(ctx context.Context) error {
	spfileLoc := filepath.Join(configBaseDir, task.standbyMetadata.dbName, fmt.Sprintf("spfile%s.ora", task.standbyMetadata.dbName))
	resp, err := task.dbdClient.FileExists(ctx, &dbdpb.FileExistsRequest{Name: spfileLoc})
	if err != nil {
		return status.Errorf(codes.Internal, "createSPFile: Error while check whether spfile exist: %v", err)
	}
	// skip this step if spfile already exists under /u02 in expected location
	if resp.Exists {
		return nil
	}

	_, err = task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{fmt.Sprintf("create spfile='%s' from memory", spfileLoc)}, Suppress: false})
	if err != nil {
		return status.Errorf(codes.Internal, "createSPFile: Error while create spfile: %v", err)
	}

	_, err = task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
		DatabaseName: task.standbyMetadata.dbName,
		Option:       "immediate",
	})
	if err != nil {
		return status.Errorf(codes.Internal, "createSPFile: Error while shutting down db: %v", err)
	}

	_, err = task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:         dbdpb.BounceDatabaseRequest_STARTUP,
		DatabaseName:      task.standbyMetadata.dbName,
		AvoidConfigBackup: true,
	})
	if err != nil {
		return status.Errorf(codes.Internal, "createSPFile: Error while starting db: %v", err)
	}
	return nil
}

// markProvisionDone create provisioning done file in db pod.
func (task *bootstrapStandbyTask) markProvisionDone(ctx context.Context) error {
	if _, err := task.dbdClient.CreateFile(ctx, &dbdpb.CreateFileRequest{
		Path:    consts.ProvisioningDoneFile,
		Content: "",
	}); err != nil {
		return status.Errorf(codes.Internal, "markProvisionDone: Error while create provisioning_done file: %v", err)
	}
	return nil
}

func (task *bootstrapStandbyTask) openPDBs(ctx context.Context) error {
	_, err := task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: []string{consts.OpenPluggableDatabaseSQL}, Suppress: false})
	if err != nil {
		return status.Errorf(codes.Internal, "openPDBs: Error while open pluggable databases: %v", err)
	}
	return nil
}

// newBootstrapStandbyTask bootstraps database to a standard El Carro oracle database.
// This will be used for both manual setup standby and automatic setup standby.
func newBootstrapStandbyTask(ctx context.Context, dbdClient dbdpb.DatabaseDaemonClient) *bootstrapStandbyTask {
	t := &bootstrapStandbyTask{
		tasks:           task.NewTasks(ctx, "bootstrapStandby"),
		dbdClient:       dbdClient,
		standbyMetadata: &standbyMetadata{},
	}

	t.tasks.AddTask("initStandbyMetadata", t.initStandbyMetadata)
	t.tasks.AddTask("createListener", t.createListener)
	t.tasks.AddTask("createSPFile", t.createSPFile)
	t.tasks.AddTask("setupUsers", t.setupUsers)
	t.tasks.AddTask("markProvisionDone", t.markProvisionDone)
	t.tasks.AddTask("openPDBs", t.openPDBs)
	return t
}
