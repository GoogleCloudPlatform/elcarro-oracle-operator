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
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"k8s.io/klog/v2"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
)

var (
	// ListenerTemplateName is the filepath for the listener file template in the container.
	ListenerTemplateName = filepath.Join(consts.ScriptDir, "bootstrap-database-listener.template")

	// TnsnamesTemplateName is the filepath for the tnsnames file template in the container.
	TnsnamesTemplateName = filepath.Join(consts.ScriptDir, "bootstrap-database-tnsnames.template")

	// ControlFileTemplateName is the filepath for the control file template in the container.
	ControlFileTemplateName = filepath.Join(consts.ScriptDir, "bootstrap-database-crcf.template")

	// InitOraTemplateName is the filepath for the initOra file template in the container for Oracle EE/SE.
	InitOraTemplateName = filepath.Join(consts.ScriptDir, "bootstrap-database-initfile.template")

	// InitOraXeTemplateName is the filepath for the initOra file template in the container for Oracle 18c XE.
	InitOraXeTemplateName = filepath.Join(consts.ScriptDir, "bootstrap-database-initfile-oracle-xe.template")

	fileSQLNet = "sqlnet.ora"
	// SQLNetSrc is the filepath for the control file template in the container.
	SQLNetSrc = filepath.Join(consts.ScriptDir, fileSQLNet)

	// MetaDataFile is the filepath of the Database image metadata(Oracle Home,
	// CDB name, Version) file in the image.
	MetaDataFile = "/home/oracle/.metadata"
)

// ListenerInput is the struct, which will be applied to the listener template.
type ListenerInput struct {
	PluggableDatabaseNames []string
	DatabaseName           string
	DatabaseBase           string
	DatabaseHome           string
	ListenerName           string
	ListenerPort           string
	ListenerProtocol       string
	DatabaseHost           string
	DBDomain               string
}

type controlfileInput struct {
	DatabaseName       string
	DataFilesDir       string
	DataFilesMultiLine string
}

// oracleDB defines APIs for the DB information provider.
// Information provider need implement this interface to support oracle DB task.
type oracleDB interface {
	// GetVersion returns the version of the oracle DB.
	GetVersion() string
	// GetDataFilesDir returns data files directory location.
	GetDataFilesDir() string
	// GetSourceDataFilesDir returns data files directory location of the pre-built DB.
	GetSourceDataFilesDir() string
	// GetConfigFilesDir returns config file directory location.
	GetConfigFilesDir() string
	// GetSourceConfigFilesDir returns config file directory location of the pre-built DB.
	GetSourceConfigFilesDir() string
	// GetAdumpDir returns adump directory location.
	GetAdumpDir() string
	// GetCdumpDir returns cdump directory location.
	GetCdumpDir() string
	// GetFlashDir returns flash directory location.
	GetFlashDir() string
	// GetListenerDir returns listeners directory location.
	GetListenerDir() string
	// GetDatabaseBase returns oracle base location.
	GetDatabaseBase() string
	// GetDatabaseName returns database name.
	GetDatabaseName() string
	// GetSourceDatabaseName returns database name of the pre-built DB.
	GetSourceDatabaseName() string
	// GetDatabaseHome returns database home location.
	GetDatabaseHome() string
	// GetDataFiles returns initial data files associated with the DB.
	GetDataFiles() []string
	// GetSourceConfigFiles returns initial config files associated with pre-built DB.
	GetSourceConfigFiles() []string
	// GetConfigFiles returns config files associated with current DB.
	GetConfigFiles() []string
	// GetMountPointDatafiles returns the mount point of the data files.
	GetMountPointDatafiles() string
	// GetMountPointAdmin returns the mount point of the admin directory.
	GetMountPointAdmin() string
	// GetListeners returns listeners of the DB.
	GetListeners() map[string]*consts.Listener
	// GetDatabaseUniqueName returns database unique name.
	GetDatabaseUniqueName() string
	// GetDBDomain returns DB domain.
	GetDBDomain() string
	// GetMountPointDiag returns the mount point of the diag directory.
	GetMountPointDiag() string
	// GetDatabaseParamPGATargetMB returns PGA value in MB.
	GetDatabaseParamPGATargetMB() uint64
	// GetDatabaseParamSGATargetMB returns SGA value in MB.
	GetDatabaseParamSGATargetMB() uint64
	// GetOratabFile returns oratab file location.
	GetOratabFile() string
	// GetSourceDatabaseHost returns host name of the pre-built DB.
	GetSourceDatabaseHost() string
	// GetHostName returns host name.
	GetHostName() string
	// GetCreateUserCmds returns create user commands to setup users.
	GetCreateUserCmds() []*createUser
	// IsCDB returns true if this is a cdb.
	IsCDB() bool
}

// createUser provides user name and sql commands to create the user in this DB.
type createUser struct {
	user string
	cmds []string
}

// osUtil was added for unit test.
type osUtil interface {
	Lookup(username string) (*user.User, error)
	LookupGroup(name string) (*user.Group, error)
}

// OSUtilImpl contains utility methods for fetching user/group metadata.
type OSUtilImpl struct{}

// Lookup method obtains the user's metadata (uid, gid, username, name, homedir).
func (*OSUtilImpl) Lookup(username string) (*user.User, error) {
	return user.Lookup(username)
}

// LookupGroup method obtains the group's metadata (gid, name).
func (*OSUtilImpl) LookupGroup(name string) (*user.Group, error) {
	return user.LookupGroup(name)
}

type task interface {
	GetName() string
	Call(ctx context.Context) error
}

// simpleTask is a task which should be testable. Task should bring the system
// to a state which can be verified.
type simpleTask struct {
	name    string
	callFun func(ctx context.Context) error
}

func (task *simpleTask) GetName() string {
	return task.name
}

func (task *simpleTask) Call(ctx context.Context) error {
	return task.callFun(ctx)
}

func doSubTasks(ctx context.Context, parentTaskName string, subTasks []task) error {
	klog.InfoS("parent task: running", "task", parentTaskName)
	for _, sub := range subTasks {
		klog.InfoS("subtask: running", "parent task", parentTaskName, "sub task", sub.GetName())
		if err := sub.Call(ctx); err != nil {
			klog.ErrorS(err, "Subtask failed", "parent task", parentTaskName, "sub task", sub.GetName())
			return err
		}
		klog.InfoS("subtask: Done", "parent task", parentTaskName, "sub task", sub.GetName())
	}
	klog.InfoS("parent task: Done", "task", parentTaskName)

	return nil
}

// oracleUser returns uid and gid of the Oracle user.
func oracleUser(util osUtil) (uint32, uint32, error) {
	u, err := util.Lookup(consts.OraUser)
	if err != nil {
		return 0, 0, fmt.Errorf("oracleUser: could not determine the current user: %v", err)
	}

	if u.Username == "root" {
		return 0, 0, fmt.Errorf("oracleUser: this program is designed to run by the Oracle software installation owner (e.g. oracle), not %q", u.Username)
	}

	// Oracle user's primary group name should be either dba or oinstall.
	groups := consts.OraGroup
	var gids []string
	for _, group := range groups {
		g, err := util.LookupGroup(group)
		// Not both groups are mandatory, e.g. oinstall may not exist.
		klog.InfoS("looking up groups", "group", group, "g", g)
		if err != nil {
			continue
		}
		gids = append(gids, g.Gid)
	}
	for _, g := range gids {
		if u.Gid == g {
			usr, err := strconv.ParseUint(u.Uid, 10, 32)
			if err != nil {
				return 0, 0, err
			}
			grp, err := strconv.ParseUint(u.Gid, 10, 32)
			if err != nil {
				return 0, 0, err
			}
			return uint32(usr), uint32(grp), nil
		}
	}
	return 0, 0, fmt.Errorf("oracleUser: current user's primary group (GID=%q) is not dba|oinstall (GID=%q)", u.Gid, gids)
}

// LoadTemplateListener applies listener input to listener and tns template.
// It returns listener tns and sqlnet in string.
// In contrast to pfile, env file and a control file, there may be multiple listeners
// and a search/replace in that file is different, so it's easier to load it while
// iterating over listeners, not ahead of time. This method also generates the tnsnames
// based on the port numbers of the listeners.
func LoadTemplateListener(l *ListenerInput, name, port, protocol string) (string, string, string, error) {
	l.ListenerName = name
	l.ListenerPort = port
	l.ListenerProtocol = protocol
	t, err := template.New(filepath.Base(ListenerTemplateName)).ParseFiles(ListenerTemplateName)
	if err != nil {
		return "", "", "", fmt.Errorf("LoadTemplateListener: parsing %q failed: %v", ListenerTemplateName, err)
	}

	listenerBuf := &bytes.Buffer{}
	if err := t.Execute(listenerBuf, l); err != nil {
		return "", "", "", fmt.Errorf("LoadTemplateListener: executing %q failed: %v", ListenerTemplateName, err)
	}

	tns, err := template.New(filepath.Base(TnsnamesTemplateName)).ParseFiles(TnsnamesTemplateName)
	if err != nil {
		return "", "", "", fmt.Errorf("LoadTemplateListener: parsing %q failed: %v", TnsnamesTemplateName, err)
	}

	tnsBuf := &bytes.Buffer{}
	if err := tns.Execute(tnsBuf, l); err != nil {
		return "", "", "", fmt.Errorf("LoadTemplateListener: executing %q failed: %v", TnsnamesTemplateName, err)
	}

	sqlnet, err := ioutil.ReadFile(SQLNetSrc)
	if err != nil {
		return "", "", "", fmt.Errorf("initDBListeners: unable to read sqlnet from scripts directory: %v", err)
	}
	return listenerBuf.String(), tnsBuf.String(), string(sqlnet), nil
}

// MakeDirs creates directories in the container.
func MakeDirs(ctx context.Context, dirs []string, uid, gid uint32) error {
	for _, odir := range dirs {
		if err := os.MkdirAll(odir, 0750); err != nil {
			return fmt.Errorf("create a directory %q failed: %v", odir, err)
		}
		klog.InfoS("created a directory", "dir", odir)
	}
	return nil
}

// replace does a search and replace of term to toterm in place.
func replace(outFile, term, toterm string, uid, gid uint32) error {
	input, err := ioutil.ReadFile(outFile)
	if err != nil {
		return fmt.Errorf("replace: reading %q failed: %v", outFile, err)
	}
	out := bytes.Replace(input, []byte(term), []byte(toterm), -1)
	if err := ioutil.WriteFile(outFile, out, 0600); err != nil {
		return fmt.Errorf("replace: error writing file: %v", err)
	}

	return nil
}

// MoveFile moves a file between directories.
// os.Rename() gives error "invalid cross-device link" for Docker container with Volumes.
func MoveFile(sourceFile, destFile string) error {
	klog.Infof("Moving %s to %s", sourceFile, destFile)
	inputFile, err := os.Open(sourceFile)
	if err != nil {
		return fmt.Errorf("couldn't open source file: %s", err)
	}
	defer func() {
		if err := inputFile.Close(); err != nil {
			klog.Warningf("failed to close $v: %v", inputFile, err)
		}
	}()
	outputFile, err := os.Create(destFile)
	if err != nil {
		return fmt.Errorf("couldn't open dest file: %s", err)
	}
	defer func() {
		if err := outputFile.Close(); err != nil {
			klog.Warningf("failed to close $v: %v", outputFile, err)
		}
	}()
	_, err = io.Copy(outputFile, inputFile)
	if err != nil {
		return fmt.Errorf("writing to output file failed: %s", err)
	}
	// The copy was successful, so now delete the original file
	return os.Remove(sourceFile)
}

// MoveConfigFiles moves Database config files from Oracle standard paths to the
// persistent configuration in the PD.
func MoveConfigFiles(OracleHome, CDBName string) error {
	// /u02/app/oracle/oraconfig/<CDBName>
	configDir := fmt.Sprintf(consts.ConfigDir, consts.DataMount, CDBName)
	// /u01/app/oracle/product/12.2/db/dbs/
	sourceConfigDir := filepath.Join(OracleHome, "dbs")
	for _, f := range []string{fmt.Sprintf("spfile%s.ora", CDBName), fmt.Sprintf("orapw%s", CDBName)} {
		sf := filepath.Join(sourceConfigDir, f)
		tf := filepath.Join(configDir, f)
		tfDir := filepath.Dir(tf)
		if err := os.MkdirAll(tfDir, 0750); err != nil {
			return fmt.Errorf("MoveConfigFiles: failed to create dir %s: %v", tfDir, err)
		}
		if err := MoveFile(sf, tf); err != nil {
			return fmt.Errorf("MoveConfigFiles: move config file %s to %s failed: %v", sf, tf, err)
		}
	}
	return nil
}

// getConfigFilesMapping returns config files symlink mapping.
func getConfigFilesMapping(OracleHome, CDBName string) map[string]string {
	mapping := make(map[string]string)

	configDir := fmt.Sprintf(consts.ConfigDir, consts.DataMount, CDBName)
	sourceConfigDir := filepath.Join(OracleHome, "dbs")

	for _, f := range []string{fmt.Sprintf("spfile%s.ora", CDBName), fmt.Sprintf("orapw%s", CDBName)} {
		link := filepath.Join(sourceConfigDir, f)
		file := filepath.Join(configDir, f)
		mapping[link] = file
	}
	return mapping
}

// RelinkConfigFiles creates softlinks under the Oracle standard paths from the
// persistent configuration files in the PD.
func RelinkConfigFiles(OracleHome, CDBName string) error {
	if err := RemoveConfigFileLinks(OracleHome, CDBName); err != nil {
		return fmt.Errorf("RelinkConfigFiles: unable to delete existing links: %v", err)
	}

	for link, file := range getConfigFilesMapping(OracleHome, CDBName) {
		if err := os.Symlink(file, link); err != nil {
			return fmt.Errorf("RelinkConfigFiles: symlink creation failed from %s to oracle directories %s: %v", link, file, err)
		}
	}
	return nil
}

// RemoveConfigFileLinks removes softlinks of config files under the Oracle standard path.
// Prepare for database creation through DBCA.
func RemoveConfigFileLinks(OracleHome, CDBName string) error {
	for link := range getConfigFilesMapping(OracleHome, CDBName) {
		if _, err := os.Lstat(link); err == nil {
			if err := os.Remove(link); err != nil {
				return fmt.Errorf("RemoveConfigFileLinks: unable to delete existing link %s: %v", link, err)
			}
		}
	}
	return nil
}

// CreateUserCmd returns sql cmd to create a user with provided identifier.
func CreateUserCmd(user, identifier string) string {
	return fmt.Sprintf("create user %s identified by %s", user, identifier)
}

// ChangePasswordCmd returns sql cmd to change user identifier.
func ChangePasswordCmd(user, newIdentifier string) string {
	return fmt.Sprintf("alter user %s identified by %s ", user, newIdentifier)
}

// GrantUserCmd returns sql cmd to grant permissions to a user.
// Permissions are either a single permission or a list of permissions separated by comma.
func GrantUserCmd(user, permissions string) string {
	return fmt.Sprintf("grant %s to %s", permissions, user)
}

// FetchMetaDataFromImage returns Oracle Home, CDB name, Version by parsing
// database image metadata file if it exists. Otherwise, environment variables are used.
func FetchMetaDataFromImage() (oracleHome, cdbName, version string, err error) {
	if _, err = os.Stat(MetaDataFile); os.IsNotExist(err) {
		//some images such as OCR images may not contain a .metadata file
		return fetchMetaDataFromEnvironmentVars()
	}
	return fetchMetaDataFromMetadataFile()
}

func fetchMetaDataFromEnvironmentVars() (oracleHome, cdbName, version string, err error) {
	if os.Getenv("ORACLE_SID") != "" {
		cdbName = os.Getenv("ORACLE_SID")
		//the existence of the ORACLE_SID env variable isn't enough to conclude that a CDB of that name exists
		//The existence of an oradata directory containing ORACLE_SID confirms the existence of a CDB of that name
		if _, err = os.Stat(os.Getenv("ORACLE_BASE") + "/oradata/" + os.Getenv("ORACLE_SID")); os.IsNotExist(err) {
			cdbName = ""
		}
	}
	return os.Getenv("ORACLE_HOME"), cdbName, getOracleVersionUsingOracleHome(os.Getenv("ORACLE_HOME")), nil
}

func fetchMetaDataFromMetadataFile() (oracleHome, cdbName, version string, err error) {
	f, err := os.Open(MetaDataFile)
	if err != nil {
		return "", "", "", err
	}
	defer func() {
		if err := f.Close(); err != nil {
			klog.Warningf("failed to close %v: %v", f, err)
		}
	}()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		kv := strings.Split(line, "=")

		switch kv[0] {
		case "ORACLE_HOME":
			oracleHome = kv[1]
		case "ORACLE_SID":
			cdbName = kv[1]
		case "VERSION":
			version = kv[1]
		}
	}
	return oracleHome, cdbName, version, nil
}

// getVersionUsingOracleHome infers the version of the ORACLE Database installation from the specified ORACLE_HOME path
func getOracleVersionUsingOracleHome(oracleHome string) string {
	tokens := strings.Split(oracleHome, "/")
	return tokens[len(tokens)-2]
}

// GetDefaultInitParams returns default init parameters, which will be set in DB creation.
func GetDefaultInitParams(dbName string) map[string]string {
	controlFileLoc := filepath.Join(fmt.Sprintf(consts.DataDir, consts.DataMount, dbName), "control01.ctl")
	initParamDict := make(map[string]string)
	initParamDict["log_archive_dest_1"] = "'LOCATION=USE_DB_RECOVERY_FILE_DEST'"
	initParamDict["enable_pluggable_database"] = "TRUE"
	initParamDict["common_user_prefix"] = "'gcsql$'"
	initParamDict["control_files"] = fmt.Sprintf("'%s'", controlFileLoc)
	return initParamDict
}

// MapToSlice converts map[string]string into a string slice with format "<key>=<value>".
func MapToSlice(kv map[string]string) []string {
	var result []string
	for k, v := range kv {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}
	return result
}

// MergeInitParams merges default parameters and user specified parameters, and
// returns merged parameters.
func MergeInitParams(defaultParams map[string]string, userParams []string) (map[string]string, error) {
	mergedParams := make(map[string]string)

	for _, userParam := range userParams {
		kv := strings.Split(userParam, "=")
		if len(kv) != 2 {
			return nil, fmt.Errorf("MergeInitParam: user param %s is not separated by =", userParam)
		}
		klog.InfoS("provision/MergeInitParams: adding user param", "key", kv[0], "val", kv[1])
		mergedParams[kv[0]] = kv[1]
	}
	// We only support merging of user params and reject any params trying to override our internal setting used by controller.
	// For example, if we permit overrides and the user tries to reassign common_user_prefix with says xyz instead of the default gcsql$, our health checks will break.
	for k, v := range defaultParams {
		if val, ok := mergedParams[k]; ok {
			klog.InfoS("provision/MergeInitParams: overriding user param", "key", k, "user defined val", val, "override val", v)
		}
		mergedParams[k] = v
		klog.InfoS("provision/MergeInitParams: adding default param", "key", k, "val", v)
	}

	return mergedParams, nil
}
