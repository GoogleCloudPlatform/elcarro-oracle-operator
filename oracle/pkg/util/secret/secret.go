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
