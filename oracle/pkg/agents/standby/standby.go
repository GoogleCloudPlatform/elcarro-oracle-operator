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
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/standbyhelpers"
	connect "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/util/task"
	lropb "google.golang.org/genproto/googleapis/longrunning"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	configBaseDir = fmt.Sprintf(consts.ConfigBaseDir, consts.DataMount)
)

// SecretAccessor defines the methods we use from the secret accessor.
type SecretAccessor interface {
	Get(context.Context) (string, error)
}

// Primary is a domain object that describes an Oracle external primary
// instance.
type Primary struct {
	Host             string
	Port             int
	Service          string
	User             string
	PasswordAccessor SecretAccessor
}

// Standby is a domain object that describes an Oracle standby replica
// instance.
type Standby struct {
	CDBName      string
	DBUniqueName string
	DBDomain     string
	Host         string
	Port         int
	LogDiskSize  int64
	Version      string
}

// dgMembers describes members in DG configuration.
type dgMembers struct {
	configuration    string
	primary          string
	physicalStandbys []string
	logicalStandbys  []string
}

// standbyContains returns whether the specified dbUniqueName is a member of
// physical or logical standby of the data guard configuration.
func (m *dgMembers) standbyContains(dbUniqueName string) bool {
	for _, db := range m.physicalStandbys {
		if strings.EqualFold(db, dbUniqueName) {
			return true
		}
	}
	for _, db := range m.logicalStandbys {
		if strings.EqualFold(db, dbUniqueName) {
			return true
		}
	}
	return false
}

// size returns the total number of group members in the data guard configuration.
func (m *dgMembers) size() int {
	return len(m.physicalStandbys) + len(m.logicalStandbys)
}

// CreateStandby creates a standby database by cloning a external database.
func CreateStandby(ctx context.Context, primary *Primary, standby *Standby, backupGcsPath, operationId string, dbdClient dbdpb.DatabaseDaemonClient) (*lropb.Operation, error) {
	operation, err := dbdClient.GetOperation(ctx, &lropb.GetOperationRequest{Name: operationId})
	if s, ok := status.FromError(err); ok && s.Code() == codes.NotFound {
		t := newCreateStandbyTask(ctx, primary, standby, backupGcsPath, operationId, dbdClient)
		err := task.Do(ctx, t.tasks)
		if err != nil {
			return nil, err
		}
		return t.lro, nil
	} else if err != nil {
		return nil, fmt.Errorf("CreateStandby: failed to GetOperation with err %v", err)
	}
	return operation, nil
}

// SetUpDataGuard sets up Data Guard between primary and standby.
func SetUpDataGuard(ctx context.Context, primary *Primary, standby *Standby, passwordFileGcsPath string, dbdClient dbdpb.DatabaseDaemonClient) error {
	t := newSetUpStandbyTask(ctx, primary, standby, passwordFileGcsPath, dbdClient)
	return task.Do(ctx, t.tasks)
}

// DataGuardStatus get configuration and this standby database status.
func DataGuardStatus(ctx context.Context, StandbyUniqueName string, dbdClient dbdpb.DatabaseDaemonClient) ([]string, error) {
	resp, err := dbdClient.RunDataGuard(ctx, &dbdpb.RunDataGuardRequest{
		Target: "/",
		Scripts: []string{
			"show configuration",
			fmt.Sprintf("show database %s", StandbyUniqueName),
		},
	})
	if err != nil {
		return nil, err
	}
	return resp.GetOutput(), err
}

// PromoteStandby promotes standby database to primary.
func PromoteStandby(ctx context.Context, primary *Primary, standby *Standby, dbdClient dbdpb.DatabaseDaemonClient) error {
	t := newPromoteStandbyTask(ctx, primary, standby, dbdClient)
	return task.Do(ctx, t.tasks)
}

// BootstrapStandby converts promoted standby to standard El Carro Oracle instance.
func BootstrapStandby(ctx context.Context, dbdClient dbdpb.DatabaseDaemonClient) error {
	t := newBootstrapStandbyTask(ctx, dbdClient)
	return task.Do(ctx, t.tasks)
}

// VerifyStandbySettings does preflight checks on standby settings.
func VerifyStandbySettings(ctx context.Context, primary *Primary, standby *Standby, passwordGcsPath, backupGcsPath string, dbdClient dbdpb.DatabaseDaemonClient) (settingErrs []*standbyhelpers.StandbySettingErr) {
	t := newVerifyStandbySettingsTask(ctx, primary, standby, passwordGcsPath, backupGcsPath, dbdClient)
	task.Do(ctx, t.tasks)
	return t.settingErrs
}

type dgConfig struct {
	dbdClient                   dbdpb.DatabaseDaemonClient
	buildTarget                 func(ctx context.Context) (string, error)
	configurationNameRe         *regexp.Regexp
	primaryUniqueNameRe         *regexp.Regexp
	physicalStandbyUniqueNameRe *regexp.Regexp
	logicalStandbyUniqueNameRe  *regexp.Regexp
	connRe                      *regexp.Regexp
}

func (d *dgConfig) exists(ctx context.Context) bool {
	target, err := d.buildTarget(ctx)
	if err != nil {
		return false
	}
	_, err = d.dbdClient.RunDataGuard(ctx, &dbdpb.RunDataGuardRequest{
		Target:  target,
		Scripts: []string{"show configuration"},
	})
	return err == nil
}

func (d *dgConfig) remove(ctx context.Context) error {
	target, err := d.buildTarget(ctx)
	if err != nil {
		return fmt.Errorf("failed to build target: %v", err)
	}
	if resp, err := d.dbdClient.RunDataGuard(ctx, &dbdpb.RunDataGuardRequest{
		Target:  target,
		Scripts: []string{"remove configuration"},
	}); err != nil {
		return fmt.Errorf("failed to remove configuration: %v, with response: %v", err, resp)
	}
	return nil
}

func (d *dgConfig) removeStandbyDB(ctx context.Context, dbUniqueName string) error {
	target, err := d.buildTarget(ctx)
	if err != nil {
		return fmt.Errorf("failed to build target: %v", err)
	}
	if resp, err := d.dbdClient.RunDataGuard(ctx, &dbdpb.RunDataGuardRequest{
		Target:  target,
		Scripts: []string{fmt.Sprintf("disable database %s", dbUniqueName)},
	}); err != nil {
		return fmt.Errorf("failed to disable database: %v, with response: %v", err, resp)
	}
	if resp, err := d.dbdClient.RunDataGuard(ctx, &dbdpb.RunDataGuardRequest{
		Target:  target,
		Scripts: []string{fmt.Sprintf("remove database %s", dbUniqueName)},
	}); err != nil {
		return fmt.Errorf("failed to remove database: %v, with response: %v", err, resp)
	}
	return nil
}

func (d *dgConfig) members(ctx context.Context) (*dgMembers, error) {
	target, err := d.buildTarget(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to build target: %v", err)
	}
	resp, err := d.dbdClient.RunDataGuard(ctx, &dbdpb.RunDataGuardRequest{
		Target:  target,
		Scripts: []string{"show configuration"},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get DG configuration: %v", err)
	}
	if len(resp.GetOutput()) != 1 {
		return nil, fmt.Errorf("got unexpected resp: %v, want len(resp.GetOuput()) = 1", resp)

	}
	config := d.configurationNameRe.FindStringSubmatch(resp.GetOutput()[0])
	if config == nil {
		return nil, fmt.Errorf("failed to find configuration name from %v", resp.GetOutput()[0])
	}
	pUnique := d.primaryUniqueNameRe.FindStringSubmatch(resp.GetOutput()[0])
	if pUnique == nil {
		return nil, fmt.Errorf("failed to find primary unique name from %v", resp.GetOutput()[0])
	}
	physicalUniques := d.physicalStandbyUniqueNameRe.FindAllStringSubmatch(resp.GetOutput()[0], -1)
	var pStandbys []string
	for _, physicalUnique := range physicalUniques {
		pStandbys = append(pStandbys, physicalUnique[1])
	}
	logicalUniques := d.logicalStandbyUniqueNameRe.FindAllStringSubmatch(resp.GetOutput()[0], -1)
	var lStandbys []string
	for _, logicalUnique := range logicalUniques {
		lStandbys = append(lStandbys, logicalUnique[1])
	}
	return &dgMembers{
		configuration:    config[1],
		primary:          pUnique[1],
		physicalStandbys: pStandbys,
		logicalStandbys:  lStandbys,
	}, nil
}

func (d *dgConfig) connectIdentifier(ctx context.Context, uniqueName string) (string, error) {
	target, err := d.buildTarget(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to build target: %v", err)
	}
	resp, err := d.dbdClient.RunDataGuard(ctx, &dbdpb.RunDataGuardRequest{
		Target:  target,
		Scripts: []string{fmt.Sprintf("show database %s dgconnectidentifier", uniqueName)},
	})
	if err != nil {
		return "", fmt.Errorf("failed to get connect identifier: %v", err)
	}
	if len(resp.GetOutput()) != 1 {
		return "", fmt.Errorf("got unexpected resp: %v, want len(resp.GetOuput()) = 1", resp)
	}
	matched := d.connRe.FindStringSubmatch(resp.GetOutput()[0])
	if matched == nil {
		return "", fmt.Errorf("failed to find connect identifier from %v", resp.GetOutput()[0])
	}
	return matched[1], nil
}

func (d *dgConfig) setConnectIdentifier(ctx context.Context, uniqueName, newIdentifier string) error {
	target, err := d.buildTarget(ctx)
	if err != nil {
		return fmt.Errorf("failed to build target: %v", err)
	}
	if _, err := d.dbdClient.RunDataGuard(ctx, &dbdpb.RunDataGuardRequest{
		Target:  target,
		Scripts: []string{fmt.Sprintf("edit database '%s' set property 'dgconnectidentifier'='%s'", uniqueName, newIdentifier)},
	}); err != nil {
		return fmt.Errorf("failed to update connect identifier(%s) for DB(%s) : %v", newIdentifier, uniqueName, err)
	}
	return nil
}

func newDgConfig(dbdClient dbdpb.DatabaseDaemonClient, buildTarget func(ctx context.Context) (string, error)) *dgConfig {
	return &dgConfig{
		dbdClient:                   dbdClient,
		buildTarget:                 buildTarget,
		configurationNameRe:         regexp.MustCompile(`Configuration\s*-\s*(\S+)`),
		primaryUniqueNameRe:         regexp.MustCompile(`(\S+)\s*-\s*Primary database`),
		physicalStandbyUniqueNameRe: regexp.MustCompile(`(\S+)\s*-\s*Physical standby database`),
		logicalStandbyUniqueNameRe:  regexp.MustCompile(`(\S+)\s*-\s*Logical standby database`),
		connRe:                      regexp.MustCompile(`DGConnectIdentifier\s*=\s*'(\S+)'`),
	}
}

func fetchAndParseSingleColumnMultiRowQueriesLocal(ctx context.Context, dbdClient dbdpb.DatabaseDaemonClient, query string) ([]string, error) {
	res, err := fetchAndParseQueries(
		ctx,
		&dbdpb.RunSQLPlusCMDRequest{
			Commands:    []string{query},
			ConnectInfo: &dbdpb.RunSQLPlusCMDRequest_Local{},
		},
		dbdClient,
	)
	if err != nil {
		return nil, err
	}
	var rows []string
	for _, row := range res {
		if len(row) != 1 {
			return nil, fmt.Errorf("fetchAndParseSingleColumnMultiRowQueriesLocal: # of cols returned by query != 1: %v", row)
		}
		for _, v := range row {
			rows = append(rows, v)
		}
	}
	return rows, nil
}

// fetchAndParseSingleColumnMultiRowQueriesFromEM is a utility method intended
// for running single column queries on the external server. It parses the
// single column JSON result-set (returned by runSQLPlus API) and returns a list.
func fetchAndParseSingleColumnMultiRowQueries(ctx context.Context, primary *Primary, dbdClient dbdpb.DatabaseDaemonClient, query string) ([]string, error) {
	passwd, err := primary.PasswordAccessor.Get(ctx)
	if err != nil {
		return nil, err
	}
	res, err := fetchAndParseQueries(
		ctx,
		&dbdpb.RunSQLPlusCMDRequest{
			Commands:    []string{query},
			Suppress:    true,
			ConnectInfo: &dbdpb.RunSQLPlusCMDRequest_Dsn{Dsn: connect.EZ(primary.User, passwd, primary.Host, strconv.Itoa(primary.Port), primary.Service, true)},
		},
		dbdClient,
	)
	if err != nil {
		return nil, err
	}
	var rows []string
	for _, row := range res {
		if len(row) != 1 {
			return nil, fmt.Errorf("fetchAndParseSingleColumnMultiRowQueries: # of cols returned by query != 1: %v", row)
		}
		for _, v := range row {
			rows = append(rows, v)
		}
	}
	return rows, nil
}

// fetchAndParseQueries is a utility method intended for running queries
// on the external server. It parses the JSON result-set (returned by runSQLPlus
// API) and returns a list of rows with column-value mapping.
func fetchAndParseQueries(ctx context.Context, sqlRequest *dbdpb.RunSQLPlusCMDRequest, dbdClient dbdpb.DatabaseDaemonClient) ([]map[string]string, error) {
	response, err := dbdClient.RunSQLPlusFormatted(ctx, sqlRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to run query %q: %v", sqlRequest.GetCommands(), err)
	}
	return parseSQLResponse(response)
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
