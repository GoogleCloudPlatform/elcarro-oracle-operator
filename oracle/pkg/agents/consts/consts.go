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

// Package consts provides common Oracle constants across the entire Data Plane.
package consts

// Listener is an Oracle listener struct
type Listener struct {
	LType    string
	Port     int32
	Local    bool
	Protocol string
}

// OraDir wraps up the host and oracle names for a specific directory. e.g.
// /u03/app/oracle/<dbname>/dpdump and PDB_DATA_PUMP_DIR
type OraDir struct {
	Linux  string
	Oracle string
}

const (
	// DefaultHealthAgentPort is Health Agent's default port number.
	DefaultHealthAgentPort = 3201

	// DefaultConfigAgentPort is Config Agent's default port number.
	DefaultConfigAgentPort = 3202

	// DefaultDBDaemonPort is DB daemon's default port number.
	DefaultDBDaemonPort = 3203

	// DefaultMonitoringAgentPort is the default port where the oracle exporter runs
	DefaultMonitoringAgentPort = 9161

	// Localhost is a general localhost name.
	Localhost = "localhost"

	// DomainSocketFile is meant for the agents to communicate to the Database Daemon.
	DomainSocketFile = "/var/tmp/dbdaemon.sock"

	// ProxyDomainSocketFile is meant for the database daemon to communicate to the database daemon proxy.
	ProxyDomainSocketFile = "/var/tmp/dbdaemon_proxy.sock"

	// SecureListenerPort is a secure listener port number.
	SecureListenerPort = 6021
	// SSLListenerPort is an SSL listener port number.
	SSLListenerPort = 3307

	// OpenPluggableDatabaseSQL is used to open pluggable database (e.g. after CDB start).
	OpenPluggableDatabaseSQL = "alter pluggable database all open"

	// ListPDBsSQL lists pluggable databases excluding a root container.
	ListPDBsSQL = "select name from v$containers where name !='CDB$ROOT'"

	// ListPluggableDatabaseExcludeSeedSQL is used to list pluggable databases exclude PDB$SEED
	ListPluggableDatabaseExcludeSeedSQL = "select pdb_name from dba_pdbs where pdb_name!='PDB$SEED'"

	// DefaultPGAMB is the default size of the PGA which the CDBs are created.
	DefaultPGAMB = 1200

	// DefaultSGAMB is the default size of the SGA which the CDBs are created.
	DefaultSGAMB = 1800

	//Oracle18c Version for Oracle 18c XE
	Oracle18c = "18c"

	// SourceDatabaseHost is the hostname used during image build process.
	SourceDatabaseHost = "ol7-db12201-gi-cdb-docker-template-vm"

	// CharSet is the supported character set for Oracle databases.
	CharSet = "AL32UTF8"

	// OracleDBContainerName is the container name for the Oracle database.
	OracleDBContainerName = "oracle_db"

	// SecurityUser is the user for lockdown triggers.
	SecurityUser = "gcsql$security"

	// PDBLoaderUser is the user for impdb/expdp operations by end users.
	PDBLoaderUser = "gcsql$pdbloader"

	// MonitoringAgentName is the container name for the monitoring agent.
	MonitoringAgentName = "oracle-monitoring"

	// DefaultExitErrorCode is default exit code
	DefaultExitErrorCode = 128

	// RMANBackup is the oracle rman command for taking backups.
	RMANBackup = "backup"
)

var (
	// ProvisioningDoneFile is a flag name/location created at the end of provisioning.
	// this is placed on the PD storage so that on recreate, the bootstrap doesnt re-run.
	ProvisioningDoneFile = "/u02/app/oracle/provisioning_successful"

	// SECURE is the name of the secure tns listener
	SECURE = "SECURE"
	// ListenerNames is the list of listeners
	ListenerNames = map[string]*Listener{
		"SECURE": {
			LType:    SECURE,
			Port:     SecureListenerPort,
			Local:    true,
			Protocol: "TCP",
		},
		"SSL": {
			LType:    "SSL",
			Port:     SSLListenerPort,
			Protocol: "TCPS",
		},
	}

	// DpdumpDir is the Impdp/Expdp directory and oracle directory name.
	// Linux is relative to the PDB PATH_PREFIX.
	DpdumpDir = OraDir{Linux: "dmp", Oracle: "PDB_DATA_PUMP_DIR"}

	// OraGroup is the group that owns the database software.
	OraGroup = []string{"dba", "oinstall"}

	// OraTab is the oratab file path.
	OraTab = "/etc/oratab"

	// OraUser is the owner of the database and database software.
	OraUser = "oracle"

	// OracleBase is the Oracle base path.
	OracleBase = "/u02/app/oracle"

	// DataDir is the directory where datafiles exists.
	DataDir = "/%s/app/oracle/oradata/%s"

	// PDBDataDir is the directory where PDB datafiles exists.
	PDBDataDir = DataDir + "/%s/data"

	// PDBSeedDir is the directory where the SEED datafiles exists.
	PDBSeedDir = DataDir + "/pdbseed"

	// PDBPathPrefix is the directory where PDB data directory exists.
	PDBPathPrefix = DataDir + "/%s"

	// ConfigDir is where the spfile, pfile and pwd file are persisted.
	ConfigDir = "/%s/app/oracle/oraconfig/%s"

	// RecoveryAreaDir is where the flash recovery area will be.
	RecoveryAreaDir = "/%s/app/oracle/fast_recovery_area/%s"

	// DataMount is the PD mount where the data is persisted.
	DataMount = "u02"

	// LogMount is the PD mount where the logs are persisted.
	LogMount = "u03"

	// ListenerDir is the listener directory.
	ListenerDir = "/%s/app/oracle/oraconfig/network"

	// ScriptDir is where the scripts are located on the container image.
	ScriptDir = "/agents"

	// WalletDir is where the SSL Certs are stored.
	WalletDir = "/u02/app/oracle/wallet"

	// OracleDir is where the env file is located
	OracleDir = "/home/oracle"

	// DefaultRMANDir sets the default rman backup directory
	DefaultRMANDir = "/u03/app/oracle/rman"

	// RMANStagingDir sets the staging directory for rman backup to GCS.
	RMANStagingDir = "/u03/app/oracle/rmanstaging"
)
