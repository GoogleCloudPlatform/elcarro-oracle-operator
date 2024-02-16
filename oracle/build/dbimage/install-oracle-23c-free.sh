#!/bin/bash
# Copyright 2021 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# shellcheck disable=2153

set -x
set -e
set -u
export PATH="/bin:/sbin:/usr/bin:/usr/sbin:/usr/local/bin:/usr/local/sbin"
readonly CDB_NAME=${1:-GCLOUD}
readonly CHARACTER_SET=${2:-AL32UTF8}
readonly USER="oracle"
readonly GROUP="dba"
readonly OHOME="/opt/oracle/product/23c/dbhomeFree"

set_environment() {
  export ORACLE_DOCKER_INSTALL=true
  source "/home/oracle/${CDB_NAME}.env"
}


install_oracle() {
  yum -y localinstall https://download.oracle.com/otn-pub/otn_software/db-free/oracle-database-free-23c-1.0-1.el8.x86_64.rpm
}

write_oracle_config() {
  echo "\
CHARSET=${CHARACTER_SET}
ORACLE_SID=${CDB_NAME}
SKIP_VALIDATIONS=FALSE" > /etc/sysconfig/oracle-free-23c.conf
}

create_cdb() {
  set +x
  local syspass="$(openssl rand -base64 16 | tr -dc a-zA-Z0-9)"
  (echo "${syspass}"; echo "${syspass}";) | /etc/init.d/oracle-free-23c configure
  set -x
}

set_file_ownership() {
  chown -R "${USER}:${GROUP}" "${OHOME}"
  chown -R "${USER}:${GROUP}" "/home/${USER}"
  chown "${USER}:${GROUP}" /etc/oraInst.loc
  chown -R "${USER}:${GROUP}" /opt
}

shutdown_oracle() {
  run_sql "shutdown immediate;"
  echo "Oracle Database Shutdown"
}

delete_xe_pdb() {
  run_sql "ALTER PLUGGABLE DATABASE FREEPDB1 CLOSE;"
  run_sql "DROP PLUGGABLE DATABASE FREEPDB1 INCLUDING DATAFILES;"
}

run_sql() {
  echo "${1}" | sudo -E -u oracle "${ORACLE_HOME}/bin/sqlplus" -S / as sysdba
}

main() {
  echo "Running Oracle 23c FREE install script..."
  set_environment
  install_oracle
  write_oracle_config
  create_cdb
  set_file_ownership
  delete_xe_pdb
  shutdown_oracle
  echo "Oracle 23c FREE installation succeeded!"
}

main