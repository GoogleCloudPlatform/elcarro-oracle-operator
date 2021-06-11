package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"google.golang.org/grpc"
	log "k8s.io/klog/v2"
)

func testDbdaemon() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// update below variables to match with your test env
	inst := "mydb"
	ns := "db"
	svc := "service/" + fmt.Sprintf(controllers.DbdaemonSvcName, inst)
	dbdaemonPort := strconv.Itoa(consts.DefaultDBDaemonPort)
	// by default, use current default kubectl context (kubectl config current-context)
	kctx := ""

	args := []string{
		"port-forward",
		svc,
		dbdaemonPort + ":" + dbdaemonPort,
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

	conn, err := grpc.DialContext(ctx, fmt.Sprintf("%s:%s", "127.0.0.1", dbdaemonPort), grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Error(err, "failed to create a conn via gRPC.Dial")
		os.Exit(1)
	}

	defer conn.Close()
	c := dbdpb.NewDatabaseDaemonClient(conn)
	// replace below code to test other dbdaemon APIs
	resp, err := c.GetDatabaseName(ctx, &dbdpb.GetDatabaseNameRequest{})
	log.Info(fmt.Sprintf("resp: %v, err: %v", resp, err))
}
