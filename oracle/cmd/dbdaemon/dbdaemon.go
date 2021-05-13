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

// Database Daemon listens on consts.DomainSocketFile domain socket file
// (or optionally on a specified TCP port) and accepts requests from other
// data plane agents running in containers.
//
// Usage:
//   dbdaemon
//
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/user"
	"syscall"

	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/database/dbdaemon"
)

const (
	lockFile      = "/var/tmp/dbdaemon.lock"
	exitErrorCode = consts.DefaultExitErrorCode
)

var cdbNameFromYaml = flag.String("cdb_name", "GCLOUD", "Name of the CDB to create")

// A user running this program should not be root and
// a primary group should be either dba or oinstall.
func userCheck(skipChecking bool) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("userCheck: could not determine the current user: %v", err)
	}
	if skipChecking {
		klog.InfoS("skipped by request, setting user", "username", u.Username)
		return nil
	}

	if u.Username == "root" {
		return fmt.Errorf("userCheck: this program is designed to run by the Oracle software installation owner (e.g. oracle), not %q", u.Username)
	}

	groups := []string{"dba", "oinstall"}
	var gIDs []string
	for _, group := range groups {
		g, err := user.LookupGroup(group)
		// Not both groups are mandatory, e.g. oinstall may not exist.
		klog.InfoS("checking groups", "group", group, "g", g)
		if err != nil {
			continue
		}
		gIDs = append(gIDs, g.Gid)
	}
	for _, g := range gIDs {
		if u.Gid == g {
			return nil
		}
	}
	return fmt.Errorf("userCheck: current user's primary group (GID=%q) is not dba|oinstall (GID=%q)", u.Gid, gIDs)
}

func agentInit() error {
	lock, err := os.Create(lockFile)
	if err != nil {
		klog.ErrorS(err, "failed to access lock file", "lockFile", lockFile)
		return err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		klog.ErrorS(err, "failed to obtain a lock on lock file. Another instance of Database Daemon may be running", "lockFile", lockFile)
		return err
	}

	return nil
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	var (
		lis net.Listener
		err error
	)

	if err := agentInit(); err != nil {
		os.Exit(exitErrorCode)
	}

	lis, err = net.Listen("tcp", fmt.Sprintf(":%d", consts.DefaultDBDaemonPort))

	if err != nil {
		klog.ErrorS(err, "listen call failed")
		os.Exit(exitErrorCode)
	}
	defer lis.Close()

	hostname, err := os.Hostname()
	if err != nil {
		klog.ErrorS(err, "failed to get hostname")
		os.Exit(exitErrorCode)
	}

	grpcSvr := grpc.NewServer()
	dbdaemonServer, err := dbdaemon.New(context.Background(), *cdbNameFromYaml)
	if err != nil {
		klog.ErrorS(err, "failed to execute dbdaemon.New")
		os.Exit(exitErrorCode)
	}
	dbdpb.RegisterDatabaseDaemonServer(grpcSvr, dbdaemonServer)

	klog.InfoS("Starting a Database Daemon...", "host", hostname, "listenerAddr", lis.Addr())
	grpcSvr.Serve(lis)
}
