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
	"fmt"

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
