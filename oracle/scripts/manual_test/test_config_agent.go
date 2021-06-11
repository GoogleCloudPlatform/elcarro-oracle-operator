package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	capb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/config_agent/protos"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	"google.golang.org/grpc"
	log "k8s.io/klog/v2"
)

func testConfigAgent() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// update below variables to match with your test env
	inst := "mydb"
	ns := "db"
	svc := "service/" + fmt.Sprintf(controllers.AgentSvcName, inst)
	agentPort := strconv.Itoa(consts.DefaultConfigAgentPort)
	// by default, use current default kubectl context (kubectl config current-context)
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

	conn, err := grpc.DialContext(ctx, fmt.Sprintf("%s:%s", "127.0.0.1", agentPort), grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Error(err, "failed to create a conn via gRPC.Dial")
		os.Exit(1)
	}

	defer conn.Close()
	caClient := capb.NewConfigAgentClient(conn)
	// replace below code to test other config agent APIs
	resp, err := caClient.GetParameterTypeValue(ctx, &capb.GetParameterTypeValueRequest{Keys: []string{"db_block_size"}})
	log.Info(fmt.Sprintf("resp: %v, err: %v", resp, err))
}
