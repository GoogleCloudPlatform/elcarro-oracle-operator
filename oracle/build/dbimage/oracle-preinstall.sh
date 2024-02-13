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

set -x
set -e
set -u
readonly ORACLE_12="12.2"
readonly ORACLE_18="18c"
readonly ORACLE_19="19.3"
readonly ORACLE_23="23c"
readonly USER='oracle'
readonly GROUP='dba'

install_packages() {
  yum install -y shadow-utils openssl sudo zip
  yum install -y nmap-ncat.x86_64
  yum install -y strace.x86_64
  yum install -y net-tools.x86_64
  yum install -y lsof.x86_64
  yum install -y "${PREINSTALL_RPM}"

  echo "#%PAM-1.0
auth       include      system-auth
account    include      system-auth
password   include      system-auth
" >/etc/pam.d/sudo
  if [[ "${DB_VERSION}" == "${ORACLE_18}" || "${DB_VERSION}" == "${ORACLE_23}" ]]; then
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
  fi
}


pick_pre_installer() {
  if [[ "${DB_VERSION}" == "${ORACLE_12}" ]]; then
    local -g PREINSTALL_RPM="oracle-database-server-12cR2-preinstall.x86_64"
  elif [[ "${DB_VERSION}" == "${ORACLE_18}" ]]; then
    local -g PREINSTALL_RPM="oracle-database-preinstall-18c.x86_64"
  elif [[ "${DB_VERSION}" == "${ORACLE_19}" ]]; then
    local -g PREINSTALL_RPM="oracle-database-preinstall-19c.x86_64"
  elif [[ "${DB_VERSION}" == "${ORACLE_23}" ]]; then
    local -g PREINSTALL_RPM="https://yum.oracle.com/repo/OracleLinux/OL8/developer/x86_64/getPackage/oracle-database-preinstall-23c-1.0-1.el8.x86_64.rpm"
  else
    echo "DB version ${DB_VERSION} not supported"
    exit 1
  fi
}

setup_directories() {
  mkdir -p "${ORACLE_HOME}"
  #use oinstall instead of dba to allow the script to work for 18c XE.
  #This is harmless because we always revert ownership of $ORACLE_BASE to oracle:dba in the Dockerfile.
  chown -R "${USER}:oinstall" "${ORACLE_BASE}"
  chown -R "${USER}:${GROUP}" "/home/${USER}"
}

create_env_file() {
  echo "export ORACLE_HOME=${ORACLE_HOME}" >>"/home/oracle/${CDB_NAME}.env"
  echo "export ORACLE_BASE=${ORACLE_BASE}" >>"/home/oracle/${CDB_NAME}.env"
  echo "export ORACLE_SID=${CDB_NAME}" >>"/home/oracle/${CDB_NAME}.env"
  echo "export PATH=${ORACLE_HOME}/bin:${ORACLE_HOME}/OPatch:/usr/local/bin:/usr/local/sbin:/sbin:/bin:/usr/sbin:/usr/bin:/root/bin" >>"/home/oracle/${CDB_NAME}.env"
  echo "export LD_LIBRARY_PATH=${ORACLE_HOME}/lib:/usr/lib" >>"/home/oracle/${CDB_NAME}.env"
  chown "${USER}:${GROUP}" "/home/oracle/${CDB_NAME}.env"
}

pick_pre_installer
install_packages
setup_directories
if [[ "${CREATE_CDB}" == true ]]; then
  create_env_file
fi
