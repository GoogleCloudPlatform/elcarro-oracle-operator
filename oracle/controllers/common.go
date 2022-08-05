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
	"strings"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

const (
	FinalizerName              = "oracle.db.anthosapis.com"
	PhysBackupTimeLimitDefault = 60 * time.Minute
	StatusReady                = "Ready"
	StatusInProgress           = "InProgress"

	RestoreInProgress = "Restore" + StatusInProgress
	CreateInProgress  = "Create" + StatusInProgress

	PITRLabel               = "pitr"
	IncarnationLabel        = "incarnation"
	ParentIncarnationLabel  = "parent-incarnation"
	SCNAnnotation           = "scn"
	TimestampAnnotation     = "timestamp"
	DatabaseImageAnnotation = "database-image"
)

var (
	// SvcName is a string template for service names.
	SvcName = "%s-svc"
	// AgentSvcName is a string template for agent service names.
	AgentSvcName = "%s-agent-svc"
	// DbdaemonSvcName is a string template for dbdaemon service names.
	DbdaemonSvcName = "%s-dbdaemon-svc"
	// SvcEndpoint is a string template for service endpoints.
	SvcEndpoint     = "%s.%s" // SvcName.namespaceName
	sourceCidrRange = []string{"0.0.0.0/0"}
	// StsName is a string template for Database stateful set names.
	StsName = "%s-sts"
	// AgentDeploymentName is a string template for agent deployment names.
	AgentDeploymentName = "%s-agent-deployment"
	// PvcMountName is a string template for pvc names.
	PvcMountName = "%s-pvc-%s" // inst.name-pvc-mount, e.g. mydb-pvc-u02
	// CmName is a string template for config map names.
	CmName = "%s-cm"
	// DatabasePodAppLabel is the 'app' label assigned to db pod.
	DatabasePodAppLabel = "db-op"
	// DefaultDiskSpecs is the default DiskSpec settings.
	DefaultDiskSpecs = map[string]commonv1alpha1.DiskSpec{
		"DataDisk": {
			Name: "DataDisk",
			Size: resource.MustParse("100Gi"),
		},
		"LogDisk": {
			Name: "LogDisk",
			Size: resource.MustParse("150Gi"),
		},
		"BackupDisk": {
			Name: "BackupDisk",
			Size: resource.MustParse("100Gi"),
		},
	}
	defaultDiskMountLocations = map[string]string{
		"DataDisk":   "u02",
		"LogDisk":    "u03",
		"BackupDisk": "u04",
	}
)

// StsParams stores parameters for creating a database stateful set.
type StsParams struct {
	Inst           *v1alpha1.Instance
	Scheme         *runtime.Scheme
	Namespace      string
	Images         map[string]string
	SvcName        string
	StsName        string
	PrivEscalation bool
	ConfigMap      *corev1.ConfigMap
	Restore        *v1alpha1.RestoreSpec
	Disks          []commonv1alpha1.DiskSpec
	Config         *v1alpha1.Config
	Log            logr.Logger
	Services       []commonv1alpha1.Service
}

// AgentDeploymentParams stores parameters for creating a agent deployment.
type AgentDeploymentParams struct {
	Config         *v1alpha1.Config
	Inst           *v1alpha1.Instance
	Scheme         *runtime.Scheme
	Images         map[string]string
	PrivEscalation bool
	Name           string
	Log            logr.Logger
	Args           map[string][]string
	Services       []commonv1alpha1.Service
}

type ConnCloseFunc func()

type GRPCDatabaseClientFactory struct {
	dbclient *dbdpb.DatabaseDaemonClient
}

// DatabaseClientFactory is a GRPC implementation of DatabaseClientFactory. Exists for test mock.
type DatabaseClientFactory interface {
	// New returns new Client.
	// connection close function should be invoked by the caller if
	// error is nil.
	New(ctx context.Context, r client.Reader, namespace, instName string) (dbdpb.DatabaseDaemonClient, func() error, error)
}

// GetPVCNameAndMount returns PVC names and their corresponding mount.
func GetPVCNameAndMount(instName, diskName string) (string, string) {
	spec := DefaultDiskSpecs[diskName]
	mountLocation := defaultDiskMountLocations[spec.Name]
	pvcName := fmt.Sprintf(PvcMountName, instName, mountLocation)
	return pvcName, mountLocation
}

// New returns a new database daemon client
func (d *GRPCDatabaseClientFactory) New(ctx context.Context, r client.Reader, namespace, instName string) (dbdpb.DatabaseDaemonClient, func() error, error) {
	var dbservice = fmt.Sprintf(DbdaemonSvcName, instName)
	svc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Name: dbservice, Namespace: namespace}, svc); err != nil {
		return nil, nil, err
	}

	conn, err := common.DatabaseDaemonDialService(ctx, fmt.Sprintf("%s:%d", svc.Spec.ClusterIP, consts.DefaultDBDaemonPort), grpc.WithBlock())
	if err != nil {
		return nil, func() error { return nil }, err
	}
	return dbdpb.NewDatabaseDaemonClient(conn), conn.Close, nil
}

// Contains check whether given "elem" presents in "array"
func Contains(array []string, elem string) bool {
	for _, v := range array {
		if v == elem {
			return true
		}
	}
	return false
}

// GetBackupGcsPath resolves the actual gcs path based on backup spec.
func GetBackupGcsPath(backup *v1alpha1.Backup) string {
	gcsPath := backup.Spec.GcsPath
	if backup.Spec.GcsDir != "" {
		if !strings.HasSuffix(backup.Spec.GcsDir, "/") {
			gcsPath = backup.Spec.GcsDir + "/"
		}
		gcsPath = gcsPath + backup.Name
	}
	return gcsPath
}
