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
	"strconv"
	"strings"

	connect "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/util/task"
	"k8s.io/klog/v2"
)

const (
	defaultDGConfigName = "el-carro-operator-config"
	dgConfigFile1Temp   = "dr1%s.dat"
	dgConfigFile2Temp   = "dr2%s.dat"
)

type setUpDataGuardTask struct {
	tasks               *task.Tasks
	primary             *Primary
	standby             *Standby
	passwordFileGcsPath string
	dbdClient           dbdpb.DatabaseDaemonClient
	standbyDg           *dgConfig
	primaryDg           *dgConfig
}

func (task *setUpDataGuardTask) ensureListener(ctx context.Context) error {
	if _, err := task.dbdClient.TNSPing(ctx, &dbdpb.TNSPingRequest{
		ConnectionString: connect.EZ("", "", "localhost", strconv.Itoa(consts.SecureListenerPort), task.primary.Service, false),
	}); err != nil {
		klog.InfoS("failed to ping listener ", "err", err)
		klog.InfoS("Recreating listener")
		dbName, err := fetchAndParseSingleColumnMultiRowQueries(ctx, task.primary, task.dbdClient, "select value from V$parameter where name='db_name'")
		if err != nil {
			return fmt.Errorf("ensureDatabase: Error while reading DB name from primary: %v", err)
		}
		if _, err := task.dbdClient.CreateListener(ctx, &dbdpb.CreateListenerRequest{
			DatabaseName:   dbName[0],
			Port:           consts.SecureListenerPort,
			Protocol:       "TCP",
			ExcludePdb:     true,
			CdbServiceName: task.primary.Service,
		}); err != nil {
			return fmt.Errorf("ensureListener: Error while while creating the listener: %v", err)
		}
	}
	return nil
}

func (task *setUpDataGuardTask) ensureDatabase(ctx context.Context) error {
	mode, err := fetchAndParseSingleColumnMultiRowQueriesLocal(ctx, task.dbdClient, "select open_mode from v$database")
	if err == nil {
		klog.InfoS("the standby database status", "open mode", mode)
		return nil
	}
	klog.InfoS("trying to startup mount the standby database")
	dbName, err := fetchAndParseSingleColumnMultiRowQueries(ctx, task.primary, task.dbdClient, "select value from V$parameter where name='db_name'")
	if err != nil {
		return fmt.Errorf("ensureDatabase: Error while reading DB name from primary: %v", err)
	}
	if _, err := task.dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		DatabaseName: dbName[0],
		Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
		Option:       "nomount",
	}); err != nil {
		return fmt.Errorf("ensureDatabase: Error while starting the standby database: %v", err)
	}

	if _, err = task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{
			fmt.Sprintf("alter database mount standby database"),
		},
	}); err != nil {
		return fmt.Errorf("ensureDatabase: Error while mount the standby database: %v", err)
	}

	return nil
}

func (task *setUpDataGuardTask) ensureStandbyLog(ctx context.Context) error {
	res, err := fetchAndParseQueries(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands:    []string{"select group# from v$standby_log"},
		Suppress:    true,
		ConnectInfo: &dbdpb.RunSQLPlusCMDRequest_Local{},
	}, task.dbdClient)
	if err != nil {
		klog.ErrorS(err, "error while reading standby log information")
		// not critical, continue
		return nil
	}
	if len(res) > 0 {
		klog.InfoS("found standby logs", "res", res)
		return nil
	}
	klog.InfoS("adding standby logs for standby")

	passwd, err := task.primary.PasswordAccessor.Get(ctx)
	if err != nil {
		klog.ErrorS(err, "error while accessing secret")
		// not critical, continue
		return nil
	}
	res, err = fetchAndParseQueries(
		ctx,
		&dbdpb.RunSQLPlusCMDRequest{
			Commands: []string{"select bytes/1024/1024 from v$log"},
			Suppress: true,
			ConnectInfo: &dbdpb.RunSQLPlusCMDRequest_Dsn{
				Dsn: connect.EZ(task.primary.User, passwd, task.primary.Host, strconv.Itoa(task.primary.Port), task.primary.Service, true),
			},
		},
		task.dbdClient)
	if err != nil {
		klog.ErrorS(err, "error while reading primary log information")
		// not critical, continue
		return nil
	}
	if len(res) == 0 {
		klog.Warningf("got primary log bytes %v, skip adding standby log", err)
		return nil
	}
	for _, byteKV := range res {
		byteVal, ok := byteKV["BYTES/1024/1024"]
		if !ok {
			klog.Errorf("error while parsing primary log information: %v", byteKV)
			// not critical, continue
			return nil
		}
		if _, err = task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
			Commands: []string{
				fmt.Sprintf("alter database add standby logfile thread 1 size %sM", byteVal),
			},
		}); err != nil {
			klog.ErrorS(err, "error while adding standby log")
			return nil
		}
	}
	if _, err = task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{
			fmt.Sprintf("alter database add standby logfile thread 1 size %sM", res[0]["BYTES/1024/1024"]),
		},
	}); err != nil {
		klog.ErrorS(err, "error while adding standby log")
		return nil
	}
	return nil
}

func (task *setUpDataGuardTask) ensureConfigLocation(ctx context.Context) error {
	configLocations, err := fetchAndParseSingleColumnMultiRowQueriesLocal(ctx, task.dbdClient, "select value from V$parameter where name in ('dg_broker_config_file1', 'dg_broker_config_file2')")
	if err != nil {
		return fmt.Errorf("ensureConfigLocation: Error while getting dg broker config files location on standby: %v", err)
	}
	wantConfigFile1 := filepath.Join(fmt.Sprintf(consts.ConfigBaseDir, consts.DataMount), fmt.Sprintf(dgConfigFile1Temp, task.standby.DBUniqueName))
	wantConfigFile2 := filepath.Join(fmt.Sprintf(consts.ConfigBaseDir, consts.DataMount), fmt.Sprintf(dgConfigFile2Temp, task.standby.DBUniqueName))

	if len(configLocations) == 2 && configLocations[0] == wantConfigFile1 && configLocations[1] == wantConfigFile2 {
		return nil
	}
	klog.InfoS("Updating dg broker config file location", "dg config file1", wantConfigFile1, "dg config file2", wantConfigFile2)
	dgEnable, err := fetchAndParseSingleColumnMultiRowQueriesLocal(ctx, task.dbdClient, "select value from V$parameter where name='dg_broker_start'")
	if err != nil {
		return fmt.Errorf("ensureDataGuardBrokerEnable: Error while checking dg broker enable on standby: %v", err)
	}
	if strings.EqualFold(dgEnable[0], "true") {
		klog.InfoS("Data Guard is running, skip updating config file locations")
		return nil
	}

	if _, err = task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{
			fmt.Sprintf("alter system set dg_broker_config_file1='%s' scope=both", wantConfigFile1),
			fmt.Sprintf("alter system set dg_broker_config_file2='%s' scope=both", wantConfigFile2),
		},
	}); err != nil {
		return fmt.Errorf("ensureConfigLocation: Error while setting dg broker config files location on standby: %v", err)
	}
	return nil
}

func (task *setUpDataGuardTask) ensureDataGuardBrokerEnable(ctx context.Context) error {
	dgEnable, err := fetchAndParseSingleColumnMultiRowQueriesLocal(ctx, task.dbdClient, "select value from V$parameter where name='dg_broker_start'")
	if err != nil {
		return fmt.Errorf("ensureDataGuardBrokerEnable: Error while checking dg broker enable on standby: %v", err)
	}
	if strings.EqualFold(dgEnable[0], "true") {
		return nil
	}
	klog.InfoS("Enabling dg broker on standby")
	if _, err = task.dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{"alter system set dg_broker_start=true scope=both"},
	}); err != nil {
		return fmt.Errorf("ensureDataGuardBrokerEnable: Error while enabling dg broker on standby: %v", err)
	}
	return nil
}

func (task *setUpDataGuardTask) ensureDGConfigExists(ctx context.Context) error {
	// always try standby config first, so that we do not need password
	if task.standbyDg.exists(ctx) || task.primaryDg.exists(ctx) {
		return nil
	}
	klog.InfoS("downloading password file to standby")
	if err := task.downloadPasswordFile(ctx); err != nil {
		return fmt.Errorf("ensureDGConfigExists: Error while downloading password file: %v", err)
	}
	klog.InfoS("creating Data Guard configuration on primary")
	// create new configuration on primary
	// query primary db unique
	vals, err := fetchAndParseSingleColumnMultiRowQueries(ctx, task.primary, task.dbdClient, "select value from V$parameter where name='db_unique_name'")
	if err != nil {
		return fmt.Errorf("ensureDGConfigExists: Error while fetching primary db unique name: %v", err)
	}
	primaryUniqueName := vals[0]

	passwd, err := task.primary.PasswordAccessor.Get(ctx)
	if err != nil {
		return fmt.Errorf("ensureDGConfigExists: Error while accessing secret: %v", err)
	}
	resp, err := task.dbdClient.RunDataGuard(ctx, &dbdpb.RunDataGuardRequest{
		Scripts: []string{
			fmt.Sprintf("create configuration %s as primary database is %s connect identifier is '%s'",
				defaultDGConfigName,
				primaryUniqueName,
				connect.EZ("", "", task.primary.Host, strconv.Itoa(task.primary.Port), task.primary.Service, false),
			),
			fmt.Sprintf("add database %s as connect identifier is '%s' maintained as physical",
				task.standby.DBUniqueName,
				// in data migration, standby listener will reuse primary service name
				connect.EZ("", "", task.standby.Host, strconv.Itoa(task.standby.Port), task.primary.Service, false),
			),
			// the task will not reconcile DG enable state, users can disable DG.
			// the task only enable the configuration created by itself.
			"enable configuration",
		},
		Target: connect.EZ(task.primary.User, passwd, task.primary.Host, strconv.Itoa(task.primary.Port), task.primary.Service, false),
	})
	klog.InfoS("create DG config", "resp", resp)
	if err != nil {
		return fmt.Errorf("ensureDGConfigExists: Error while creating DG configuration: %v", err)
	}
	return nil
}

func (task *setUpDataGuardTask) ensureDGConfigStandbyMatch(ctx context.Context) error {
	if !task.standbyDg.exists(ctx) {
		// handle the scenario that primary created, but add standby failed in the previous run.
		klog.InfoS("downloading password file to standby")
		if err := task.downloadPasswordFile(ctx); err != nil {
			return fmt.Errorf("ensureDGConfigStandbyMatch: Error while downloading password file: %v", err)
		}
		passwd, err := task.primary.PasswordAccessor.Get(ctx)
		if err != nil {
			return fmt.Errorf("ensureDGConfigStandbyMatch: Error while accessing secret: %v", err)
		}
		resp, err := task.dbdClient.RunDataGuard(ctx, &dbdpb.RunDataGuardRequest{
			Scripts: []string{
				fmt.Sprintf("add database %s as connect identifier is '%s' maintained as physical",
					task.standby.DBUniqueName,
					// in data migration, standby listener will reuse primary service name
					connect.EZ("", "", task.standby.Host, strconv.Itoa(task.standby.Port), task.primary.Service, false),
				),
				// the task will not reconcile DG enable state, users can disable DG.
				// the task only enable the configuration created by itself.
				"enable configuration",
			},
			Target: connect.EZ(task.primary.User, passwd, task.primary.Host, strconv.Itoa(task.primary.Port), task.primary.Service, false),
		})
		klog.InfoS("Add standby config", "resp", resp)
		if err != nil {
			return fmt.Errorf("ensureDGConfigStandbyMatch: Error while adding standby to DG configuration: %v", err)
		}
		return nil
	}

	// if configuration created, let Data Guard manage primary connect identifier,
	// ensure standby connect identifier matched with the latest POD status.
	want := connect.EZ("", "", task.standby.Host, strconv.Itoa(task.standby.Port), task.primary.Service, false)
	got, err := task.standbyDg.connectIdentifier(ctx, task.standby.DBUniqueName)
	if err != nil {
		klog.Warning("failed tp read standby connect identifier from local dg config", err)
		// try primary config
		got, err = task.primaryDg.connectIdentifier(ctx, task.standby.DBUniqueName)
		if err != nil {
			return fmt.Errorf("ensureDGConfigStandbyMatch: Error while reading standby connect identifier: %v", err)
		}
	}
	if got != want {
		klog.InfoS("updating Data Guard standby configuration on primary.")
		if err := task.primaryDg.setConnectIdentifier(ctx, task.standby.DBUniqueName, want); err != nil {
			return fmt.Errorf("ensureDGConfigStandbyMatch: Error while setting standby connect identifier: %v", err)
		}
	}
	return nil
}

func (task *setUpDataGuardTask) ensureDGConfigPrimaryMatch(ctx context.Context) error {
	members, err := task.standbyDg.members(ctx)
	if err != nil {
		return fmt.Errorf("ensureDGConfigPrimaryMatch: Error while reading DG members: %v", err)
	}
	got, err := task.standbyDg.connectIdentifier(ctx, members.primary)
	if err != nil {
		return fmt.Errorf("ensureDGConfigPrimaryMatch: Error while reading primary connect identifier:: %v", err)
	}
	want := connect.EZ("", "", task.primary.Host, strconv.Itoa(task.primary.Port), task.primary.Service, false)
	if got != want {
		klog.InfoS("updating Data Guard primary configuration on primary.")
		// query DB to ensure the code get the correct unique name
		vals, err := fetchAndParseSingleColumnMultiRowQueries(ctx, task.primary, task.dbdClient, "select value from V$parameter where name='db_unique_name'")
		if err != nil {
			return fmt.Errorf("ensureDGConfigPrimaryMatch: Error while fetching primary db unique name: %v", err)
		}
		primaryUniqueName := vals[0]
		if err := task.primaryDg.setConnectIdentifier(ctx, primaryUniqueName, want); err != nil {
			return fmt.Errorf("ensureDGConfigPrimaryMatch: Error while setting primary connect identifier: %v", err)
		}
	}
	return nil
}

func (task *setUpDataGuardTask) downloadPasswordFile(ctx context.Context) error {
	dbName, err := fetchAndParseSingleColumnMultiRowQueries(ctx, task.primary, task.dbdClient, "select value from V$parameter where name='db_name'")
	if err != nil {
		return fmt.Errorf("downloadPasswordFile: Error while reading DB name from primary: %v", err)
	}
	if _, err := task.dbdClient.DownloadDirectoryFromGCS(ctx, &dbdpb.DownloadDirectoryFromGCSRequest{
		GcsPath:   task.passwordFileGcsPath,
		LocalPath: filepath.Join(configBaseDir, dbName[0], fmt.Sprintf("orapw%s", dbName[0])),
	}); err != nil {
		return fmt.Errorf("downloadPasswordFile: Error while downloading file from GCS: %v", err)
	}
	return nil
}

// newSetUpStandbyTask sets up Data Guard between primary and standby.
func newSetUpStandbyTask(ctx context.Context, primary *Primary, standby *Standby, passwordFileGcsPath string, dbdClient dbdpb.DatabaseDaemonClient) *setUpDataGuardTask {
	t := &setUpDataGuardTask{
		tasks:               task.NewTasks(ctx, "setUpStandbyTask"),
		primary:             primary,
		standby:             standby,
		passwordFileGcsPath: passwordFileGcsPath,
		dbdClient:           dbdClient,
		standbyDg: newDgConfig(dbdClient, func(context.Context) (string, error) {
			return "/", nil
		}),
		primaryDg: newDgConfig(dbdClient, func(ctx1 context.Context) (string, error) {
			passwd, err := primary.PasswordAccessor.Get(ctx1)
			if err != nil {
				return "", err
			}
			return connect.EZ(primary.User, passwd, primary.Host, strconv.Itoa(primary.Port), primary.Service, false), nil
		})}

	t.tasks.AddTask("ensureListener", t.ensureListener)
	t.tasks.AddTask("ensureDatabase", t.ensureDatabase)
	t.tasks.AddTask("ensureStandbyLog", t.ensureStandbyLog)
	t.tasks.AddTask("ensureConfigLocation", t.ensureConfigLocation)
	t.tasks.AddTask("ensureDataGuardBrokerEnable", t.ensureDataGuardBrokerEnable)
	t.tasks.AddTask("ensureDGConfigExists", t.ensureDGConfigExists)
	t.tasks.AddTask("ensureDGConfigStandbyMatch", t.ensureDGConfigStandbyMatch)
	t.tasks.AddTask("ensureDGConfigPrimaryMatch", t.ensureDGConfigPrimaryMatch)

	return t
}
