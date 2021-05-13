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


readonly ORACLE_12="12.2"
readonly ORACLE_19="19.3"
readonly ORACLE_18="18c"
readonly DUMMY_VALUE="-1"

DB_VERSION=''
EDITION='ee'
CREATE_CDB=false
CDB_NAME=''
CHARACTER_SET='AL32UTF8'
MEM_PCT=25
IMAGE_NAME_SUFFIX=''
INSTALL_PATH=''
NO_DRY_RUN=false
PROJECT_ID=''
LOCAL_BUILD=false
TAG=''

sanity_check_params() {

  if [[ "${CREATE_CDB}" == true ]]; then
    if [ -z "${CDB_NAME}" ]; then
      CDB_NAME="GCLOUD"
    fi
    db_name_len=`expr length "${CDB_NAME}"`
    if [[ "${db_name_len}" -le 0 || "${db_name_len}" -gt 8 ]]; then
      echo "CDB_NAME should be less than or equal to 8 characters"
      usage
    fi
  else
    db_name_len=`expr length "${CDB_NAME}"`
    if [[ "${db_name_len}" -gt 0 ]]; then
      echo "CDB_NAME is set but CREATE_CDB is not"
      usage
    fi
  fi

  if [[ -z "${DB_VERSION}" ]]; then
    echo "Version DB_VERSION parameter is required to create images"
    usage
  fi

  if [[ "${DB_VERSION}" != "${ORACLE_12}" && "${DB_VERSION}" != "${ORACLE_18}" && "${DB_VERSION}" != "${ORACLE_19}" ]]; then
    echo "${DB_VERSION} is not supported, the supported versions are ${ORACLE_12} and ${ORACLE_19}"
    usage
  fi

  if [ -z "${INSTALL_PATH}" ] && [ "${DB_VERSION}" != "${ORACLE_18}" ] && [ "${LOCAL_BUILD}" != true ]; then
    echo "GCS path containing Oracle installation files is not provided"
    usage
  fi

  if [[ "${MEM_PCT}" -le 0 || "${MEM_PCT}" -gt 100 ]]; then
    echo "MEM_PCT should be between 0 and 100"
    usage
  fi

  if [[ "${DB_VERSION}" = "${ORACLE_18}" ]]; then
    EDITION="xe"
  fi
}

usage() {
  echo "------USAGE------
  This tool allows you to build oracle database container images.
  You have the option of using the GCP cloud build script or performing a local build by setting the --local_build flag to true.
  Sanity checks are conducted on your inputs and safe defaults are used as necessary.

  image_build.sh --db_version [12.2, 19.3 or 18c] --create_cdb [true or false] --cdb_name [CDB_NAME] --install_path [INSTALL_PATH]

  REQUIRED FLAGS
     --install_path
       GCS path containing Oracle Database EE installation files.
       This flag is only required when using GCP Cloud Build.
       You do not need to specify this parameter for Oracle 18c XE.

     --db_version
       Version of the Oracle database.

     --create_cdb
       Specifies whether a CDB should be created. Must be set to 'true' if using Oracle 18c.

  OPTIONAL FLAGS
     --cdb_name
       Name of the CDB to create. Defaults to 'GCLOUD' if unspecified.

     --edition
       Edition of the Oracle database. ee is used if unspecified.
       This flag is not supported for Oracle 18c and will be ignored.

     --patch_version
       Version of the Oracle database PSU.
       If unspecified, 31312468 is used as the default value for 12.2 ,
       31281355 is used as the default value for 19.3 .
       This flag is not supported for Oracle 18c and will be ignored.

     --local_build
       if true, docker is used to build an image locally. If false or unspecified, Google Cloud Build is used to build the image.

     --project_id
       project_id GCP project to use for image build. If unspecified, your default gcloud project will be used.
       For local builds, this flag can be set to 'local-build'.

     --mem_pct
       Percentage of memory.
       This flag is not supported for Oracle 18c and will be ignored.

     --character_set
       Character set for the newly created CDB

     --tag
       Tag that should be applied to the image.
       If a tag is not specified, 'gcr.io/\$GCR_PROJECT_ID/oracle-database-images/oracle-\${DB_VERSION}-\${EDITION}-\${IMAGE_NAME_SUFFIX}:latest' is used.

     --no_dry_run
       Run command in full mode.  Will execute actions.
       "
    exit 1
}

function parse_arguments() {
  opts=$(getopt -o i:v:c:n:p:m:c:h \
    --longoptions install_path:,db_version:,edition:,create_cdb:,cdb_name:,mem_pct:,character_set:,help:,project_id:,patch_version:,local_build:,tag:,no_dry_run,help \
    -n "$(basename "$0")" -- "$@")
  eval set -- "$opts"
  while true; do
    case "$1" in
    -i | --install_path)
      shift
      INSTALL_PATH=$1
      shift
      ;;
    -v | --db_version)
      shift
      DB_VERSION=$1
      shift
      ;;
    -e | --edition)
      shift
      EDITION=$1
      shift
      ;;
    -c | --create_cdb)
      shift
      CREATE_CDB=$1
      shift
      ;;
    -n | --cdb_name)
      shift
      CDB_NAME=$1
      shift
      ;;
    -m | --mem_pct)
      shift
      MEM_PCT=$1
      shift
      ;;
    --character_set)
      shift
      CHARACTER_SET=$1
      shift
      ;;
    --patch_version)
      shift
      PATCH_VERSION=$1
      shift
      ;;
    --project_id)
      shift
      PROJECT_ID=$1
      shift
      ;;
    --local_build)
      shift
      LOCAL_BUILD=$1
      shift
      ;;
    -t | --tag)
      shift
      TAG=$1
      shift
      ;;
    -h | --help)
      usage
      return 1
      ;;
    --no_dry_run)
      NO_DRY_RUN=true
      shift
      ;;
    --)
      shift
      break
      ;;
    *)
      echo Invalid argument "$1"
      usage
      exit 1
      ;;
    esac
  done

}

execute_command() {
  IMAGE_NAME_SUFFIX=$(echo "$CDB_NAME" | tr '[:upper:]' '[:lower:]')
  if [ -z "${PROJECT_ID}" ]; then
    PROJECT_ID=$(gcloud config get-value project 2>/dev/null)
    echo "Project not specified, falling back on current gcloud default:"
    echo "$PROJECT_ID"
  fi

  if [ -z "${PATCH_VERSION}" ]; then
    PATCH_VERSION="${DUMMY_VALUE}"
  fi

  GCR_PROJECT_ID=$(echo "$PROJECT_ID" | tr : /)

  if [[ "${CREATE_CDB}" == true ]]; then
    IMAGE_NAME_SUFFIX="seeded-${IMAGE_NAME_SUFFIX}"
  else
    IMAGE_NAME_SUFFIX="unseeded"
    CDB_NAME="${DUMMY_VALUE}"
  fi

  if [ -z "${TAG}" ]; then
    TAG="gcr.io/${GCR_PROJECT_ID}/oracle-database-images/oracle-${DB_VERSION}-${EDITION}-${IMAGE_NAME_SUFFIX}:latest"
  fi

  if [ "${LOCAL_BUILD}" == true ]; then
    BUILD_CMD=$(echo sudo docker build --no-cache --build-arg=DB_VERSION=${DB_VERSION} --build-arg=CREATE_CDB=${CREATE_CDB} --build-arg=CDB_NAME=${CDB_NAME} --build-arg=CHARACTER_SET=${CHARACTER_SET} --build-arg=MEM_PCT=${MEM_PCT} --build-arg=EDITION=${EDITION} --build-arg=PATCH_VERSION=${PATCH_VERSION} --tag=$TAG .)
  else
    if [ "${DB_VERSION}" == "${ORACLE_18}" ]; then
      BUILD_CMD=$(echo gcloud builds submit --project=${PROJECT_ID} --config=cloudbuild-18c-xe.yaml --substitutions=_CDB_NAME="${CDB_NAME}",_CHARACTER_SET="${CHARACTER_SET}",_TAG="${TAG}")
    else
      BUILD_CMD=$(echo gcloud builds submit --project=${PROJECT_ID} --config=cloudbuild.yaml --substitutions=_INSTALL_PATH="${INSTALL_PATH}",_DB_VERSION="${DB_VERSION}",_EDITION="${EDITION}",_CREATE_CDB="${CREATE_CDB}",_CDB_NAME="${CDB_NAME}",_CHARACTER_SET="${CHARACTER_SET}",_MEM_PCT="${MEM_PCT}",_TAG="${TAG}",_PATCH_VERSION="${PATCH_VERSION}")
    fi
  fi

  if [[ "$NO_DRY_RUN" == true ]]; then
    echo "Executing the following command:"
    echo "$BUILD_CMD"
    ${BUILD_CMD}
  else
    echo "Dry run mode: the command would have executed as follows:"
    echo "$BUILD_CMD"
  fi
}

main() {
  parse_arguments "$@"
  sanity_check_params
  date
  time execute_command
}

main "$@"
