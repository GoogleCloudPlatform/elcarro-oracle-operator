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
	"errors"
	"fmt"
	"strings"

	"bitbucket.org/creachadair/stringset"
	"k8s.io/klog/v2"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/common/sql"
	dbdpb "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/oracle"
)

// users describe the managed Oracle PDB users.
type users struct {
	databaseName  string
	databaseRoles map[string]bool
	nameToUser    map[string]*user
	// envUserNames keeps track of the managed users.
	// The value will be initialized/refreshed with method users.readEnv
	envUserNames []string
}

// diff returns users, which should be created/updated/deleted by comparing k8s spec with real environment.
func (us *users) diff(ctx context.Context, client dbdpb.DatabaseDaemonClient) (toCreateUsers, toUpdateUsers, toDeleteUsers, toUpdatePwdUsers []*user, err error) {
	if err := us.readEnv(ctx, client); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to read the env users: %v", err)
	}
	var specUserNames []string
	for k := range us.nameToUser {
		specUserNames = append(specUserNames, k)
	}
	toCreate, toCheck, toDelete := compare(specUserNames, us.envUserNames)
	toCreateUsers, err = us.getUsers(toCreate)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	toCheckUsers, err := us.getUsers(toCheck)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for _, d := range toDelete {
		du := newNoSpecUser(us.databaseName, d)
		if err := du.readEnv(ctx, client); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("failed to read the env user %v: %v", du, err)
		}
		toDeleteUsers = append(toDeleteUsers, du)
	}
	for _, u := range toCheckUsers {
		toGrant, toRevoke, toUpdatePwd, err := u.diff(ctx, client)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("failed to read the env user %v: %v", u, err)
		}
		if len(toGrant) != 0 || len(toRevoke) != 0 {
			toUpdateUsers = append(toUpdateUsers, u)
		}
		if toUpdatePwd {
			toUpdatePwdUsers = append(toUpdatePwdUsers, u)
		}
	}

	return toCreateUsers, toUpdateUsers, toDeleteUsers, toUpdatePwdUsers, nil
}

func (us *users) readEnv(ctx context.Context, client dbdpb.DatabaseDaemonClient) error {
	envUserNames, err := queryDB(
		ctx,
		client,
		us.databaseName,
		"select username from dba_users where ORACLE_MAINTAINED='N' and INHERITED='NO'",
		"USERNAME",
		func(userName string) bool {
			return userName != pdbAdmin
		},
	)
	if err != nil {
		return fmt.Errorf("failed to load users from DB %v", err)
	}
	us.envUserNames = envUserNames
	roles, err := queryDB(
		ctx,
		client,
		us.databaseName,
		"select role from dba_roles",
		"ROLE",
		func(roleName string) bool {
			return true
		},
	)
	if err != nil {
		return fmt.Errorf("failed to load roles from DB %v", err)
	}
	us.databaseRoles = make(map[string]bool)
	for _, role := range roles {
		us.databaseRoles[role] = true
	}
	return nil
}

func (us *users) getUsers(names []string) ([]*user, error) {
	var res []*user
	for _, name := range names {
		u, ok := us.nameToUser[name]
		if !ok {
			return nil, fmt.Errorf("failed to find %s in %v", name, us.nameToUser)
		}
		res = append(res, u)
	}
	return res, nil
}

func newUsers(databaseName string, userSpecs []*User) *users {
	nameToUser := make(map[string]*user)
	for _, us := range userSpecs {
		nameToUser[strings.ToUpper(us.Name)] = newUser(databaseName, us)
	}

	return &users{
		databaseName: strings.ToUpper(databaseName),
		nameToUser:   nameToUser,
	}
}

// user describe a managed Oracle PDB user.
type user struct {
	databaseName string
	userName     string
	specPrivs    []string
	// envDbaSysPrivs keeps track of the privileges granted to the user (dba_sys_privs table).
	// The value will be initialized/refreshed with method user.readEnv
	envDbaSysPrivs []string
	// envDbaRolePrivs keeps track of the roles granted to the user (dba_role_privs table).
	// The value will be initialized/refreshed with method user.readEnv
	envDbaRolePrivs []string
	// gsmSecNewVer is new GSM secret version from the spec.
	gsmSecNewVer string
	// gsmSecCurVer is the current GSM secret version.
	gsmSecCurVer string
	// newPassword is used by both gsm and plaintext;
	// can be overwritten later if GSM is enabled.
	newPassword string
	// curPassword is only used for plaintext status diff.
	curPassword string
}

func (u *user) readEnv(ctx context.Context, client dbdpb.DatabaseDaemonClient) error {
	sysPrivs, err := queryDB(
		ctx,
		client,
		u.databaseName,
		fmt.Sprintf("select privilege from dba_sys_privs where grantee='%s'", sql.StringParam(u.userName)),
		"PRIVILEGE",
		func(string) bool {
			return true
		},
	)
	if err != nil {
		return fmt.Errorf("failed to query dba sys privileges: %v", err)
	}
	u.envDbaSysPrivs = sysPrivs
	rolePrivs, err := queryDB(
		ctx,
		client,
		u.databaseName,
		fmt.Sprintf("select granted_role from dba_role_privs where grantee='%s'", sql.StringParam(u.userName)),
		"GRANTED_ROLE",
		func(string) bool {
			return true
		},
	)
	if err != nil {
		return fmt.Errorf("failed to query dba role privileges: %v", err)
	}
	u.envDbaRolePrivs = rolePrivs
	return nil
}

// diff returns privileges, which should be granted/revoked by comparing k8s spec with real environment.
func (u *user) diff(ctx context.Context, client dbdpb.DatabaseDaemonClient) (toGrant, toRevoke []string, toUpdatePwd bool, err error) {
	if err := u.readEnv(ctx, client); err != nil {
		return nil, nil, false, fmt.Errorf("failed to read the env user: %v", err)
	}
	var envPrivs []string
	envPrivs = append(envPrivs, u.envDbaSysPrivs...)
	envPrivs = append(envPrivs, u.envDbaRolePrivs...)
	toGrant, _, toRevoke = compare(u.specPrivs, envPrivs)
	// Always update password if the request version is not equal to the current version
	// or the request version is latest (if the latest password equals to the current one,
	// the SQL underlying won't report error as expected).
	toUpdateGsmPwd := (u.gsmSecNewVer != "" && !strings.EqualFold(u.gsmSecNewVer, u.gsmSecCurVer)) || strings.HasSuffix(u.gsmSecNewVer, "latest")
	if toUpdateGsmPwd {
		var gsmPwd string
		gsmPwd, err = AccessSecretVersionFunc(ctx, u.gsmSecNewVer)
		if err != nil {
			return nil, nil, false, fmt.Errorf("failed to read GSM secret: %v", err)
		}
		u.newPassword = gsmPwd
	}
	toUpdatePlaintextPwd := u.curPassword != "" && u.curPassword != u.newPassword
	return toGrant, toRevoke, toUpdateGsmPwd || toUpdatePlaintextPwd, nil
}

func (u *user) create(ctx context.Context, client dbdpb.DatabaseDaemonClient) error {
	var grantCmds []string
	for _, p := range u.specPrivs {
		grantCmds = append(grantCmds, sql.QueryGrantPrivileges(p, u.userName))
	}
	sqls := append(
		[]string{
			sql.QuerySetSessionContainer(u.databaseName),
			sql.QueryCreateUser(u.userName, u.newPassword),
		},
		grantCmds...,
	)
	if _, err := client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: sqls,
	}); err != nil {
		return fmt.Errorf("failed to create user %v: %v", u, err)
	}
	return nil
}

func (u *user) update(ctx context.Context, client dbdpb.DatabaseDaemonClient, roles map[string]bool) error {
	if err := u.updateRolePrivs(ctx, client, roles); err != nil {
		return err
	}
	if err := u.updateSysPrivs(ctx, client, roles); err != nil {
		return err
	}
	return nil

}

func (u *user) updateUserPassword(ctx context.Context, client dbdpb.DatabaseDaemonClient) error {
	_, _, toUpdate, err := u.diff(ctx, client)
	if err != nil {
		return fmt.Errorf("failed to get diff to update user %v: %v", u, err)
	}
	if !toUpdate {
		return nil
	}
	if err := u.updatePassword(ctx, client); err != nil {
		return fmt.Errorf("failed to alter user %s: %v", u.userName, err)
	}
	return nil
}

func (u *user) updateRolePrivs(ctx context.Context, client dbdpb.DatabaseDaemonClient, roles map[string]bool) error {
	toGrant, toRevoke, _, err := u.diff(ctx, client)
	if err != nil {
		return fmt.Errorf("failed to get diff to update user %v: %v", u, err)
	}
	var toGrantRoles, toRevokeRoles []string
	for _, g := range toGrant {
		if roles[g] {
			toGrantRoles = append(toGrantRoles, g)
		}
	}

	for _, r := range toRevoke {
		if roles[r] {
			toRevokeRoles = append(toRevokeRoles, r)
		}
	}

	if err := u.grant(ctx, client, toGrantRoles); err != nil {
		return fmt.Errorf("failed to grant roles %v to user %s: %v", toGrantRoles, u.userName, err)
	}
	if err := u.revoke(ctx, client, toRevokeRoles); err != nil {
		return fmt.Errorf("failed to revoke roles %v from user %s: %v", toRevokeRoles, u.userName, err)
	}
	return nil
}

func (u *user) updateSysPrivs(ctx context.Context, client dbdpb.DatabaseDaemonClient, roles map[string]bool) error {
	toGrant, toRevoke, _, err := u.diff(ctx, client)
	if err != nil {
		return fmt.Errorf("failed to get diff to update user %v: %v", u, err)
	}

	var toGrantPrivs, toRevokePrivs []string
	for _, g := range toGrant {
		if !roles[g] {
			toGrantPrivs = append(toGrantPrivs, g)
		}
	}

	for _, r := range toRevoke {
		if !roles[r] {
			toRevokePrivs = append(toRevokePrivs, r)
		}
	}

	if err := u.grant(ctx, client, toGrantPrivs); err != nil {
		return fmt.Errorf("failed to grant privs %v to user %s: %v", toGrantPrivs, u.userName, err)
	}
	if err := u.revoke(ctx, client, toRevokePrivs); err != nil {
		return fmt.Errorf("failed to revoke privs %v from user %s: %v", toRevokePrivs, u.userName, err)
	}
	return nil
}

func (u *user) updatePassword(ctx context.Context, client dbdpb.DatabaseDaemonClient) error {
	alterUserCmds := []string{sql.QueryAlterUser(u.userName, u.newPassword)}
	sqls := append([]string{sql.QuerySetSessionContainer(u.databaseName)}, alterUserCmds...)
	if _, err := client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: sqls,
		Suppress: true,
	}); err != nil {
		return fmt.Errorf("failed to alter user %s: %v", u.userName, err)
	}
	return nil
}

func (u *user) grant(ctx context.Context, client dbdpb.DatabaseDaemonClient, toGrant []string) error {
	if len(toGrant) == 0 {
		return nil
	}
	var grantCmds []string
	for _, p := range toGrant {
		grantCmds = append(grantCmds, sql.QueryGrantPrivileges(p, u.userName))
	}
	sqls := append([]string{sql.QuerySetSessionContainer(u.databaseName)}, grantCmds...)
	if _, err := client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: sqls,
	}); err != nil {
		return fmt.Errorf("failed to grant %v to user %s: %v", toGrant, u.userName, err)
	}
	return nil
}

func (u *user) revoke(ctx context.Context, client dbdpb.DatabaseDaemonClient, toRevoke []string) error {
	if len(toRevoke) == 0 {
		return nil
	}
	var revokeCmds []string
	for _, p := range toRevoke {
		revokeCmds = append(revokeCmds, sql.QueryRevokePrivileges(p, u.userName))
	}
	sqls := append([]string{sql.QuerySetSessionContainer(u.databaseName)}, revokeCmds...)
	if _, err := client.RunSQLPlus(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: sqls,
	}); err != nil {
		return fmt.Errorf("failed to revoke %v from user %s: %v", toRevoke, u.userName, err)
	}
	return nil
}

func (u *user) delete() (suppressedSQLs string) {
	return sql.QuerySetSessionContainer(u.databaseName) + fmt.Sprintf("; DROP USER %s CASCADE;", sql.MustBeObjectName(u.userName))
}

func (u *user) String() string {
	return fmt.Sprintf("{database: %q, name: %q, specPrivs: %v, envSysPrivs: %v, envRolePrivs %v}", u.databaseName, u.userName, u.specPrivs, u.envDbaSysPrivs, u.envDbaRolePrivs)
}

func (u *user) GetUserName() string {
	return u.userName
}

func (u *user) GetUserEnvPrivs() []string {
	var privs []string
	privs = append(privs, u.envDbaRolePrivs...)
	privs = append(privs, u.envDbaSysPrivs...)
	return privs
}

func newUser(databaseName string, specUser *User) *user {
	var privs []string
	for _, p := range specUser.Privileges {
		upperP := strings.ToUpper(p)
		// example: GRANT SELECT ON TABLE t TO SCOTT
		if strings.Contains(upperP, " ON ") {
			klog.ErrorS(errors.New("object privileges not supported, will be omitted by operator"), "not supported privileges", "priv", p)
		} else {
			privs = append(privs, upperP)
		}
	}
	var lastVersion string
	if specUser.PasswordGsmSecretRef != nil {
		lastVersion = specUser.PasswordGsmSecretRef.Version
	} else {
		lastVersion = ""
	}
	user := &user{
		databaseName: strings.ToUpper(databaseName),
		userName:     strings.ToUpper(specUser.Name),
		// Used by both gsm and plaintext
		// can be overwritten later if GSM is enabled.
		newPassword: specUser.Password,
		// Only used for plaintext status diff.
		curPassword: specUser.LastPassword,
		specPrivs:   privs,
		// Empty version is returned if PasswordGsmSecretRef is nil.
		gsmSecCurVer: lastVersion,
	}
	if specUser.PasswordGsmSecretRef != nil {
		user.gsmSecNewVer = fmt.Sprintf(gsmSecretStr, specUser.PasswordGsmSecretRef.ProjectId, specUser.PasswordGsmSecretRef.SecretId, specUser.PasswordGsmSecretRef.Version)
	}
	return user
}

func newNoSpecUser(databaseName, userName string) *user {
	return &user{
		databaseName: strings.ToUpper(databaseName),
		userName:     strings.ToUpper(userName),
	}
}

func queryDB(ctx context.Context, client dbdpb.DatabaseDaemonClient, databaseName, sqlQuery, key string, filter func(val string) bool) ([]string, error) {
	resp, err := client.RunSQLPlusFormatted(ctx, &dbdpb.RunSQLPlusCMDRequest{
		Commands: []string{
			sql.QuerySetSessionContainer(databaseName),
			sqlQuery,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("queryDB failed to query data: %v", err)
	}
	rows, err := parseSQLResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("queryDB failed to parse data from %v: %v", resp, err)
	}
	userNames, err := queryRowsByKey(rows, key, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve %v from %v", key, rows)
	}
	return userNames, nil
}

func queryRowsByKey(rows []map[string]string, rowKey string, filter func(val string) bool) ([]string, error) {
	var res []string
	for _, row := range rows {
		v, ok := row[rowKey]
		if !ok {
			return nil, fmt.Errorf("failed to retrieve %v from %v", rowKey, row)
		}
		if filter(v) {
			res = append(res, v)
		}
	}
	return res, nil
}

// compare returns set difference left\right intersection right\left
func compare(left, right []string) (leftMinusRight, intersection, rightMinusLeft []string) {
	leftSet := stringset.New(left...)
	rightSet := stringset.New(right...)
	return leftSet.Diff(rightSet).Elements(), leftSet.Intersect(rightSet).Elements(), rightSet.Diff(leftSet).Elements()
}
