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

package databasecontroller

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/integer"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common/sql"
	k8s "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

const (
	gsmResourceVersionString = "projects/%s/secrets/%s/versions/%s"
	pdbAdminUserName         = "GPDB_ADMIN"
)

var (
	dialTimeout = 3 * time.Minute
)

// NewDatabase attempts to create a new PDB if it doesn't exist yet.
// The first return value of NewDatabase is "bail out or not?".
// If a PDB is new, just created now, NewDatabase returns bail=false.
// If it's an existing PDB, NewDatabase returns bail=true (so that the rest
// of the workflow, e.g. creating users step, is not attempted).
func NewDatabase(ctx context.Context, r *DatabaseReconciler, db *v1alpha1.Database, dbDomain, cdbName string, log logr.Logger) (bool, error) {
	r.Recorder.Eventf(db, corev1.EventTypeNormal, k8s.CreatingDatabase, fmt.Sprintf("Creating new database %q", db.Spec.Name))

	// Establish a connection to a Config Agent.
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	req := &controllers.CreateDatabaseRequest{
		Name:     db.Spec.Name,
		CdbName:  cdbName,
		DbDomain: dbDomain,
	}
	userVerStr := ""
	// database_controller.validateSpec has validated the spec earlier;
	// So no duplicated validation here.
	if db.Spec.AdminPassword != "" {
		userVerStr = db.Spec.AdminPassword
		req.Password = db.Spec.AdminPassword
		if lastPwd, ok := db.Status.UserResourceVersions[pdbAdminUserName]; ok {
			req.LastPassword = lastPwd
		}
	}
	if db.Spec.AdminPasswordGsmSecretRef != nil {
		userVerStr = fmt.Sprintf(gsmResourceVersionString, db.Spec.AdminPasswordGsmSecretRef.ProjectId, db.Spec.AdminPasswordGsmSecretRef.SecretId, db.Spec.AdminPasswordGsmSecretRef.Version)
		ref := &controllers.GsmSecretReference{
			ProjectId: db.Spec.AdminPasswordGsmSecretRef.ProjectId,
			SecretId:  db.Spec.AdminPasswordGsmSecretRef.SecretId,
			Version:   db.Spec.AdminPasswordGsmSecretRef.Version,
		}
		if lastVer, ok := db.Status.UserResourceVersions[pdbAdminUserName]; ok {
			ref.LastVersion = lastVer
		}
		req.AdminPasswordGsmSecretRef = ref
	}
	cdOut, err := controllers.CreateDatabase(ctx, r, r.DatabaseClientFactory, db.Namespace, db.Spec.Instance, *req)
	if err != nil {
		return false, fmt.Errorf("resource/NewDatabase: failed on CreateDatabase gRPC call: %v", err)
	}
	log.Info("resource/NewDatabase: CreateDatabase DONE with this output", "out", cdOut)

	// "AdminUserSyncCompleted" status indicates PDB existed
	// and admin user sync completed.
	if cdOut == "AdminUserSyncCompleted" {
		r.Recorder.Eventf(db, corev1.EventTypeWarning, k8s.DatabaseAlreadyExists, fmt.Sprintf("Database %q already exists, sync admin user performed", db.Spec.Name))
		// Update user version status map after newly synced database admin user.
		// The caller will update the status by r.Status().Update.
		if db.Status.UserResourceVersions == nil {
			db.Status.UserResourceVersions = make(map[string]string)
		}
		db.Status.UserResourceVersions[pdbAdminUserName] = userVerStr
		// Return true indicating PDB already existed and return
		// PDB admin userVerMap which need to by synced by caller.
		// The caller will trigger syncUser instead of createUser later.
		return true, nil
	}

	// Indicated underlying database exists and admin user is in sync with the config.
	if cdOut == "AlreadyExists" {
		r.Recorder.Eventf(db, corev1.EventTypeWarning, k8s.DatabaseAlreadyExists, fmt.Sprintf("Database %q already exists", db.Spec.Name))
		return true, nil
	}

	hostname, err := os.Hostname()
	if err != nil {
		log.Error(err, "resources/NewDatabase: failed to get a hostname")
	}

	log.V(1).Info("resources/NewDatabase: new database requested: DONE", "hostname", hostname)
	// Update user version status map after newly created database.
	// The caller will update the status by r.Status().Update.
	if db.Status.UserResourceVersions == nil {
		db.Status.UserResourceVersions = make(map[string]string)
	}
	db.Status.UserResourceVersions[pdbAdminUserName] = userVerStr
	return false, nil
}

// NewUsers attempts to create a new user.
func NewUsers(ctx context.Context, r *DatabaseReconciler, db *v1alpha1.Database, dbDomain, cdbName string, log logr.Logger) error {
	log.Info("resources/NewUsers: new database users requested", "dbName", db.Spec.Name, "requestedUsers", db.Spec.Users)
	var usernames, usersCmds, grantsCmds []string
	var userSpecs []*controllers.User
	userVerMap := make(map[string]string)
	// Copy pdb admin user version into local map to sync later.
	if v, ok := db.Status.UserResourceVersions[pdbAdminUserName]; ok {
		userVerMap[pdbAdminUserName] = v
	}
	for k, u := range db.Spec.Users {
		log.Info("create user", "user#", k, "username", u.Name)
		if len(usernames) < 3 {
			usernames = append(usernames, u.Name)
		} else if len(usernames) == 3 {
			usernames = append(usernames, "...")
		}
		// database_controller.validateSpec has validated the spec earlier;
		// So no duplicated validation here.
		if u.Password != "" {
			usersCmds = append(usersCmds, sql.QueryCreateUser(u.Name, u.Password))
			userVerMap[u.Name] = u.Password
		}
		if u.GsmSecretRef != nil {
			userSpecs = append(userSpecs, &controllers.User{
				Name: u.Name,
				PasswordGsmSecretRef: &controllers.GsmSecretReference{
					ProjectId: u.GsmSecretRef.ProjectId,
					SecretId:  u.GsmSecretRef.SecretId,
					Version:   u.GsmSecretRef.Version,
				}})
			userVerMap[u.Name] = fmt.Sprintf(gsmResourceVersionString, u.GsmSecretRef.ProjectId, u.GsmSecretRef.SecretId, u.GsmSecretRef.Version)
		}

		for _, p := range u.Privileges {
			grantsCmds = append(grantsCmds, sql.QueryGrantPrivileges(string(p), u.Name))
		}
	}

	r.Recorder.Eventf(db, corev1.EventTypeNormal, k8s.CreatingUser, "Creating new users %v", usernames)

	// Establish a connection to a Config Agent.
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	req := &controllers.CreateUsersRequest{
		CdbName:       cdbName,
		PdbName:       db.Spec.Name,
		GrantPrivsCmd: grantsCmds,
		DbDomain:      dbDomain,
	}
	if usersCmds != nil {
		req.CreateUsersCmd = usersCmds
	}
	if userSpecs != nil {
		req.User = userSpecs
	}
	cdOut, err := controllers.CreateUsers(ctx, r, r.DatabaseClientFactory, db.Namespace, db.Spec.Instance, *req)
	if err != nil {
		log.Error(err, "resources/NewUsers: failed on CreateUsers gRPC call")
	}
	log.Info("resources/NewUsers: CreateUsers succeeded with this output", "output", cdOut)

	hostname, err := os.Hostname()
	if err != nil {
		log.Error(err, "resources/NewUsers: failed to get a hostname")
	}
	log.V(1).Info("resources/NewUsers: new database users requested: DONE", "hostname", hostname)
	r.Recorder.Eventf(db, corev1.EventTypeNormal, k8s.CreatedUser, "Created new users %v", usernames)

	db.Status.Conditions = k8s.Upsert(db.Status.Conditions, k8s.UserReady, v1.ConditionTrue, k8s.CreateComplete, "")
	db.Status.UserNames = usernames
	db.Status.UserResourceVersions = userVerMap
	r.updateIsChangeApplied(ctx, db)
	if err := r.Status().Update(ctx, db); err != nil {
		return err
	}
	return nil
}

// SyncUsers attempts to update PDB users.
func SyncUsers(ctx context.Context, r *DatabaseReconciler, db *v1alpha1.Database, cdbName string, log logr.Logger) error {
	log.Info("resources/syncUsers: sync database users requested", "db", db)
	r.Recorder.Eventf(db, corev1.EventTypeNormal, k8s.SyncingUser, fmt.Sprintf("Syncing users for database %q", db.Spec.Name))

	var userSpecs []*controllers.User
	var usernames []string
	userVerMap := make(map[string]string)
	// Copy pdb admin user version into local map to sync later.
	if v, ok := db.Status.UserResourceVersions[pdbAdminUserName]; ok {
		userVerMap[pdbAdminUserName] = v
	}
	for _, user := range db.Spec.Users {
		var privs []string
		usernames = append(usernames, user.Name)
		for _, specPriv := range user.Privileges {
			privs = append(privs, string(specPriv))
		}
		userSpec := &controllers.User{
			Name:       user.Name,
			Privileges: privs,
		}
		// database_controller.validateSpec has validated the spec earlier;
		// So no duplicated validation here.
		if user.Password != "" {
			userVerMap[user.Name] = user.Password
			userSpec.Password = user.Password
			lastPwd, ok := db.Status.UserResourceVersions[user.Name]
			if ok {
				userSpec.LastPassword = lastPwd
			}
		}
		if user.GsmSecretRef != nil {
			userVerMap[user.Name] = fmt.Sprintf(gsmResourceVersionString, user.GsmSecretRef.ProjectId, user.GsmSecretRef.SecretId, user.GsmSecretRef.Version)
			ref := &controllers.GsmSecretReference{
				ProjectId: user.GsmSecretRef.ProjectId,
				SecretId:  user.GsmSecretRef.SecretId,
				Version:   user.GsmSecretRef.Version,
			}
			if lastVer, ok := db.Status.UserResourceVersions[user.Name]; ok {
				ref.LastVersion = lastVer
			}
			userSpec.PasswordGsmSecretRef = ref
		}
		userSpecs = append(userSpecs, userSpec)
	}
	req := &controllers.UsersChangedRequest{
		PdbName:   db.Spec.Name,
		UserSpecs: userSpecs,
	}
	resp, err := controllers.UsersChanged(ctx, r, r.DatabaseClientFactory, db.GetNamespace(), db.Spec.Instance, *req)
	if err != nil {
		log.Error(err, "resources/syncUsers: failed on UsersChanged gRPC call")
		return err
	}

	if resp.Changed {
		db.Status.Phase = commonv1alpha1.DatabaseUpdating
		db.Status.Conditions = k8s.Upsert(db.Status.Conditions, k8s.UserReady, v1.ConditionFalse, k8s.SyncInProgress, "")
		if err := r.Status().Update(ctx, db); err != nil {
			return err
		}
		log.Info("resources/syncUsers: update database users requested", "CDB", cdbName, "PDB", db.Spec.Name)
		req := &controllers.UpdateUsersRequest{
			PdbName:   db.Spec.Name,
			UserSpecs: userSpecs,
		}
		if err := controllers.UpdateUsers(ctx, r, r.DatabaseClientFactory, db.GetNamespace(), db.Spec.Instance, *req); err != nil {
			log.Error(err, "resources/syncUsers: failed on UpdateUser gRPC call")
			return err
		}
		log.Info("resources/syncUsers: update database users done", "CDB", cdbName, "PDB", db.Spec.Name)
	}
	log.Info("resources/syncUsers: sync database users done", "CDB", cdbName, "PDB", db.Spec.Name)

	userReady := &v1.Condition{
		Type:    k8s.UserReady,
		Status:  v1.ConditionTrue,
		Reason:  k8s.SyncComplete,
		Message: "",
	}

	if len(resp.Suppressed) != 0 {
		userReady.Status = v1.ConditionFalse
		userReady.Reason = k8s.UserOutOfSync
		var msg []string
		for _, u := range resp.Suppressed {
			if u.SuppressType == controllers.UsersChangedResponse_DELETE {
				msg = append(msg, fmt.Sprintf("User %q not defined in database spec, "+
					"supposed to be deleted. suppressed SQL %q. Fix by deleting the user in DB or updating DB spec to include the user", u.UserName, u.Sql))
			} else if u.SuppressType == controllers.UsersChangedResponse_CREATE {
				msg = append(msg, fmt.Sprintf("User %q cannot be created, "+
					"password is not provided. Fix by creating the user in DB or updating DB spec to include password", u.UserName))
			}
		}
		userReady.Message = strings.Join(msg, ".")
	}

	if k8s.ConditionStatusEquals(userReady, v1.ConditionTrue) {
		r.Recorder.Eventf(db, corev1.EventTypeNormal, k8s.SyncedUser, fmt.Sprintf("Synced users for database %q", db.Spec.Name))
	} else {
		r.Recorder.Eventf(db, corev1.EventTypeWarning, k8s.FailedToSyncUser, fmt.Sprintf("Failed to sync users for database %q, %s", db.Spec.Name, userReady.Message))
	}

	db.Status.Conditions = k8s.Upsert(db.Status.Conditions, userReady.Type, userReady.Status, userReady.Reason, userReady.Message)
	db.Status.UserResourceVersions = userVerMap
	db.Status.UserNames = usernames[0:integer.IntMin(3, len(usernames))]
	if len(usernames) > 3 {
		db.Status.UserNames = append(db.Status.UserNames, "...")
	}
	r.updateIsChangeApplied(ctx, db)
	if err := r.Status().Update(ctx, db); err != nil {
		return err
	}
	return nil
}
