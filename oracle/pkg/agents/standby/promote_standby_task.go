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
	"strconv"
	"strings"

	connect "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/util/task"
	"k8s.io/klog/v2"
)

type promoteStandbyTask struct {
	tasks     *task.Tasks
	primary   *Primary
	standby   *Standby
	dbdClient dbdpb.DatabaseDaemonClient
	standbyDg *dgConfig
	primaryDg *dgConfig
}

func (task *promoteStandbyTask) removeDataGuardConfig(ctx context.Context) error {
	// Always try standby config first, so that we do not need password;
	// No need to remove Data Guard configuration, if they don't exist.
	if !task.standbyDg.exists(ctx) && !task.primaryDg.exists(ctx) {
		return nil
	}
	// If it only contains the configurations managed by us, remove configuration.
	// Otherwise, remove standby database configuration only.
	members, err := task.primaryDg.members(ctx)
	if err != nil {
		return fmt.Errorf("removeDataGuardConfig: Error while reading DG members: %v", err)
	}

	if (members.configuration == defaultDGConfigName) &&
		(members.size() == 1) && members.standbyContains(task.standby.DBUniqueName) {
		klog.InfoS("removing Data Guard configuration on primary")
		if err := task.primaryDg.remove(ctx); err != nil {
			return fmt.Errorf("removeDataGuardConfig: Error while removing primary Data Guard configuration: %v", err)
		}
	} else if members.standbyContains(task.standby.DBUniqueName) {
		klog.InfoS("removing Data Guard standby database")
		if err := task.primaryDg.removeStandbyDB(ctx, strings.ToLower(task.standby.DBUniqueName)); err != nil {
			return fmt.Errorf("removeDataGuardConfig: Error while removing standby Data Guard configuration: %v", err)
		}
	}
	return nil
}

func (task *promoteStandbyTask) promoteStandby(ctx context.Context) error {
	// Prepare promotion by cancelling managed recovery processes for standby.
	klog.InfoS("checking current managed recovery processes")
	res, err := fetchAndParseQueries(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands:    []string{consts.ListMRPSql},
		ConnectInfo: &dbdpb.RunSQLPlusCMDRequest_Local{},
	}, task.dbdClient)
	if err != nil {
		// Log the non-critical error and continue.
		klog.ErrorS(err, "error while querying managed recovery processes")
	}
	if len(res) > 0 {
		klog.InfoS("found managed recovery processes", "res", res)
		klog.InfoS("cancelling managed recovery processes for standby")
		if _, err = task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
			Commands: []string{
				consts.CancelMRPSql,
			},
		}); err != nil {
			// Log the non-critical error and continue.
			klog.ErrorS(err, "error while cancelling managed recovery processes")
		}
	}

	// Promote standby database to primary.
	klog.InfoS("checking standby database role")
	res, err = fetchAndParseQueries(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands:    []string{consts.ListPrimaryRoleSql},
		ConnectInfo: &dbdpb.RunSQLPlusCMDRequest_Local{},
	}, task.dbdClient)
	if err != nil {
		// Log the non-critical error and continue.
		klog.ErrorS(err, "error while checking standby database role")
	}
	if len(res) < 1 {
		klog.InfoS("promoting standby database to primary")
		if _, err = task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
			Commands: []string{
				consts.ActivateStandbySql,
			},
		}); err != nil {
			return fmt.Errorf("promoteStandby: Error while promoting standby database: %v", err)
		}
	}

	// Open new primary database.
	klog.InfoS("checking if new primary database is open")
	res, err = fetchAndParseQueries(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands:    []string{consts.ListOpenDatabaseSql},
		ConnectInfo: &dbdpb.RunSQLPlusCMDRequest_Local{},
	}, task.dbdClient)
	if err != nil {
		// Log the non-critical error and continue.
		klog.ErrorS(err, "error while checking new primary database status")
	}
	if len(res) < 1 {
		klog.InfoS("opening new primary database")
		if _, err = task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
			Commands: []string{
				consts.OpenDatabaseSql,
			},
		}); err != nil {
			return fmt.Errorf("promoteStandby: Error while opening new primary database: %v", err)
		}
	}
	return nil
}

// newPromoteStandbyTask sets up Data Guard between primary and standby.
func newPromoteStandbyTask(ctx context.Context, primary *Primary, standby *Standby, dbdClient dbdpb.DatabaseDaemonClient) *promoteStandbyTask {
	t := &promoteStandbyTask{
		tasks:     task.NewTasks(ctx, "promoteStandbyTask"),
		dbdClient: dbdClient,
		primary:   primary,
		standby:   standby,
		standbyDg: newDgConfig(dbdClient, func(context.Context) (string, error) {
			return "/", nil
		}),
		primaryDg: newDgConfig(dbdClient, func(ctx1 context.Context) (string, error) {
			passwd, err := primary.PasswordAccessor.Get(ctx1)
			if err != nil {
				return "", err
			}
			return connect.EZ(primary.User, passwd, primary.Host, strconv.Itoa(primary.Port), primary.Service, false), nil
		}),
	}

	t.tasks.AddTask("removeDataGuardConfig", t.removeDataGuardConfig)
	t.tasks.AddTask("promoteStandby", t.promoteStandby)

	return t
}
