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

package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/pitr"
	pb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/pitr/proto"
	pitrServer "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/pitr/server"

	"google.golang.org/grpc"
	"k8s.io/klog/v2"
)

var port = flag.Int("port", 0, "The tcp port of a PITR Agent server.")
var dbservice = flag.String("dbservice", "", "The DB service.")
var dbport = flag.Int("dbport", 0, "The DB service port.")
var dest = flag.String("dest", "", "The dest url to the replication destination location")
var retentionDays = flag.Int("retentiondays", 7, "how long(in days) PITR need to retain redo logs")

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	if !strings.HasPrefix(*dest, "gs://") {
		klog.Error("invalid dest url for replication, only support GCS in the current release", "dest", *dest)
		os.Exit(1)
	}
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		klog.ErrorS(err, "PITR Agent failed to listen")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := common.DatabaseDaemonDialService(ctx, fmt.Sprintf("%s:%d", *dbservice, *dbport), grpc.WithBlock())
	if err != nil {
		klog.ErrorS(err, "PITR Agent failed to connect to dbdaemon")
		os.Exit(1)
	}
	defer conn.Close()
	dbdClient := dbdpb.NewDatabaseDaemonClient(conn)

	if err := pitr.SetArchiveLag(ctx, dbdClient); err != nil {
		klog.ErrorS(err, "failed to set the archive lag parameter")
		os.Exit(1)
	}

	mDir := *dest
	if !strings.HasSuffix(*dest, "/") {
		mDir = *dest + "/"
	}

	hashStore, err := pitr.NewSimpleStore(ctx, mDir+"hash/")
	if err != nil {
		klog.ErrorS(err, "failed to create hash store")
		os.Exit(1)
	}
	defer hashStore.Close(ctx)

	metadataStore, err := pitr.NewSimpleStore(ctx, mDir)
	if err != nil {
		klog.ErrorS(err, "failed to create metadata store")
		os.Exit(1)
	}
	defer metadataStore.Close(ctx)

	go func() {
		if err := pitr.RunLogReplication(ctx, dbdClient, *dest, hashStore); err != nil {
			klog.Error(err, "failed to start log replication")
		}
		cancel()
	}()

	go func() {
		if err := pitr.RunMetadataUpdate(ctx, dbdClient, hashStore, metadataStore); err != nil {
			klog.Error(err, "failed to start metadata update")
		}
		cancel()
	}()

	go func() {
		if err := pitr.RunLogRetention(ctx, *retentionDays, metadataStore, hashStore); err != nil {
			klog.Error(err, "failed to start log retention")
		}
		cancel()
	}()

	grpcSvr := grpc.NewServer()
	pb.RegisterPITRAgentServer(grpcSvr, &pitrServer.PITRServer{DBService: *dbservice, DBPort: *dbport, MetadataStore: metadataStore})
	go func() {
		klog.InfoS("Starting PITR Agent", "port", *port)
		if err := grpcSvr.Serve(lis); err != nil {
			klog.ErrorS(err, "PITR Agent failed to start")
		}
		cancel()
	}()

	<-ctx.Done()
	klog.Info("Exiting PITR agent")
	grpcSvr.Stop()
}
