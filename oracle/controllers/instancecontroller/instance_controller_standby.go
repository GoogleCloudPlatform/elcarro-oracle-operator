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

package instancecontroller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
)

func (r *InstanceReconciler) bootstrapStandby(ctx context.Context, inst *v1alpha1.Instance, log logr.Logger) error {
	req := &controllers.BootstrapStandbyRequest{
		CdbName:  inst.Spec.CDBName,
		Version:  inst.Spec.Version,
		Dbdomain: controllers.GetDBDomain(inst),
	}
	migratedPDBs, err := controllers.BootstrapStandby(ctx, r, r.DatabaseClientFactory, inst.GetNamespace(), inst.GetName(), *req)
	if err != nil {
		return fmt.Errorf("failed to bootstrap the standby instance: %v", err)
	}

	// Create missing resources for migrated database.
	for _, pdb := range migratedPDBs {
		var users []v1alpha1.UserSpec
		for _, u := range pdb.Users {
			var privs []v1alpha1.PrivilegeSpec
			for _, p := range u.Privs {
				privs = append(privs, v1alpha1.PrivilegeSpec(p))
			}
			users = append(users, v1alpha1.UserSpec{
				UserSpec: commonv1alpha1.UserSpec{
					Name: u.UserName,
				},
				Privileges: privs,
			})
		}
		database := &v1alpha1.Database{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: inst.GetNamespace(),
				Name:      pdb.PdbName,
			},
			Spec: v1alpha1.DatabaseSpec{
				DatabaseSpec: commonv1alpha1.DatabaseSpec{
					Name:     pdb.PdbName,
					Instance: inst.GetName(),
				},
				Users: users,
			},
		}
		if err := r.Client.Create(ctx, database); err != nil {
			return fmt.Errorf("failed to create database resource: %v", err)
		}
	}
	return nil
}
