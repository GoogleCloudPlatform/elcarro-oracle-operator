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
readonly OHOME="/opt/oracle/product/18c/dbhomeXE"
readonly DB_VERSION="18c"

setup_directories() {
  mkdir -p "/home/${USER}"
}

install_debug_utilities() {
  yum install -y shadow-utils openssl sudo
  yum install -y nmap-ncat.x86_64
  yum install -y strace.x86_64
  yum install -y net-tools.x86_64
  yum install -y lsof.x86_64
}

write_pam_files() {
  echo "#%PAM-1.0
auth       include      system-auth
account    include      system-auth
password   include      system-auth
" >/etc/pam.d/sudo

    echo "#%PAM-1.0
auth		sufficient	pam_rootok.so
auth		substack	system-auth
auth		include		postlogin
account		sufficient	pam_succeed_if.so uid = 0 use_uid quiet
account		include		system-auth
password	include		system-auth
session		include		postlogin
session		optional	pam_xauth.so
" >/etc/pam.d/su
}

set_environment() {
  export ORACLE_DOCKER_INSTALL=true
  echo "export ORACLE_HOME=${OHOME}" >>"/home/oracle/${CDB_NAME}.env"
  echo "export ORACLE_BASE=/opt/oracle" >>"/home/oracle/${CDB_NAME}.env"
  echo "export ORACLE_SID=${CDB_NAME}" >>"/home/oracle/${CDB_NAME}.env"
  echo "export PATH=${OHOME}/bin:${OHOME}/OPatch:/usr/local/bin:/usr/local/sbin:/sbin:/bin:/usr/sbin:/usr/bin:/root/bin" >>"/home/oracle/${CDB_NAME}.env"
  echo "export LD_LIBRARY_PATH=${OHOME}/lib" >>"/home/oracle/${CDB_NAME}.env"
  source "/home/oracle/${CDB_NAME}.env"
}


install_oracle() {
  yum -y localinstall https://download.oracle.com/otn-pub/otn_software/db-express/oracle-database-xe-18c-1.0-1.x86_64.rpm
}

write_oracle_config() {
  echo "\
CHARSET=${CHARACTER_SET}
ORACLE_SID=${CDB_NAME}
SKIP_VALIDATIONS=FALSE" > /etc/sysconfig/oracle-xe-18c.conf
}

create_cdb() {
  local syspass="$(openssl rand -base64 16 | tr -dc a-zA-Z0-9)"
  (echo "${syspass}"; echo "${syspass}";) | /etc/init.d/oracle-xe-18c configure
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
  run_sql "ALTER PLUGGABLE DATABASE XEPDB1 CLOSE;"
  run_sql "DROP PLUGGABLE DATABASE XEPDB1 INCLUDING DATAFILES;"
}

run_sql() {
  echo "${1}" | sudo -E -u oracle "${ORACLE_HOME}/bin/sqlplus" -S / as sysdba
}

create_metadata_file() {
  echo "ORACLE_HOME=${OHOME}" >>"/home/oracle/.metadata"
  echo "ORACLE_SID=${CDB_NAME}" >>"/home/oracle/.metadata"
  echo "VERSION=${DB_VERSION}" >>"/home/oracle/.metadata"
}

main() {
  echo "Running Oracle 18c XE install script..."
  install_debug_utilities
  write_pam_files
  setup_directories
  set_environment
  install_oracle
  write_oracle_config
  create_cdb
  set_file_ownership
  delete_xe_pdb
  shutdown_oracle
  create_metadata_file
  echo "Oracle 18c XE installation succeeded!"
}

main