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

package pitrcontroller

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	pb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/pitr/proto"
)

type RealBackupControl struct {
	Client client.Client
}

func (r *RealBackupControl) List(ctx context.Context, opts ...client.ListOption) ([]v1alpha1.Backup, error) {
	var backupList v1alpha1.BackupList
	err := r.Client.List(ctx, &backupList, opts...)
	if err != nil {
		return nil, err
	}
	var backups []v1alpha1.Backup
	for _, b := range backupList.Items {
		if b.DeletionTimestamp != nil {
			continue
		}
		backups = append(backups, *b.DeepCopy())
	}
	return backups, nil
}

type RealPITRControl struct {
	Client client.Client
}

func (r *RealPITRControl) AvailableRecoveryWindows(ctx context.Context, p *v1alpha1.PITR) ([]*pb.Range, error) {
	agentSvc := &corev1.Service{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: fmt.Sprintf(PITRSvcTemplate, p.GetName()), Namespace: p.GetNamespace()}, agentSvc); err != nil {
		return nil, err
	}
	conn, err := grpc.Dial(fmt.Sprintf("%s:%d", agentSvc.Spec.ClusterIP, DefaultPITRAgentPort), grpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("failed to create a conn via gRPC.Dial: %w", err)
	}
	defer conn.Close()
	c := pb.NewPITRAgentClient(conn)
	resp, err := c.Status(ctx, &pb.StatusRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetRecoveryWindows(), nil
}

func (r *RealPITRControl) UpdateStatus(ctx context.Context, p *v1alpha1.PITR) error {
	return r.Client.Status().Update(ctx, p)
}
