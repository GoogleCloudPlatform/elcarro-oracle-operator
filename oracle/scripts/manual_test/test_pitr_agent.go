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

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/pitrcontroller"
	pb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/pitr/proto"
	"google.golang.org/grpc"
	log "k8s.io/klog/v2"
)

func testPITRAgent() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	//update below variables to match with your test env
	inst := "mydb"
	ns := "db"
	svc := "service/" + fmt.Sprintf(pitrcontroller.PITRSvcTemplate, inst)
	agentPort := strconv.Itoa(pitrcontroller.DefaultPITRAgentPort)
	// by default, use current default kubectl context (kubectl config current-context)
	if false {
		kctx := ""

		args := []string{
			"port-forward",
			svc,
			agentPort + ":" + agentPort,
			"-n",
			ns,
		}
		if kctx != "" {
			args = append(args, "--context="+kctx)
		}

		cmd := exec.CommandContext(ctx, "kubectl", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			log.Info(fmt.Sprintf(stderr.String()))
			log.Error(err, "failed to start port forwarding")
		}

		defer func() {
			if err := cmd.Process.Kill(); err != nil {
				log.Error(err, "failed to stop port forwarding")
			}
		}()
	}

	conn, err := grpc.DialContext(ctx, fmt.Sprintf("%s:%s", "127.0.0.1", agentPort), grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Error(err, "failed to create a conn via gRPC.Dial")
		os.Exit(1)
	}

	defer conn.Close()
	c := pb.NewPITRAgentClient(conn)
	// replace below code to test other PITR agent APIs
	resp, err := c.Status(ctx, &pb.StatusRequest{})
	log.Info(fmt.Sprintf("resp: %v, err: %v", resp, err))
}
