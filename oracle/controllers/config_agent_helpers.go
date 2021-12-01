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

package controllers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common/sql"
	lropb "google.golang.org/genproto/googleapis/longrunning"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

// GetLROOperation returns LRO operation for the specified namespace instance and operation id.
func GetLROOperation(ctx context.Context, dbClientFactory DatabaseClientFactory, r client.Reader, id, namespace, instName string) (*lropb.Operation, error) {
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, err
	}
	defer closeConn()

	req := &lropb.GetOperationRequest{Name: id}
	return dbClient.GetOperation(ctx, req)
}

// DeleteLROOperation deletes LRO operation for the specified namespace instance and operation id.
func DeleteLROOperation(ctx context.Context, dbClientFactory DatabaseClientFactory, r client.Reader, id, namespace, instName string) error {
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return err
	}
	defer closeConn()

	_, err = dbClient.DeleteOperation(ctx, &lropb.DeleteOperationRequest{Name: id})
	return err
}

// Check for LRO job status
// Return (true, nil) if LRO is done without errors.
// Return (true, err) if LRO is done with an error.
// Return (false, nil) if LRO still in progress.
// Return (false, err) if other error occurred.
func IsLROOperationDone(ctx context.Context, dbClientFactory DatabaseClientFactory, r client.Reader, id, namespace, instName string) (bool, error) {
	operation, err := GetLROOperation(ctx, dbClientFactory, r, id, namespace, instName)
	if err != nil {
		return false, err
	}
	if !operation.GetDone() {
		return false, nil
	}

	// handle case when remote LRO completed unsuccessfully
	if operation.GetError() != nil {
		return true, fmt.Errorf("Operation failed with err: %s. %v", operation.GetError().GetMessage(), err)
	}

	return true, nil
}

type BounceDatabaseRequest struct {
	Sid string
	// avoid_config_backup: by default we backup the config except for scenarios
	// when it isn't possible (like bootstrapping)
	AvoidConfigBackup bool
}

// BounceDatabase shutdown/startup the database as requested.
func BounceDatabase(ctx context.Context, r client.Reader, dbClientFactory DatabaseClientFactory, namespace, instName string, req BounceDatabaseRequest) error {
	klog.InfoS("BounceDatabase", "namespace", namespace, "instName", instName, "sid", req.Sid)
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return err
	}
	defer closeConn()

	klog.InfoS("BounceDatabase", "client", dbClient)
	_, err = dbClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
		DatabaseName: req.Sid,
		Option:       "immediate",
	})
	if err != nil {
		return fmt.Errorf("BounceDatabase: error while shutting db: %v", err)
	}
	klog.InfoS("BounceDatabase: shutdown successful")

	_, err = dbClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		Operation:         dbdpb.BounceDatabaseRequest_STARTUP,
		DatabaseName:      req.Sid,
		AvoidConfigBackup: req.AvoidConfigBackup,
	})
	if err != nil {
		return fmt.Errorf("configagent/BounceDatabase: error while starting db: %v", err)
	}
	klog.InfoS("configagent/BounceDatabase: startup successful")
	return err
}

func RecoverConfigFile(ctx context.Context, dbClientFactory DatabaseClientFactory, r client.Reader, namespace, instName, cdbName string) error {
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return err
	}
	defer closeConn()

	if _, err := dbClient.RecoverConfigFile(ctx, &dbdpb.RecoverConfigFileRequest{CdbName: cdbName}); err != nil {
		klog.InfoS("configagent/RecoverConfigFile: error while recovering config file: err", "err", err)
		return fmt.Errorf("configagent/RecoverConfigFile: failed to recover config file due to: %v", err)
	}
	klog.InfoS("configagent/RecoverConfigFile: config file backup successful")
	return err
}

// SetParameter sets database parameter as requested.
func SetParameter(ctx context.Context, dbClientFactory DatabaseClientFactory, r client.Reader, namespace, instName, key, value string) (bool, error) {
	dbClient, closeConn, err := dbClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return false, err
	}
	defer closeConn()

	// Fetch parameter type
	// The possible values are IMMEDIATE FALSE DEFERRED
	query := fmt.Sprintf("select issys_modifiable from v$parameter where name='%s'", sql.StringParam(key))
	paramType, err := fetchAndParseSingleResultQuery(ctx, dbClient, query)
	if err != nil {
		return false, fmt.Errorf("configagent/SetParameter: error while inferring parameter type: %v", err)
	}
	query = fmt.Sprintf("select type from v$parameter where name='%s'", sql.StringParam(key))
	paramDatatype, err := fetchAndParseSingleResultQuery(ctx, dbClient, query)
	if err != nil {
		return false, fmt.Errorf("configagent/SetParameter: error while inferring parameter data type: %v", err)
	}
	// string parameters need to be quoted,
	// those have type 2, see the link for the parameter types description
	// https://docs.oracle.com/database/121/REFRN/GUID-C86F3AB0-1191-447F-8EDF-4727D8693754.htm
	isStringParam := paramDatatype == "2"
	command, err := sql.QuerySetSystemParameterNoPanic(key, value, isStringParam)
	if err != nil {
		return false, fmt.Errorf("configagent/SetParameter: error constructing set parameter query: %v", err)
	}

	isStatic := false
	if paramType == "FALSE" {
		klog.InfoS("configagent/SetParameter", "parameter_type", "STATIC")
		command = fmt.Sprintf("%s scope=spfile", command)
		isStatic = true
	}

	_, err = dbClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{command},
		Suppress: false,
	})
	if err != nil {
		return false, fmt.Errorf("configagent/SetParameter: error while executing parameter command: %q", command)
	}
	return isStatic, nil
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
