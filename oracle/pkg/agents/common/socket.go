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

package common

import (
	"context"
	"fmt"
	"net"
	"time"

	"k8s.io/klog/v2"
)

var (
	// MaxAttempts can be set via a flag by the program importing the library.
	MaxAttempts = 5
	// MaxDelay can be set via a flag by the program importing the library.
	MaxDelay = 30 * time.Second
)

// UnixDialer opens a socket connection.
func UnixDialer(ctx context.Context, addr string) (net.Conn, error) {
	var err error
	var conn net.Conn
	var d net.Dialer
	var i int

	for i = 1; i <= MaxAttempts; i++ {
		conn, err = d.DialContext(ctx, "unix", addr)
		if err == nil {
			return conn, nil
		}

		// UnixDialer is usually called by other functions then this error will be swallowed, adding log.
		klog.InfoS("Unix dialer failed", "addr", addr, "err", err)
		select {
		case <-time.After(MaxDelay):
			continue
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	klog.ErrorS(fmt.Errorf("failed to connect"), "failed to connect", "numAttempts", i)
	return nil, err
}

// GrpcUnixDialer opens up a unix socket connection compatible with gRPC dialers.
func GrpcUnixDialer(ctx context.Context, addr string) (net.Conn, error) {
	return UnixDialer(ctx, addr)
}
