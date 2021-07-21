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

package provision

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/security"
)

// oracleCDB provides Oracle CDB information.
type oracleCDB struct {
	sourceCDBName            string
	cdbName                  string
	version                  string
	host                     string
	uniqueName               string
	DBDomain                 string
	databaseParamSGATargetMB uint64
	databaseParamPGATargetMB uint64
}

// newOracleCDB constructs a CDB information provider.
func newOracleCDB(ctx context.Context, sourceDBName, cdbName string, version, zone, host, DBDomain string, paramPGATargetMB, paramSGATargetMB uint64) *oracleCDB {
	uniqueName := fmt.Sprintf("%s_%s", cdbName, strings.Replace(zone, "-", "", -1))
	return &oracleCDB{
		sourceCDBName:            sourceDBName,
		cdbName:                  cdbName,
		version:                  version,
		uniqueName:               uniqueName,
		host:                     host,
		DBDomain:                 DBDomain,
		databaseParamSGATargetMB: paramSGATargetMB,
		databaseParamPGATargetMB: paramPGATargetMB,
	}
}

// GetVersion returns the version of the oracle DB.
func (db *oracleCDB) GetVersion() string {
	return db.version
}

// GetDataFilesDir returns data files directory location.
func (db *oracleCDB) GetDataFilesDir() string {
	return fmt.Sprintf(consts.DataDir, consts.DataMount, db.cdbName)
}

// GetSourceDataFilesDir returns data files directory location of the pre-built DB.
func (db *oracleCDB) GetSourceDataFilesDir() string {
	return filepath.Join(os.Getenv("ORACLE_BASE"), "oradata", db.GetSourceDatabaseName())
}

// GetConfigFilesDir returns config file directory location.
func (db *oracleCDB) GetConfigFilesDir() string {
	return fmt.Sprintf(consts.ConfigDir, consts.DataMount, db.cdbName)
}

// GetSourceConfigFilesDir returns config file directory location of the pre-built DB.
func (db *oracleCDB) GetSourceConfigFilesDir() string {
	return filepath.Join(db.GetDatabaseHome(), "dbs")
}

// GetAdumpDir returns adump directory location.
func (db *oracleCDB) GetAdumpDir() string {
	return filepath.Join(db.GetDatabaseBase(), "admin", db.GetDatabaseName(), "adump")
}

// GetCdumpDir returns cdump directory location.
func (db *oracleCDB) GetCdumpDir() string {
	return filepath.Join(db.GetDatabaseBase(), "admin", db.GetDatabaseName(), "cdump")
}

// GetFlashDir returns flash directory location.
func (db *oracleCDB) GetFlashDir() string {
	return fmt.Sprintf(consts.RecoveryAreaDir, consts.LogMount, db.cdbName)
}

// GetListenerDir returns listeners directory location.
func (db *oracleCDB) GetListenerDir() string {
	return fmt.Sprintf(consts.ListenerDir, consts.DataMount)
}

// GetDatabaseBase returns oracle base location.
func (db *oracleCDB) GetDatabaseBase() string {
	return consts.OracleBase
}

// GetDatabaseName returns database name.
func (db *oracleCDB) GetDatabaseName() string {
	return db.cdbName
}

// GetSourceDatabaseName returns database name of the pre-built DB.
func (db *oracleCDB) GetSourceDatabaseName() string {
	return db.sourceCDBName
}

// GetDatabaseHome returns database home location.
func (db *oracleCDB) GetDatabaseHome() string {
	return os.Getenv("ORACLE_HOME")
}

// GetDataFiles returns initial data files associated with the DB.
func (db *oracleCDB) GetDataFiles() []string {
	return []string{"system01.dbf", "sysaux01.dbf", "undotbs01.dbf", "users01.dbf", "pdbseed/undotbs01.dbf", "pdbseed/sysaux01.dbf", "pdbseed/system01.dbf"}
}

// GetSourceConfigFiles returns initial config files associated with pre-built DB.
func (db *oracleCDB) GetSourceConfigFiles() []string {
	return []string{fmt.Sprintf("spfile%s.ora", db.GetSourceDatabaseName()), fmt.Sprintf("orapw%s", db.GetSourceDatabaseName())}
}

// GetConfigFiles returns config files associated with current DB.
func (db *oracleCDB) GetConfigFiles() []string {
	return []string{fmt.Sprintf("spfile%s.ora", db.GetDatabaseName()), fmt.Sprintf("orapw%s", db.GetDatabaseName())}
}

// GetMountPointDatafiles returns the mount point of the data files.
func (db *oracleCDB) GetMountPointDatafiles() string {
	return fmt.Sprintf("/%s", consts.DataMount)
}

// GetMountPointAdmin returns the mount point of the admin directory.
func (db *oracleCDB) GetMountPointAdmin() string {
	return fmt.Sprintf("/%s", consts.DataMount)
}

// GetListeners returns listeners of the DB.
func (db *oracleCDB) GetListeners() map[string]*consts.Listener {
	return consts.ListenerNames
}

// GetDatabaseUniqueName returns database unique name.
func (db *oracleCDB) GetDatabaseUniqueName() string {
	return db.uniqueName
}

// GetDBDomain returns DB domain.
func (db *oracleCDB) GetDBDomain() string {
	return db.DBDomain
}

// GetMountPointDiag returns the mount point of the diag directory.
func (db *oracleCDB) GetMountPointDiag() string {
	return fmt.Sprintf("/%s", consts.DataMount)
}

// GetDatabaseParamPGATargetMB returns PGA value in MB.
func (db *oracleCDB) GetDatabaseParamPGATargetMB() uint64 {
	return db.databaseParamPGATargetMB
}

// GetDatabaseParamSGATargetMB returns SGA value in MB.
func (db *oracleCDB) GetDatabaseParamSGATargetMB() uint64 {
	return db.databaseParamSGATargetMB
}

// GetOratabFile returns oratab file location.
func (db *oracleCDB) GetOratabFile() string {
	return consts.OraTab
}

// GetSourceDatabaseHost returns host name of the pre-built DB.
func (db *oracleCDB) GetSourceDatabaseHost() string {
	return consts.SourceDatabaseHost
}

// GetHostName returns host name.
func (db *oracleCDB) GetHostName() string {
	return db.host
}

// GetCreateUserCmds returns create user commands to setup users.
func (db *oracleCDB) GetCreateUserCmds() []*createUser {
	var (
		sPwd, pPwd string
		err        error
	)
	if sPwd, err = security.RandOraclePassword(); err != nil {
		return nil
	}
	if pPwd, err = security.RandOraclePassword(); err != nil {
		return nil
	}

	return []*createUser{
		{
			user: consts.SecurityUser,
			cmds: []string{
				CreateUserCmd(consts.SecurityUser, sPwd),
				fmt.Sprintf("grant create session to %s container=all", consts.SecurityUser),
				fmt.Sprintf("grant create trigger to %s container=all", consts.SecurityUser),
				fmt.Sprintf("grant administer database trigger to %s container=all", consts.SecurityUser)},
		},
		{
			// This CDB user should have no permissions on CDB as it will import/export user provided DMPs
			user: consts.PDBLoaderUser,
			cmds: []string{
				CreateUserCmd(consts.PDBLoaderUser, pPwd)},
		},
	}
}

// IsCDB returns true if this is a CDB.
func (db *oracleCDB) IsCDB() bool {
	return true
}
