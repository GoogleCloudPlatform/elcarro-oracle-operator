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

// Database Daemon Proxy

package main

import (
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
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/database/dbdaemonproxy"
)

const (
	lockFile      = "/var/tmp/dbdaemon_proxy.lock"
	exitErrorCode = consts.DefaultExitErrorCode
)

var (
	sockFile        = flag.String("socket", consts.ProxyDomainSocketFile, "Path to the domain socket file for a Database Daemon Proxy.")
	port            = flag.Int("port", 0, "Optional port to bind a Database Daemon Proxy to.")
	skipUserCheck   = flag.Bool("skip_user_check", false, "Optionally skip a check of a user who runs the Database Daemon Proxy (by default it should be a database software owner)")
	cdbNameFromYaml = flag.String("cdb_name", "GCLOUD", "Name of the CDB to create")
)

// A user running this program should not be root and
// a primary group should be either dba or oinstall.
func userCheck(skipChecking bool) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("dbdaemonproxy/userCheck: failed to determine the current user: %v", err)
	}
	if skipChecking {
		klog.InfoS("dbdaemonproxy/userCheck: skipped by request", "username", u.Username)
		return nil
	}

	if u.Username == "root" {
		return fmt.Errorf("dbdaemonproxy/userCheck: this program is designed to run by the Oracle software installation owner (e.g. oracle), not %q", u.Username)
	}

	groups := []string{"dba", "oinstall"}
	var gIDs []string
	for _, group := range groups {
		g, err := user.LookupGroup(group)
		// Not both groups are mandatory, e.g. oinstall may not exist.
		klog.InfoS("dbdaemonproxy/userCheck", "group", group, "g", g)
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
	return fmt.Errorf("dbdaemonproxy/userCheck: current user's primary group (GID=%q) is not dba|oinstall (GID=%q)", u.Gid, gIDs)
}

func agentInit() error {
	lock, err := os.Create(lockFile)
	if err != nil {
		klog.ErrorS(err, "dbdaemonproxy/agentInit: failed to access the lock file", "lockFile", lockFile)
		return err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		klog.ErrorS(err, "dbdaemonproxy/agentInit: failed to obtain a lock. Another instance of the Database Daemon may be running", "lockFile", lockFile)
		return err
	}
	// If domain socket file exists remove it before listener tries to create it.
	if err = os.Remove(*sockFile); err != nil && !os.IsNotExist(err) {
		klog.ErrorS(err, "dbdaemonproxy/agentInit: failed to remove socket file", "sockFile", *sockFile)
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

	if err := userCheck(*skipUserCheck); err != nil {
		klog.ErrorS(err, "dbdaemonproxy/main: failed a requested user check")
		os.Exit(exitErrorCode)
	}

	if err := agentInit(); err != nil {
		os.Exit(exitErrorCode)
	}

	if *port == 0 {
		lis, err = net.Listen("unix", *sockFile)
	} else {
		lis, err = net.Listen("tcp", fmt.Sprintf("localhost:%d", *port))
	}
	if err != nil {
		klog.ErrorS(err, "dbdaemonproxy/main: listen call failed")
		os.Exit(exitErrorCode)
	}
	defer lis.Close()

	if *port == 0 {
		// Only root or a Database Daemon user id is allowed to communicate with
		// the Database Daemon via socket file.
		if err = os.Chmod(*sockFile, 0700); err != nil {
			klog.ErrorS(err, "dbdaemonproxy/main: failed to set permissions on socket file %q: %v", "sockFile", *sockFile)
			os.Exit(exitErrorCode)
		}
	}

	hostname, err := os.Hostname()
	if err != nil {
		klog.ErrorS(err, "dbdaemonproxy/main: failed to get a hostname")
		os.Exit(exitErrorCode)
	}

	grpcSvr := grpc.NewServer()
	s, err := dbdaemonproxy.New(hostname, *cdbNameFromYaml)
	if err != nil {
		klog.ErrorS(err, "dbdaemonproxy/main: failed to execute New")
		os.Exit(exitErrorCode)
	}
	dbdpb.RegisterDatabaseDaemonProxyServer(grpcSvr, s)

	klog.InfoS("Starting a Database Daemon Proxy...", "host", hostname, "address", lis.Addr())
	grpcSvr.Serve(lis)
}
