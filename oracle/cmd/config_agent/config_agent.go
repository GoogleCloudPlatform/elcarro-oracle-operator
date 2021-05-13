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
	"flag"
	"fmt"
	"net"
	"os"

	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	pb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	ca "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/server"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
)

var port = flag.Int("port", consts.DefaultConfigAgentPort, "The tcp port of a Config Agent server.")
var dbservice = flag.String("dbservice", "", "The DB service.")
var dbport = flag.Int("dbport", 0, "The DB service port.")

func main() {
	klog.InitFlags(nil)
	flag.Parse()
	// Start grpc service of config agent.
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		klog.ErrorS(err, "Config Agent failed to listen")
		os.Exit(1)
	}

	hostname, err := os.Hostname()
	if err != nil {
		klog.ErrorS(err, "Config Agent failed to get a hostname")
		os.Exit(1)
	}

	grpcSvr := grpc.NewServer()

	pb.RegisterConfigAgentServer(grpcSvr, &ca.ConfigServer{DBService: *dbservice, DBPort: *dbport})

	klog.InfoS("Starting Config Agent", "hostname", hostname, "port", *port)
	if err := grpcSvr.Serve(lis); err != nil {
		klog.ErrorS(err, "Config Agent failed to start")
		os.Exit(1)
	}
}
