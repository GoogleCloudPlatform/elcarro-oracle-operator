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
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/local"
)

var (
	// CallTimeout can be set via a flag by the program importing the library.
	CallTimeout = 15 * time.Minute
)

// withTimeout returns a context with a default timeout if the input context has no timeout.
func withTimeout(ctx context.Context, timeOut time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeOut)
}

// DatabaseDaemonDialLocalhost connects to a local Database Daemon via gRPC.
func DatabaseDaemonDialLocalhost(ctx context.Context, port int, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	ctxDial, cancel := withTimeout(ctx, CallTimeout)
	defer cancel()
	finalOpts := append([]grpc.DialOption{grpc.WithTransportCredentials(local.NewCredentials())}, opts...)
	return grpc.DialContext(ctxDial, fmt.Sprintf("localhost:%d", port), finalOpts...)
}

// DatabaseDaemonDialSocket connects to Database Daemon via gRPC.
func DatabaseDaemonDialSocket(ctx context.Context, socket string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	ctxDial, cancel := withTimeout(ctx, CallTimeout)
	defer cancel()
	endpoint := fmt.Sprintf("passthrough://unix/%s", socket)
	finalOpts := append([]grpc.DialOption{grpc.WithTransportCredentials(local.NewCredentials()), grpc.WithContextDialer(GrpcUnixDialer)}, opts...)
	return grpc.DialContext(ctxDial, endpoint, finalOpts...)
}

// DatabaseDaemonDialService connects to Database Service via gRPC.
func DatabaseDaemonDialService(ctx context.Context, serviceAndPort string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	ctxDial, cancel := withTimeout(ctx, CallTimeout)
	defer cancel()
	finalOpts := append([]grpc.DialOption{grpc.WithInsecure()}, opts...)
	return grpc.DialContext(ctxDial, serviceAndPort, finalOpts...)
}
