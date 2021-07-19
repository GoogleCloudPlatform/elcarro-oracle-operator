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


SCRIPTS_DIR="/agents"

function term_handler() {
   echo "$(date +%Y-%m-%d.%H:%M:%S) SIGTERM received, stopping a database container..." >> "${SCRIPTS_DIR}/init_oracle.log"
   ${SCRIPTS_DIR}/stop_oracle.sh abort
}

function kill_handler() {
   echo "$(date +%Y-%m-%d.%H:%M:%S) SIGKILL received..."  >> "${SCRIPTS_DIR}/init_oracle.log"
}

function int_handler() {
   echo "$(date +%Y-%m-%d.%H:%M:%S) SIGINT received..."  >> "${SCRIPTS_DIR}/init_oracle.log"
}

get_sga_pga() {
  local tot=$(free -m|awk '/Mem/ {print $2}')
  sga=$(( ${tot} * 1 / 2 ))
  pga=$(( ${tot} * 1 / 8 ))
}

trap term_handler SIGTERM
trap kill_handler SIGKILL
trap int_handler SIGINT

echo "$(date +%Y-%m-%d.%H:%M:%S) Enabling Unified Auditing in the oracledb container..."  >> "${SCRIPTS_DIR}/init_oracle.log"
make -C $ORACLE_HOME/rdbms/lib -f ins_rdbms.mk uniaud_on ioracle ORACLE_HOME="${ORACLE_HOME}" >> "${SCRIPTS_DIR}/init_oracle.log"
rc=$?
if (( ${rc} != 0 )); then
  echo "$(date +%Y-%m-%d.%H:%M:%S) Error occurred while attempting to enable Unified Auditing in the oracledb container: ${rc}"  >> "${SCRIPTS_DIR}/init_oracle.log"
fi

${SCRIPTS_DIR}/dbdaemon_proxy --cdb_name="$1" &
childPID=$!
echo "$(date +%Y-%m-%d.%H:%M:%S) Initializing database daemon proxy with PID $childPID"  >> "${SCRIPTS_DIR}/init_oracle.log"

get_sga_pga
echo "$(date +%Y-%m-%d.%H:%M:%S) Initializing CDB database with PGA ${pga} and SGA ${sga} version:${VERSION}"  >> "${SCRIPTS_DIR}/init_oracle.log"
${SCRIPTS_DIR}/init_oracle --pga="${pga}" --sga="${sga}" --cdb_name="$1" --db_domain="$2" --logtostderr=true
rc=$?
if (( ${rc} != 0 )); then
  echo "$(date +%Y-%m-%d.%H:%M:%S) Error initializing CDB database: ${rc}"  >> "${SCRIPTS_DIR}/init_oracle.log"
fi
echo "$(date +%Y-%m-%d.%H:%M:%S) Create CDB database done."  >> "${SCRIPTS_DIR}/init_oracle.log"
wait $childPID
