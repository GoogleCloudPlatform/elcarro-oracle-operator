// Copyright 2022 Google LLC
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

package standby

import (
	"bytes"
	"fmt"
	"text/template"
)

const (
	standbyInitOraTemplateName    = "standby_init_ora_template"
	standbyInitOraTemplateContent = `
*.enable_pluggable_database={{ .PrimaryEnablePDB }}
*.dispatchers='(PROTOCOL=TCP) (SERVICE={{ .PrimaryDbName }}XDB)'
*.log_archive_dest_1='LOCATION=USE_DB_RECOVERY_FILE_DEST'
*.compatible={{ .PrimaryCompatibility }}
*.db_domain='{{ .StandbyDbDomain }}'
*.db_name={{ .PrimaryDbName }}
*.db_unique_name={{ .StandbyDBUniqueName }}
*.db_file_name_convert=({{ .DataFileDirList }})
*.log_file_name_convert=({{ .LogFileDirList }})
*.audit_file_dest='/u02/app/oracle/admin/{{ .PrimaryDbName }}/adump'
*.control_files='/u02/app/oracle/oradata/{{ .PrimaryDbName }}/control01.ctl'
*.db_recovery_file_dest='/u03/app/oracle/fast_recovery_area/{{ .PrimaryDbName }}'
*.audit_trail='DB'
*.db_block_size=8192
*.diagnostic_dest='/u02/app/oracle'
*.db_recovery_file_dest_size={{ .StandbyLogDiskSize }}
*.filesystemio_options='SETALL'
*.local_listener='(DESCRIPTION=(ADDRESS=(PROTOCOL=ipc)(KEY=REGLSNR_6021)))'
*.open_cursors=300
*.pga_aggregate_target={{ .StandbyPgaSizeKB }}K
*.remote_login_passwordfile='EXCLUSIVE'
*.sga_target={{ .StandbySgaSizeKB }}K
*.standby_file_management='AUTO'
*.undo_tablespace='UNDOTBS1'
*.log_archive_dest_state_1='ENABLE'
`
)

// standbyInitOraInput holds the required parameters for constructing init.ora file for EM replica.
type standbyInitOraInput struct {
	PrimaryDbName        string
	PrimaryDbUniqueName  string
	DataFileDirList      string
	LogFileDirList       string
	PrimaryHost          string
	PrimaryPort          int
	PrimaryCompatibility string
	PrimaryEnablePDB     string
	StandbyDBUniqueName  string
	StandbyDbDomain      string
	StandbyLogDiskSize   int64
	StandbySgaSizeKB     int
	StandbyPgaSizeKB     int
}

// loadInitOraTemplate generates an init ora content using the template and the required parameters.
func (i *standbyInitOraInput) loadInitOraTemplate() (string, error) {
	t, err := template.New(standbyInitOraTemplateName).Parse(standbyInitOraTemplateContent)
	if err != nil {
		return "", fmt.Errorf("loadInitOraTemplate: parsing %q failed: %v", standbyInitOraTemplateName, err)
	}

	initOraBuf := &bytes.Buffer{}
	if err := t.Execute(initOraBuf, i); err != nil {
		return "", fmt.Errorf("LoadInitOraTemplate: executing %q failed: %v", standbyInitOraTemplateName, err)
	}
	return initOraBuf.String(), nil
}
