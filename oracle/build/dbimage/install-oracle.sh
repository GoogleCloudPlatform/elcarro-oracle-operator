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
readonly DB_VERSION="$1"
readonly EDITION="$2"
readonly CREATE_CDB="$3"
readonly CDB_NAME="$4"
readonly CHARACTER_SET=${5:-AL32UTF8}
readonly MEM_PCT=${6:-25}
PATCH_VERSION="$7"
readonly INIT_PARAMS="log_archive_dest_1='LOCATION=USE_DB_RECOVERY_FILE_DEST',enable_pluggable_database=TRUE,common_user_prefix='gcsql$'"
readonly USER='oracle'
readonly GROUP='dba'
readonly OCM_FILE="ocm.rsp"
readonly OHOME="/u01/app/oracle/product/${DB_VERSION}/db"
readonly ORACLE_12="12.2"
readonly ORACLE_19="19.3"

setup_patching() {
  if [[ "${PATCH_VERSION}" == "-1" ]]; then
    if [[ "${DB_VERSION}" == "${ORACLE_12}" ]]; then
      PATCH_VERSION="31741641"
    elif [[ "${DB_VERSION}" == "${ORACLE_19}" ]]; then
      PATCH_VERSION="32545013"
    fi
  fi
  local patch_suffix
  if [[ "${DB_VERSION}" == "${ORACLE_12}" ]]; then
    patch_suffix="_122010_Linux-x86-64.zip"
  elif [[ "${DB_VERSION}" == "${ORACLE_19}" ]]; then
    patch_suffix="_190000_Linux-x86-64.zip"
  fi
  PATCH_ZIP="p${PATCH_VERSION}${patch_suffix}"

  if [[ ! -f "${STAGE_DIR}"/"${PATCH_ZIP}" ]]; then
    echo "could not find the PSU zip in ${STAGE_DIR}/${PATCH_ZIP}, possible fixes:
if '--local_build' is enabled, try stage ${PATCH_ZIP} to the same directory with image_build.sh.
if '--local_build' is disabled, try stage ${PATCH_ZIP} to the gcs bucket.
Update '--patch_version' when running image_build.sh, the script assumes the file name is 'p<var>patch_version</var>${patch_suffix}'."
    exit 1
  fi
}

setup_installers() {
  if [[ "${DB_VERSION}" == "${ORACLE_12}" ]]; then
    local -g INSTALL_CONFIG="ora12-config.sh"
    # shellcheck source=ora12-config.sh
    source "${STAGE_DIR}"/"${INSTALL_CONFIG}"
    local -g CHECKSUM_FILE="checksum.sha256.12"
  elif [[ "${DB_VERSION}" == "${ORACLE_19}" ]]; then
    local -g INSTALL_CONFIG="ora19-config.sh"
    # shellcheck source=ora19-config.sh
    source "${STAGE_DIR}"/"${INSTALL_CONFIG}"
    local -g CHECKSUM_FILE="checksum.sha256.19"
  else
    echo "DB version ${DB_VERSION} not supported"
    exit 1
  fi
  _fallback_install_file
  _fallback_opatch_file
}

_fallback_install_file() {
  if [[ -f "${STAGE_DIR}"/"${INSTALL_ZIP}" ]]; then
    echo "found DB installer zip ${STAGE_DIR}/${INSTALL_ZIP}, install will use ${INSTALL_ZIP}"
    return
  fi
  # installer zip can be downloaded either from OTN or edelivery
  # default file name for edelivery: V839960-01.zip
  # default file name for OTN: linuxx64_12201_database.zip
  local candidates=("linuxx64_12201_database.zip")
  if [[ "${DB_VERSION}" == "${ORACLE_19}" ]]; then
    candidates=("LINUX.X64_193000_db_home.zip")
  fi
  echo "could not find the specified DB installer zip. Will try falling back to one of these possible names:" "${candidates[@]}"

  for f in "${candidates[@]}"; do
    if [[ -f "${STAGE_DIR}"/"${f}" ]]; then
      echo "found DB installer zip ${STAGE_DIR}/${f},install will use ${f}"
      INSTALL_ZIP="${f}"
      break
    else
      echo "could not find fallback DB installer zip ${STAGE_DIR}/${f}, try specifying an installer from one of these possible names:" "${candidates[@]}"
    fi
  done
}

_fallback_opatch_file() {
  if [[ -f "${STAGE_DIR}"/"${OPATCH_ZIP}" ]]; then
    echo "found opatch zip ${STAGE_DIR}/${OPATCH_ZIP}, install will use ${OPATCH_ZIP}"
    return
  fi
  # OPATCH_ZIP can be p6880880_200000_Linux-x86-64.zip, p6880880_190000_Linux-x86-64.zip
  # p6880880_180000_Linux-x86-64.zip, p6880880_122010_Linux-x86-64.zip
  # for 04302020 OPATCH, their sha256sum are identical "B08320195434559D9662729C5E02ABC8436A5C602B4355CC33A673F24D9D174"
  local candidates=(
    "p6880880_200000_Linux-x86-64.zip"
    "p6880880_190000_Linux-x86-64.zip"
    "p6880880_180000_Linux-x86-64.zip"
    "p6880880_122010_Linux-x86-64.zip"
  )
  echo "cannot find opatch zip from config, try one of possible names" "${candidates[@]}"

  for f in "${candidates[@]}"; do
    if [[ -f "${STAGE_DIR}"/"${f}" ]]; then
      echo "found opatch zip ${STAGE_DIR}/${f},install will use ${f}"
      OPATCH_ZIP="${f}"
      break
    else
      echo "cannot find opatch zip ${STAGE_DIR}/${f}, try fallback to other possible names"
    fi
  done
}

setup_ocm() {
  if [[ "${DB_VERSION}" == "${ORACLE_19}" ]]; then
    echo "oracle.install.responseFileVersion=/oracle/install/rspfmt_dbinstall_response_schema_v19.0.0" >"${STAGE_DIR}/${OCM_FILE}"
  elif [[ "${DB_VERSION}" == "${ORACLE_12}" ]]; then
    echo "oracle.install.responseFileVersion=/oracle/install/rspfmt_dbinstall_response_schema_v12.2.0" >"${STAGE_DIR}/${OCM_FILE}"
  else
    echo "Unsupported version ${DB_VERSION}"
    exit 1
  fi
  echo "oracle.install.option=INSTALL_DB_SWONLY" >>"${STAGE_DIR}/${OCM_FILE}"
  echo "UNIX_GROUP_NAME=dba" >>"${STAGE_DIR}/${OCM_FILE}"
  echo "INVENTORY_LOCATION=/u01/app/oracle/oraInventory" >>"${STAGE_DIR}/${OCM_FILE}"
  echo "ORACLE_HOME=${OHOME}" >>"${STAGE_DIR}/${OCM_FILE}"
  echo "ORACLE_BASE=/u01/app/oracle" >>"${STAGE_DIR}/${OCM_FILE}"
  echo "oracle.install.db.InstallEdition=${EDITION}" >>"${STAGE_DIR}/${OCM_FILE}"
  echo "oracle.install.db.OSDBA_GROUP=dba" >>"${STAGE_DIR}/${OCM_FILE}"
  echo "oracle.install.db.OSOPER_GROUP=dba" >>"${STAGE_DIR}/${OCM_FILE}"
  echo "oracle.install.db.OSBACKUPDBA_GROUP=dba" >>"${STAGE_DIR}/${OCM_FILE}"
  echo "oracle.install.db.OSDGDBA_GROUP=dba" >>"${STAGE_DIR}/${OCM_FILE}"
  echo "oracle.install.db.OSKMDBA_GROUP=dba" >>"${STAGE_DIR}/${OCM_FILE}"
  echo "oracle.install.db.OSRACDBA_GROUP=dba" >>"${STAGE_DIR}/${OCM_FILE}"
  echo "SECURITY_UPDATES_VIA_MYORACLESUPPORT=false" >>"${STAGE_DIR}/${OCM_FILE}"
  echo "DECLINE_SECURITY_UPDATES=true" >>"${STAGE_DIR}/${OCM_FILE}"
}

setup_directories() {
  mkdir -p "${STAGE_DIR}/patches"
  chown "${USER}:${GROUP}" "${STAGE_DIR}/patches"
}

patch_oracle() {
  cd "${STAGE_DIR}/patches/${PATCH_VERSION}/${PATCH_VERSION}"
  sudo -E -u oracle "${OHOME}/OPatch/opatch" \
    apply -silent -ocmrf "${STAGE_DIR}/${OCM_FILE}"
}

checksum_files() {
  cd "${STAGE_DIR}"
  cat >"${CHECKSUM_FILE}" <<EOF
${INSTALL_ZIP_SHA256} ${INSTALL_ZIP}
EOF
  cat "${CHECKSUM_FILE}"
  if ! sha256sum -c "${STAGE_DIR}/${CHECKSUM_FILE}" --quiet; then
    echo "Checksum failure of installables."
    echo "try double check ${INSTALL_CONFIG} DB install/OPatch/PSUs filename and sha256, ensure they are matched with GCS bucket staged zip files"
    exit 1
  fi
}

install_oracle() {
  if [[ "${DB_VERSION}" == "${ORACLE_12}" ]]; then
    install_oracle12
  elif [[ "${DB_VERSION}" == "${ORACLE_19}" ]]; then
    install_oracle19
  fi
}

install_oracle19() {
  printf "inventory_loc=/u01/app/oracle/oraInventory\ninst_group=dba\n" \
    >>/etc/oraInst.loc
  chown "${USER}:${GROUP}" /etc/oraInst.loc
  cd "${OHOME}"
  export CV_ASSUME_DISTID=OL7
  sudo -E -u oracle "${OHOME}/runInstaller" \
    -silent \
    -waitforcompletion \
    -ignorePrereqFailure \
    INVENTORY_LOCATION=/etc/oraInst.loc \
    UNIX_GROUP_NAME=dba \
    ORACLE_HOME="${OHOME}" \
    ORACLE_BASE=/u01/app/oracle \
    oracle.install.db.InstallEdition="${EDITION}" \
    oracle.install.db.OSDBA_GROUP="${GROUP}" \
    oracle.install.db.OSOPER_GROUP="${GROUP}" \
    oracle.install.db.OSBACKUPDBA_GROUP="${GROUP}" \
    oracle.install.db.OSDGDBA_GROUP="${GROUP}" \
    oracle.install.db.OSKMDBA_GROUP="${GROUP}" \
    oracle.install.db.OSRACDBA_GROUP="${GROUP}" \
    oracle.install.option=INSTALL_DB_SWONLY ||
    (($? == 6)) # Check for successful install with warning suppressed.

  enable_unified_auditing

  "${OHOME}/root.sh"
}

install_oracle12() {
  printf "inventory_loc=/u01/app/oracle/oraInventory\ninst_group=dba\n" \
    >>/etc/oraInst.loc
  chown "${USER}:${GROUP}" /etc/oraInst.loc
  sudo -u oracle "${STAGE_DIR}/database/runInstaller" \
    -silent \
    -force \
    -invptrloc /etc/oraInst.loc \
    -waitforcompletion \
    -ignoresysprereqs \
    -ignoreprereq \
    UNIX_GROUP_NAME=dba \
    ORACLE_HOME="${OHOME}" \
    ORACLE_BASE=/u01/app/oracle \
    oracle.install.db.InstallEdition="${EDITION}" \
    oracle.install.db.OSDBA_GROUP="${GROUP}" \
    oracle.install.db.OSOPER_GROUP="${GROUP}" \
    oracle.install.db.OSBACKUPDBA_GROUP="${GROUP}" \
    oracle.install.db.OSDGDBA_GROUP="${GROUP}" \
    oracle.install.db.OSKMDBA_GROUP="${GROUP}" \
    oracle.install.db.OSRACDBA_GROUP="${GROUP}" \
    oracle.install.option=INSTALL_DB_SWONLY ||
    (($? == 6)) # Check for successful install with warning suppressed.

  enable_unified_auditing

  "${OHOME}/root.sh"

}

enable_unified_auditing() {
  cd "${OHOME}/rdbms/lib"
  sudo -u oracle \
    make -f ins_rdbms.mk uniaud_on ioracle ORACLE_HOME="${OHOME}"
}

create_cdb() {
  set +x
  local syspass="$(openssl rand -base64 16 | tr -dc a-zA-Z0-9)"
  sudo -u oracle "${OHOME}/bin/dbca" \
    -silent \
    -createDatabase \
    -templateName General_Purpose.dbc \
    -gdbname "${CDB_NAME}" \
    -createAsContainerDatabase true \
    -sid "${CDB_NAME}" \
    -responseFile NO_VALUE \
    -characterSet "${CHARACTER_SET}" \
    -memoryPercentage "${MEM_PCT}" \
    -emConfiguration NONE \
    -datafileDestination "/u01/app/oracle/oradata" \
    -storageType FS \
    -initParams "${INIT_PARAMS}" \
    -databaseType MULTIPURPOSE \
    -recoveryAreaDestination /u01/app/oracle/fast_recovery_area \
    -sysPassword "${syspass}" \
    -systemPassword "${syspass}"
  set -x
}

prep_seedpdb() {
  # Consider switching the temp/undo to bigfile and enabling size management of
  # these at the KRM level.

  # Ensure tempfile can grow to one full datafile 32GB by default.
  run_sql "alter session set container=pdb\$seed; alter database tempfile '/u01/app/oracle/oradata/${CDB_NAME}/pdbseed/temp01.dbf' autoextend on maxsize unlimited;"
}

set_environment() {
  source "/home/oracle/${CDB_NAME}.env"
}

cleanup_post_success() {
  # ORDS
  rm -rf "${OHOME}/ords" &&
    # SQL Developer
    rm -rf "${OHOME}/sqldeveloper" &&
    # UCP connection pool
    rm -rf "${OHOME}/ucp" &&
    # All installer files
    rm -rf "${OHOME}/lib/*.zip" &&
    # OUI backup
    rm -rf "${OHOME}/inventory/backup/*" &&
    # Network tools help
    rm -rf "${OHOME}/network/tools/help" &&
    # Database upgrade assistant
    rm -rf "${OHOME}/assistants/dbua" &&
    # Database migration assistant
    rm -rf "${OHOME}/dmu" &&
    # Remove pilot workflow installer
    rm -rf "${OHOME}/install/pilot" &&
    # Support tools
    rm -rf "${OHOME}/suptools" &&
    # Previous patches binaries
    rm -rf "${OHOME}/.patch_storage" &&
    # Temp location
    rm -rf /tmp/* &&
    # Install files
    rm -rf "${STAGE_DIR}"
}

unzip_binaries() {
  if [[ "${DB_VERSION}" == "${ORACLE_12}" ]]; then
    chown -R "${USER}:${GROUP}" "${STAGE_DIR}"
    sudo -u oracle unzip "${STAGE_DIR}/${INSTALL_ZIP}" -d "${STAGE_DIR}"
  elif [[ "${DB_VERSION}" == "${ORACLE_19}" ]]; then
    sudo -u oracle unzip "${STAGE_DIR}/${INSTALL_ZIP}" -d "${OHOME}"
  fi
  sudo -u oracle unzip "${STAGE_DIR}/${PATCH_ZIP}" -d "${STAGE_DIR}/patches/${PATCH_VERSION}"
  rm "${STAGE_DIR}/${INSTALL_ZIP}"
  rm "${STAGE_DIR}/${PATCH_ZIP}"
}

shutdown_oracle() {
  run_sql "shutdown immediate;"
  echo "Oracle Database Shutdown"
}

run_sql() {
  echo "${1}" | sudo -E -u oracle "${OHOME}/bin/sqlplus" -S / as sysdba
}

patch_log4j(){
  # Delete Jndi classes from log4j jars as part of CVE-2021-44228
  # While waiting for oracle patches.
  for f in $(find "${OHOME}" -type f -name "log4j-core*.jar"); do
    sudo -u oracle zip -q -d "$f" org/apache/logging/log4j/core/lookup/JndiLookup.class || true
  done
}

main() {
  setup_patching
  setup_installers
  checksum_files
  setup_directories
  setup_ocm
  if [[ "${CREATE_CDB}" == true ]]; then
    set_environment
  fi
  chown -R "${USER}:${GROUP}" /u01
  unzip_binaries
  install_oracle
  rm -rf "${OHOME}/OPatch"
  sudo -u oracle unzip "${STAGE_DIR}/${OPATCH_ZIP}" -d "${OHOME}"
  patch_oracle
  if [[ "${CREATE_CDB}" == true ]]; then
    create_cdb
    prep_seedpdb
    shutdown_oracle
  fi
  chown "${USER}:${GROUP}" /etc/oratab
  cleanup_post_success
  patch_log4j
  echo "Oracle installation success"
}
main
