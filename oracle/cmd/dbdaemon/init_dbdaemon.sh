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

echo "$(date +%Y-%m-%d.%H:%M:%S) Enabling Unified Auditing in the dbdaemon container..."  >> "${SCRIPTS_DIR}/init_dbdaemon.log"
make -C $ORACLE_HOME/rdbms/lib -f ins_rdbms.mk uniaud_on ioracle ORACLE_HOME="${ORACLE_HOME}" >> "${SCRIPTS_DIR}/init_dbdaemon.log"
rc=$?
if (( ${rc} != 0 )); then
  echo "$(date +%Y-%m-%d.%H:%M:%S) Error occurred while attempting to enable Unified Auditing in the dbdaemon container: ${rc}"  >> "${SCRIPTS_DIR}/init_dbdaemon.log"
fi

${SCRIPTS_DIR}/dbdaemon --cdb_name="$1"
