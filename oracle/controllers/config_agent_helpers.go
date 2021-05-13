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

	lropb "google.golang.org/genproto/googleapis/longrunning"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetLROOperation returns LRO operation for the specified namespace instance and operation id.
func GetLROOperation(caClientFactory ConfigAgentClientFactory, ctx context.Context, r client.Reader, namespace, id, instName string) (*lropb.Operation, error) {
	caClient, closeConn, err := caClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return nil, err
	}
	defer closeConn()

	return caClient.GetOperation(ctx, &lropb.GetOperationRequest{Name: id})
}

// DeleteLROOperation deletes LRO operation for the specified namespace instance and operation id.
func DeleteLROOperation(caClientFactory ConfigAgentClientFactory, ctx context.Context, r client.Reader, namespace, id, instName string) error {
	caClient, closeConn, err := caClientFactory.New(ctx, r, namespace, instName)
	if err != nil {
		return err
	}
	defer closeConn()

	_, err = caClient.DeleteOperation(ctx, &lropb.DeleteOperationRequest{Name: id})
	return err
}
