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

// A client program to interactively test Database Daemon functionality by
// simulating one of the calls issued from the Agent.
// Supported calls are:
// - CheckDatabase[CDB|PDB]
// - [Stop|Start]Database
// - [Stop|Start]Listeners
// - RunSQLPlus[Formatted]
// - DataPump[Import|Export]Async
// - ApplyDataPatchAsync
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	lropb "google.golang.org/genproto/googleapis/longrunning"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	dbdaemonlib "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

const exitErrorCode = consts.DefaultExitErrorCode

var (
	action                  = flag.String("action", "", "Action to check: CheckDatabase[CDB|PDB], [Start|Stop]Database, [Start|Stop]Listeners, RunSQLPlus[Formatted], KnownPDBs, GetDatabaseType, GetDatabaseName")
	databaseName            = flag.String("database_name", "", "PDB database name")
	operationId             = flag.String("operation_id", "", "Operation id")
	commands                = flag.String("commands", "", "A list of SQL statements delimited by a semicolon")
	reqTimeoutDefault       = 10 * time.Minute
	reqTimeout              = flag.Duration("request_timeout", reqTimeoutDefault, "Maximum amount of time allowed to complete a request (default is 10 min)")
	cdbName                 = flag.String("cdb_name", "GCLOUD", "CDB database name")
	exportObjectTypeDefault = "SCHEMAS"
	exportObjectType        = flag.String("export_object_type", exportObjectTypeDefault, "Data pump export object type")
	exportObjects           = flag.String("export_objects", "", "A list of data pump export objects delimited by a comma")
	gcsPath                 = flag.String("gcs_path", "", "GCS URI")
	gcsLogPath              = flag.String("gcs_log_path", "", "GCS URI for log upload")
	flashbackTime           = flag.String("flashback_time", "", "Flashback_time used in data pump export")
	newDatabaseDaemonClient = func(cc *grpc.ClientConn) databaseDaemonStub { return dbdpb.NewDatabaseDaemonClient(cc) }
)

// databaseDaemonStub is set up for dependency injection.
type databaseDaemonStub interface {
	RunSQLPlus(context.Context, *dbdpb.RunSQLPlusCMDRequest, ...grpc.CallOption) (*dbdpb.RunCMDResponse, error)
	RunSQLPlusFormatted(context.Context, *dbdpb.RunSQLPlusCMDRequest, ...grpc.CallOption) (*dbdpb.RunCMDResponse, error)
	CheckDatabaseState(context.Context, *dbdpb.CheckDatabaseStateRequest, ...grpc.CallOption) (*dbdpb.CheckDatabaseStateResponse, error)
	BounceDatabase(context.Context, *dbdpb.BounceDatabaseRequest, ...grpc.CallOption) (*dbdpb.BounceDatabaseResponse, error)
	BounceListener(context.Context, *dbdpb.BounceListenerRequest, ...grpc.CallOption) (*dbdpb.BounceListenerResponse, error)
	KnownPDBs(context.Context, *dbdpb.KnownPDBsRequest, ...grpc.CallOption) (*dbdpb.KnownPDBsResponse, error)
	GetDatabaseType(context.Context, *dbdpb.GetDatabaseTypeRequest, ...grpc.CallOption) (*dbdpb.GetDatabaseTypeResponse, error)
	GetDatabaseName(context.Context, *dbdpb.GetDatabaseNameRequest, ...grpc.CallOption) (*dbdpb.GetDatabaseNameResponse, error)
	DataPumpImportAsync(ctx context.Context, req *dbdpb.DataPumpImportAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error)
	DataPumpExportAsync(ctx context.Context, req *dbdpb.DataPumpExportAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error)
	ApplyDataPatchAsync(ctx context.Context, req *dbdpb.ApplyDataPatchAsyncRequest, opts ...grpc.CallOption) (*lropb.Operation, error)
	ListOperations(ctx context.Context, req *lropb.ListOperationsRequest, opts ...grpc.CallOption) (*lropb.ListOperationsResponse, error)
	GetOperation(ctx context.Context, req *lropb.GetOperationRequest, opts ...grpc.CallOption) (*lropb.Operation, error)
	DeleteOperation(ctx context.Context, in *lropb.DeleteOperationRequest, opts ...grpc.CallOption) (*empty.Empty, error)
	DownloadDirectoryFromGCS(ctx context.Context, req *dbdpb.DownloadDirectoryFromGCSRequest, opts ...grpc.CallOption) (*dbdpb.DownloadDirectoryFromGCSResponse, error)
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s -action [CheckDatabase[CDB|PDB],"+
		" [Start|Stop]Database, [Start|Stop]Listeners, RunSQLPlus[Formatted],"+
		" DataPump[Import|Export]Async, ApplyDataPatchAsync,"+
		" DownloadDirectoryFromGCS]"+
		" [-database_name -request_timeout -export_object_type -export_objects -gcs_path -gcs_log_path] [-port|-socket]", os.Args[0])
	flag.PrintDefaults()
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()
	flag.Usage = usage

	hostname, err := os.Hostname()
	if err != nil {
		klog.ErrorS(err, "Failed to retrieve the hostname")
		os.Exit(exitErrorCode)
	}

	// There's a 5 min default timeout on a Dial and  the overall
	// total default timeout of 10 min, all configurable.
	ctx, cancel := context.WithTimeout(context.Background(), *reqTimeout)
	defer cancel()

	conn, err := dbdaemonlib.DatabaseDaemonDialLocalhost(ctx, consts.DefaultDBDaemonPort, grpc.WithBlock())
	if err != nil {
		klog.ErrorS(err, "Failed to dial the Database Daemon")
		os.Exit(exitErrorCode)
	}
	defer conn.Close()

	client := newDatabaseDaemonClient(conn)

	switch *action {
	case "CheckDatabaseCDB":
		klog.InfoS("checking the state of the CDB...", "container/host", hostname)
		_, err := client.CheckDatabaseState(ctx, &dbdpb.CheckDatabaseStateRequest{IsCdb: true, DatabaseName: *cdbName})
		if err != nil {
			klog.ErrorS(err, "failed to check the state of the CDB")
			os.Exit(exitErrorCode)
		}
		klog.InfoS("action succeeded: CDB is healthy", "action", *action, "CDB", *cdbName, "container/host", hostname)

	case "CheckDatabasePDB":
		klog.InfoS("checking the state of the PDB...", "container/host", hostname, "PDB", *databaseName)
		_, err := client.CheckDatabaseState(ctx, &dbdpb.CheckDatabaseStateRequest{DatabaseName: *databaseName})
		if err != nil {
			klog.ErrorS(err, "failed to check the state of the PDB")
			os.Exit(exitErrorCode)
		}
		klog.InfoS("action succeeded: PDB is healthy", "action", *action, "container/host", hostname, "PDB", *databaseName)

	case "RunSQLPlus", "RunSQLPlusFormatted":
		klog.InfoS("Executing a SQL statement...", "action", *action, "container/host", hostname)
		if *commands == "" {
			klog.Errorf("--action=RunSQLPlus requires --commands sub parameter, but none provided")
			os.Exit(exitErrorCode)
		}

		var resp *dbdpb.RunCMDResponse
		if *action == "RunSQLPlus" {
			resp, err = client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: strings.Split(*commands, ";")})
		} else {
			resp, err = client.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: strings.Split(*commands, ";")})
		}
		if err != nil {
			klog.ErrorS(err, "failed to run SQL statement", "sql", *commands)
			os.Exit(exitErrorCode)
		}
		klog.InfoS("action succeeded: SQL statement successfully executed", "action", *action, "sql", *commands, "response", resp)

	case "StopDatabase":
		klog.InfoS("stopping a database...", "container/host", hostname, "PDB", *databaseName)

		resp, err := client.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
			Operation:    dbdpb.BounceDatabaseRequest_SHUTDOWN,
			DatabaseName: *cdbName,
			Option:       "immediate",
		})
		if err != nil {
			klog.ErrorS(err, "failed to stop a database")
			os.Exit(exitErrorCode)
		}
		klog.InfoS("action succeeded: Successfully stopped database", "action", *action, "CDB", *cdbName, "container/host", hostname, "response", resp)

	case "StartDatabase":
		klog.InfoS("starting a database...", "container/host", hostname, "CDB", *databaseName)
		resp, err := client.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
			Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
			DatabaseName: *cdbName,
			Option:       "open",
		})
		if err != nil {
			klog.ErrorS(err, "failed to start a database: %v")
			os.Exit(exitErrorCode)
		}
		klog.InfoS("action succeeded: Successfully started database", "action", *action, "CDB", *cdbName, "host", hostname, "response", resp)

	case "StopListeners":
		for listenerName := range consts.ListenerNames {
			klog.InfoS("stopping listeners...", "container/host", hostname, "listener", listenerName)
			resp, err := client.BounceListener(ctx, &dbdpb.BounceListenerRequest{
				ListenerName: listenerName,
				TnsAdmin:     filepath.Join(fmt.Sprintf(consts.ListenerDir, consts.DataMount), listenerName),
				Operation:    dbdpb.BounceListenerRequest_STOP,
			})
			if err != nil {
				klog.ErrorS(err, "failed to stop a listener", "listener", listenerName)
				os.Exit(exitErrorCode)
			}
			klog.InfoS("action succeeded: Successfully stopped a listener", "action", *action, "listener", listenerName, "container/host", hostname, "response", resp)
		}

	case "StartListeners":
		for listenerName := range consts.ListenerNames {
			klog.InfoS("starting listeners...", "container/host", hostname, "listener", listenerName)
			resp, err := client.BounceListener(ctx, &dbdpb.BounceListenerRequest{
				ListenerName: listenerName,
				TnsAdmin:     filepath.Join(fmt.Sprintf(consts.ListenerDir, consts.DataMount), listenerName),
				Operation:    dbdpb.BounceListenerRequest_START,
			})
			if err != nil {
				klog.ErrorS(err, "failed to start a listener", "listener", listenerName)
				os.Exit(exitErrorCode)
			}
			klog.InfoS("action succeeded: Successfully started a listener", "action", *action, "listener", listenerName, "container/host", hostname, "response", resp)
		}

	case "KnownPDBs":
		klog.InfoS("getting a list of known PDBs...", "container/host", hostname)

		resp, err := client.KnownPDBs(ctx, &dbdpb.KnownPDBsRequest{})
		if err != nil {
			klog.ErrorS(err, "failed to get a list of known PDBs")
			os.Exit(exitErrorCode)
		}
		klog.InfoS("action succeeded: Successfully retrieved list of known PDBs", "action", *action, "container/host", hostname, "response", resp)

	case "GetDatabaseType":
		klog.InfoS("retrieving database type...", "container/host", hostname)

		resp, err := client.GetDatabaseType(ctx, &dbdpb.GetDatabaseTypeRequest{})
		if err != nil {
			klog.ErrorS(err, "failed to retrieve database type")
			os.Exit(exitErrorCode)
		}
		klog.InfoS("action succeeded: Successfully retrieved database type", "action", *action, "container/host", hostname, "response", resp)

	case "GetDatabaseName":
		klog.InfoS("retrieving database name...", "container/host", hostname)

		resp, err := client.GetDatabaseName(ctx, &dbdpb.GetDatabaseNameRequest{})
		if err != nil {
			klog.ErrorS(err, "failed to retrieve database name")
			os.Exit(exitErrorCode)
		}
		klog.InfoS("action succeeded: Successfully retrieved database name", "action", *action, "container/host", hostname, "response", resp)

	case "DataPumpImportAsync":
		klog.InfoS("starting data pump import...", "container/host", hostname, "PDB", databaseName)

		resp, err := client.DataPumpImportAsync(ctx, &dbdpb.DataPumpImportAsyncRequest{
			SyncRequest: &dbdpb.DataPumpImportRequest{
				PdbName:    *databaseName,
				DbDomain:   "gke",
				GcsPath:    *gcsPath,
				GcsLogPath: *gcsLogPath,
				CommandParams: []string{
					"FULL=YES",
					"METRICS=YES",
					"LOGTIME=ALL",
				},
			},
		})
		if err != nil {
			klog.ErrorS(err, "failed to start data pump import")
			os.Exit(exitErrorCode)
		}

		klog.InfoS("action succeeded: Successfully started Data Pump import", "action", *action, "container/host", hostname, "response", resp)

	case "DataPumpExportAsync":
		klog.InfoS("starting data pump export...", "container/host", hostname, "PDB", *databaseName, "objectType", *exportObjectType, "exportObjects", *exportObjects)

		resp, err := client.DataPumpExportAsync(ctx, &dbdpb.DataPumpExportAsyncRequest{
			SyncRequest: &dbdpb.DataPumpExportRequest{
				PdbName:       *databaseName,
				DbDomain:      "gke",
				ObjectType:    *exportObjectType,
				Objects:       *exportObjects,
				FlashbackTime: *flashbackTime,
				GcsPath:       *gcsPath,
				GcsLogPath:    *gcsLogPath,
				CommandParams: []string{
					"METRICS=YES",
					"LOGTIME=ALL",
				},
			},
		})
		if err != nil {
			klog.ErrorS(err, "failed to start data pump export")
			os.Exit(exitErrorCode)
		}
		klog.InfoS("action succeeded: Successfully started data pump export", "action", *action, "container/host", hostname, "response", resp)

	case "ApplyDataPatchAsync":
		klog.InfoS("starting ApplyDataPatchAsync...", "container/host", hostname)

		resp, err := client.ApplyDataPatchAsync(ctx, &dbdpb.ApplyDataPatchAsyncRequest{})
		if err != nil {
			klog.ErrorS(err, "datapatch failed")
			os.Exit(exitErrorCode)
		}
		klog.InfoS("action succeeded: Finished datapatch", "action", *action, "container/host", hostname, "response", resp)

	case "ListOperations":
		klog.InfoS("running ListOperations...")
		resp, err := client.ListOperations(ctx, &lropb.ListOperationsRequest{})
		if err != nil {
			klog.ErrorS(err, "failed listing operations")
			os.Exit(exitErrorCode)
		}

		klog.InfoS("action succeeded: Successfully listed operations", "action", *action, "container/host", hostname, "response", resp)

	case "GetOperation":
		klog.InfoS("running GetOperation...")
		resp, err := client.GetOperation(ctx, &lropb.GetOperationRequest{Name: *operationId})
		if err != nil {
			klog.ErrorS(err, "failed getting operation", "id", operationId)
			os.Exit(exitErrorCode)
		}

		klog.InfoS("action succeeded: Successfully retrieved operation", "action", *action, "container/host", hostname, "response", resp)

	case "DeleteOperation":
		klog.InfoS("running DeleteOperation...")
		resp, err := client.DeleteOperation(ctx, &lropb.DeleteOperationRequest{Name: *operationId})
		if err != nil {
			klog.ErrorS(err, "failed deleting operation", "id", operationId)
			os.Exit(exitErrorCode)
		}

		klog.InfoS("action succeeded: Successfully deleted operation", "action", *action, "container/host", hostname, "response", resp)
	case "DownloadDirectoryFromGCS":
		klog.InfoS("download from GCS bucket", "gcsPath", *gcsPath)
		_, err := client.DownloadDirectoryFromGCS(ctx, &dbdpb.DownloadDirectoryFromGCSRequest{GcsPath: *gcsPath, LocalPath: consts.DefaultRMANDir})
		if err != nil {
			klog.ErrorS(err, "failed downloading directory from gcs bucket", "gcs", *gcsPath, "local path", consts.DefaultRMANDir)
			os.Exit(exitErrorCode)
		}
		klog.InfoS("download succeeded")
	case "":
		flag.Usage()

	default:
		klog.Errorf("Unknown action: %q", *action)
		os.Exit(exitErrorCode)
	}
}
