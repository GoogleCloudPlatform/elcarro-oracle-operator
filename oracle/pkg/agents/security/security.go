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

// Package security contains common methods regarding encryption and passwords.
package security

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"math/big"
	"time"
	"unicode"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	connect "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

const (
	passLength   = 10
	alterUserSQL = "alter user %s identified by %s"
)

type dockerClient interface {
	client.ContainerAPIClient
	Close() error
}

type runSQLOnClient interface {
	RunSQLPlus(context.Context, *dbdpb.RunSQLPlusCMDRequest, ...grpc.CallOption) (*dbdpb.RunCMDResponse, error)
}

type runSQLOnServer interface {
	RunSQLPlus(context.Context, *dbdpb.RunSQLPlusCMDRequest) (*dbdpb.RunCMDResponse, error)
}

// Security provides login and encryption methods.
type Security struct {
	dockerClient dockerClient
	sqlOpen      func(string, string) (*sql.DB, error)
	pollInterval time.Duration
	dbdConn      *grpc.ClientConn
	dbdClient    runSQLOnClient
}

// Close closes any Security resources and connections.
func (s *Security) Close() error {
	if s.dockerClient != nil {
		if err := s.dockerClient.Close(); err != nil {
			if s.dbdConn != nil {
				if err2 := s.dbdConn.Close(); err2 != nil {
					return err2
				}
			}
			return err
		}
	}

	if s.dbdConn != nil {
		return s.dbdConn.Close()
	}

	return nil
}

// RandOraclePassword returns a random password containing letters and numbers.
// It is caller's responsibility to handle the error.
func RandOraclePassword() (string, error) {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	const numbers = "0123456789"
	const alphanumeric = chars + numbers
	result := make([]byte, passLength-1)

	hasNumeric := false

	for i := 0; i < passLength-1; i++ {
		aRand, err := randInt(len(alphanumeric))
		if err != nil {
			return "", err
		}
		ch := alphanumeric[aRand]
		if unicode.IsNumber(rune(ch)) {
			hasNumeric = true
		}
		result[i] = ch
	}

	// We need at least one number in the password or Oracle will reject it.
	if !hasNumeric {
		nRand, err := randInt(len(numbers))
		if err != nil {
			return "", err
		}
		iRand, err := randInt(passLength - 1)
		if err != nil {
			return "", err
		}
		result[iRand] = numbers[nRand]
	}

	cRand, err := randInt(len(chars))
	if err != nil {
		return "", err
	}

	// Construct a password that starts with a character.
	return string(chars[cRand]) + string(result), nil
}

// SetupUserPwConnStringByClient sets the password for the given user to
// a randomized password with the client and returns the connection string.
func SetupUserPwConnStringByClient(ctx context.Context, onClient runSQLOnClient, username, db, DBDomain string) (string, error) {
	passwd, err := RandOraclePassword()
	if err != nil {
		return "", err
	}
	applySQL := []string{fmt.Sprintf(alterUserSQL, username, passwd)}
	if _, err := onClient.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: applySQL, Suppress: true}); err != nil {
		return "", err
	}
	svc := db
	if DBDomain != "" {
		svc = fmt.Sprintf("%s.%s", db, DBDomain)
	}
	return connect.EZ(username, passwd, consts.Localhost, fmt.Sprint(consts.SecureListenerPort), svc, false), nil
}

// SetupUserPwConnStringOnServer sets the password for the given user to
// a randomized password on the DB server and returns the connection string.
func SetupUserPwConnStringOnServer(ctx context.Context, onServer runSQLOnServer, username, db, DBDomain string) (string, error) {
	passwd, err := RandOraclePassword()
	if err != nil {
		return "", err
	}
	applySQL := []string{fmt.Sprintf(alterUserSQL, username, passwd)}
	if _, err := onServer.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: applySQL, Suppress: true}); err != nil {
		return "", err
	}
	svc := db
	if DBDomain != "" {
		svc = fmt.Sprintf("%s.%s", db, DBDomain)
	}
	return connect.EZ(username, passwd, consts.Localhost, fmt.Sprint(consts.SecureListenerPort), svc, false), nil
}

// SetupConnStringOnServer generates and sets a random password for the given user
// on the DB server and returns
// the connection string without user/password part and the generated password.
func SetupConnStringOnServer(ctx context.Context, onServer runSQLOnServer, username, db, DBDomain string) (string, string, error) {
	passwd, err := RandOraclePassword()
	if err != nil {
		return "", "", err
	}
	applySQL := []string{fmt.Sprintf(alterUserSQL, username, passwd)}
	if _, err := onServer.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{Commands: applySQL, Suppress: true}); err != nil {
		return "", "", err
	}
	svc := db
	if DBDomain != "" {
		svc = fmt.Sprintf("%s.%s", db, DBDomain)
	}
	return connect.EZ("", "", consts.Localhost, fmt.Sprint(consts.SecureListenerPort), svc, false), passwd, nil
}

// SetupUserPwConnString sets the password for the given user to a randomized password and returns the connection string.
func (s *Security) SetupUserPwConnString(ctx context.Context, username, db, DBDomain string) (string, error) {
	return SetupUserPwConnStringByClient(ctx, s.dbdClient, username, db, DBDomain)
}

// waitForHealthyOracleDBContainer waits until the oracle_db container is healthy or until context times out.
func (s *Security) waitForHealthyOracleDBContainer(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			isHealthy, err := s.IsContainerHealthy(ctx, consts.OracleDBContainerName)
			if err != nil {
				klog.ErrorS(err, "unable to connect to container, will retry", "container", consts.OracleDBContainerName)
			} else if isHealthy {
				return nil
			}
		}
		time.Sleep(s.pollInterval)
	}
}

// IsContainerHealthy checks the health status of a named container.
func (s *Security) IsContainerHealthy(ctx context.Context, name string) (bool, error) {
	cs, err := s.dockerClient.ContainerInspect(ctx, name)
	if err != nil {
		return false, err
	}

	if cs.ContainerJSONBase != nil && cs.State != nil && cs.State.Health != nil {
		healthStatus := cs.State.Health.Status
		klog.InfoS("container health status", "container", cs.Name, "healthStatus", healthStatus)
		if healthStatus == types.Healthy {
			return true, nil
		}
	} else {
		klog.InfoS("container has no available health status", "container", name)
	}
	return false, nil
}

func randInt(maxInt int) (int64, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(maxInt)))
	if err != nil {
		return 0, err
	}
	return n.Int64(), nil
}
