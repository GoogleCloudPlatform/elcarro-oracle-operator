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

package secret

import (
	"context"
	"fmt"
	"sync"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "google.golang.org/genproto/googleapis/cloud/secretmanager/v1"
)

const gsmSecretStr = "projects/%s/secrets/%s/versions/%s"

// GSMSecretAccessor returns an accessor to retrieve decrypted credential for the provided GSM secret specification.
type GSMSecretAccessor struct {
	projectId string
	secretId  string
	version   string
	passwd    *string
	mu        sync.Mutex
}

// Get returns the decrypted value of this secret and cache it for later invocation.
func (g *GSMSecretAccessor) Get(ctx context.Context) (string, error) {
	if g.passwd != nil {
		return *g.passwd, nil
	}
	// secret value will not change.
	g.mu.Lock()
	defer g.mu.Unlock()
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()
	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: fmt.Sprintf(gsmSecretStr, g.projectId, g.secretId, g.version),
	}

	// Call the API.
	result, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to access secret version: %v", err)
	}

	return string(result.Payload.Data[:]), nil
}

// Clear cleans up the cached value.
func (g *GSMSecretAccessor) Clear() {
	// best effort to clear password from memory, expect GC to clear the string.
	g.mu.Lock()
	defer g.mu.Unlock()
	g.passwd = nil
}

// NewGSMSecretAccessor returns a Google Secret Manager secret accessor.
func NewGSMSecretAccessor(projectId, secretId, version string) *GSMSecretAccessor {
	return &GSMSecretAccessor{
		projectId: projectId,
		secretId:  secretId,
		version:   version,
	}
}
