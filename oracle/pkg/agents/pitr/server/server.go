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

package server

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/pitr"
	pb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/pitr/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PITRServer represents a PITRAgentServer
type PITRServer struct {
	pb.UnimplementedPITRAgentServer
	DBService     string
	DBPort        int
	MetadataStore *pitr.SimpleStore
}

// Status returns the metadata of archived redo logs.
func (s *PITRServer) Status(ctx context.Context, _ *pb.StatusRequest) (*pb.StatusResponse, error) {
	metadata := pitr.LogMetadata{}
	s.MetadataStore.Lock()
	defer s.MetadataStore.UnLock()
	if err := s.MetadataStore.Read(ctx, pitr.MetadataStorePath, &metadata); err != nil {
		return nil, fmt.Errorf("failed to read metadata: %v", err)
	}

	tToEntry := metadata.KeyToLogEntry
	windows := pitr.Merge(metadata)
	var ranges []*pb.Range
	for _, w := range windows {
		startLog := tToEntry[w[0]]
		endLog := tToEntry[w[1]]
		ranges = append(ranges, &pb.Range{
			Start: &pb.Instant{
				Time:        timestamppb.New(startLog.FirstTime),
				Scn:         startLog.FirstChange,
				Incarnation: startLog.Incarnation,
			},
			End: &pb.Instant{
				Time:        timestamppb.New(endLog.NextTime),
				Scn:         endLog.NextChange,
				Incarnation: endLog.Incarnation,
			},
		})
	}

	return &pb.StatusResponse{
		RecoveryWindows: ranges,
	}, nil
}
