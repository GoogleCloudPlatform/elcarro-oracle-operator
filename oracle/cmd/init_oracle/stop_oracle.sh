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


(( $# != 1 )) && { echo "Usage: $(basename "$0") <stop option> "; exit 1; }

OPT=${1}
if [[ "${OPT}" != "immediate" && "${OPT}" != "abort" && "${OPT}" != "force" ]]; then
  echo "wrong stop option: ${OPT}"
  exit 1
fi

ORACLE_SID=`grep ORACLE_SID= ~/.metadata | cut -d "=" -f2`
source /home/oracle/${ORACLE_SID}.env
if [[ "${OPT}" == "force" ]]; then
  echo "Killing all ${ORACLE_SID} processes..."
  ps -ef -u "oracle"|grep "${ORACLE_SID}"| grep -v grep|awk '{print $2}'|xargs kill -9
  exit $?
fi

echo "Shutting down the database (${OPT}) ..."
sqlplus / as sysdba<<EOF
  alter system checkpoint;
  shutdown "$OPT"
exit
EOF

DATA_MOUNT="/u02"
TNS_ADMIN_BASE="${DATA_MOUNT}/app/oracle/oraconfig/network"
  
echo "Stopping the listeners..."
for l in {"SECURE"}; do
  echo "listener ${l}..."
  export TNS_ADMIN="${TNS_ADMIN_BASE}/${l}"
  lsnrctl stop ${l}
done
