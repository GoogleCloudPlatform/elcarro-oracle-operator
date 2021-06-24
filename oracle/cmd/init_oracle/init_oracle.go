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
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/compute/metadata"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	dbdaemonlib "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/database/provision"
)

const (
	bootstrapTimeout       = 19 * time.Minute
	minRequiredFreeMemInKB = 6 * 1000 * 1000 // At least 6 Gigs is required for consistently successful bootstrapping
)

var (
	supportedVersions = map[string]bool{"12.2": true, "18.3": true, "18c": true, "19.2": true, "19.3": true}
	pgaMB             = flag.Uint64("pga", consts.DefaultPGAMB, "Oracle Database PGA memory sizing in MB")
	sgaMB             = flag.Uint64("sga", consts.DefaultSGAMB, "Oracle Database SGA memory sizing in MB")
	dbDomain          = flag.String("db_domain", "", "Oracle db_domain init parameter")
	cdbNameFromYaml   = flag.String("cdb_name", "GCLOUD", "Name of the CDB to create")
	zoneName          string
	zoneNameOnce      sync.Once
)

type task interface {
	GetName() string
	Call(ctx context.Context) error
}

var newBootstrapDatabaseTask = func(ctx context.Context, isCDB bool, cdbNameFromImage, cdbNameFromYaml, version string, pgaMB, sgaMB uint64, p bool, dbdClient dbdpb.DatabaseDaemonClient) (task, error) {
	host, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	return provision.NewBootstrapDatabaseTask(ctx, isCDB, true, cdbNameFromImage, cdbNameFromYaml, version, zone(), host, *dbDomain, pgaMB, sgaMB, p, dbdClient)
}

var newDBDClient = func(ctx context.Context) (dbdpb.DatabaseDaemonClient, func() error, error) {
	conn, err := dbdaemonlib.DatabaseDaemonDialLocalhost(ctx, consts.DefaultDBDaemonPort, grpc.WithBlock())
	if err != nil {
		return nil, func() error { return nil }, err
	}
	return dbdpb.NewDatabaseDaemonClient(conn), conn.Close, nil
}

func zone() string {
	zone, err := retrieveZoneName()
	if err != nil {
		klog.InfoS("failed to retrieve a zone. Running outside of GCP?", "err", err)
		zone = "generic"
	}

	return zone
}

// retrieveZoneName returns the zone of the GCE VM. It caches the value since the zone will never
// change.
func retrieveZoneName() (string, error) {
	var err error
	zoneNameOnce.Do(func() {
		zoneName, err = metadata.Zone()
		klog.InfoS("zoneName", "zoneName", zoneName)
	})
	if err != nil {
		return "", err
	}

	return zoneName, nil
}

func provisionHost(ctx context.Context, cdbNameFromImage string, cdbNameFromYaml string, version string) error {
	p, err := hostProvisioned(ctx)
	if err != nil {
		return fmt.Errorf("failed to determine host provision state: %v", err)
	}
	dbdClient, closeConn, err := newDBDClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create database daemon client: %v", err)
	}
	defer closeConn()

	task, err := newBootstrapDatabaseTask(ctx, true, cdbNameFromImage, cdbNameFromYaml, version, *pgaMB, *sgaMB, p, dbdClient)
	if err != nil {
		return fmt.Errorf("failed to create bootstrap task: %v", err)
	}

	if err := task.Call(ctx); err != nil {
		return fmt.Errorf("failed to bootstrap database : %v", err)
	}

	if !p {
		if err := markProvisioned(); err != nil {
			return err
		}
	}
	return nil
}

//  hostProvisioned returns true if provisioner already ran.
func hostProvisioned(ctx context.Context) (bool, error) {
	_, err := os.Stat(consts.ProvisioningDoneFile)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	// Something is wrong. Do not start provisioning.
	return false, fmt.Errorf("failed to determine host status: %v", err)
}

// markProvisioned creates a flag file to indicate that provisioning completed successfully
func markProvisioned() error {
	f, err := os.Create(consts.ProvisioningDoneFile)
	if err != nil {
		return fmt.Errorf("could not create %s file: %v", consts.ProvisioningDoneFile, err)
	}
	defer f.Close()
	return nil
}

func postProvision(ctx context.Context, oracleHome, cdbName string) error {
	if err := provision.RelinkConfigFiles(oracleHome, cdbName); err != nil {
		return err
	}
	dbdClient, closeConn, err := newDBDClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create database daemon client: %v", err)
	}
	defer closeConn()
	if _, err := dbdClient.BounceDatabase(ctx, &dbdpb.BounceDatabaseRequest{
		DatabaseName: cdbName,
		Operation:    dbdpb.BounceDatabaseRequest_STARTUP,
	}); err != nil {
		klog.Error(err, "startup failed")
	}
	if _, err := dbdClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{"alter pluggable database all open"},
	}); err != nil {
		klog.Error(err, "open pdb failed")
	}
	if _, err := dbdClient.BounceListener(ctx, &dbdpb.BounceListenerRequest{
		ListenerName: "SECURE",
		TnsAdmin:     filepath.Join(fmt.Sprintf(consts.ListenerDir, consts.DataMount), consts.SECURE),
		Operation:    dbdpb.BounceListenerRequest_START,
	}); err != nil {
		klog.Error(err, "start listener failed")
	}
	return nil
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), bootstrapTimeout)
	defer cancel()

	oracleHome, cdbNameFromImage, version, err := provision.FetchMetaDataFromImage(provision.MetaDataFile)
	if err != nil {
		klog.Error(err, "error while parsing image's metadata file")
		os.Exit(consts.DefaultExitErrorCode)
	}

	if !supportedVersions[version] {
		klog.InfoS("preflight check", "unsupported version", version)
		os.Exit(consts.DefaultExitErrorCode)
	}

	if freeMem, err := getFreeMemInfoFromProc(); err != nil || freeMem < minRequiredFreeMemInKB {
		klog.InfoS("Unable to determine free memory or not enough memory available to initiate bootstrapping", "available free memory", freeMem, "required free mem", minRequiredFreeMemInKB)
		os.Exit(consts.DefaultExitErrorCode)
	}

	klog.InfoS("metadata is as follows", "home", oracleHome, "cdbNameFromYaml", *cdbNameFromYaml, "version", version)
	if cdbNameFromImage == "" {
		if _, err := os.Stat(consts.ProvisioningDoneFile); err == nil {
			if err := postProvision(ctx, oracleHome, *cdbNameFromYaml); err != nil {
				klog.Error(err, "postProvision failed")
			}
		} else {
			klog.InfoS("CDB provisioning skipped")
			if *cdbNameFromYaml != "" {
				klog.InfoS("CDB name presents in yaml, relink config files")
				if err := provision.RelinkConfigFiles(oracleHome, *cdbNameFromYaml); err != nil {
					klog.ErrorS(err, "RelinkConfigFiles failed")
				}
			}
		}
		os.Exit(consts.DefaultExitErrorCode)
	}
	klog.InfoS("image contains CDB, starting provisioning")

	if err := provisionHost(ctx, cdbNameFromImage, *cdbNameFromYaml, version); err != nil {
		klog.ErrorS(err, "CDB provisioning failed")
		os.Exit(consts.DefaultExitErrorCode)
	}
	klog.InfoS("CDB provisioning: DONE")
}

func getFreeMemInfoFromProc() (int, error) {
	content, err := ioutil.ReadFile("/proc/meminfo")
	if err != nil {
		return -1, fmt.Errorf("unable to read /proc/meminfo file")
	}
	buffer := bytes.NewBuffer(content)
	for {
		line, err := buffer.ReadString('\n')
		if err != nil && err != io.EOF {
			break
		}
		// An example MemAvailable info line looks as follows
		// MemAvailable:         1094508 kB
		if ndx := strings.Index(line, "MemAvailable:"); ndx >= 0 {
			s := strings.Split(line, ":")
			if len(s) != 2 {
				return -1, fmt.Errorf("error while parsing available memory info")
			}
			line = strings.TrimSpace(s[1])
			// Discard the last 3 characters in the line
			if mem, err := strconv.Atoi(line[:len(line)-3]); err == nil {
				klog.InfoS("Available memory size is ", "MemAvailable in KB", mem)
				return mem, nil
			}
		}
	}
	return -1, fmt.Errorf("unable to determine available memory")
}
