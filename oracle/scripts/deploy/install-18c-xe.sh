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


set -o nounset
set -o errexit

CLUSTER_NAME=gkecluster
CDB_NAME=GCLOUD
ZONE=us-central1-a
readonly OPERATOR_DIR="${HOME}/oracleop"

function usage() {
  echo "------USAGE------
  This tool installs the El Carro Operator and provisions the following resources:
  * a Kubernetes cluster on GKE to host the operator and database containers
  * an Oracle 18c XE database image
  * an Oracle database (PDB) instance in the cluster

  install-18c-xe.sh --service_account SERVICE_ACCOUNT_[NAME|EMAIL] [--cdb_name DB_NAME] [MORE_OPTIONS]

  REQUIRED FLAGS
     -a, service_account
         GCP service account that should be used to create a cluster and by the El Carro Operator.

  OPTIONAL FLAGS
     -c, --cdb_name
        Name of the container database (CDB), the default value is 'GCLOUD'.
        This name should only contain uppercase letters and numbers.
     -k, --cluster_name
        Name of GKE cluster to be created, default value is 'gkecluster'
     -z, --gke_zone
        Zone to create a cluster in, the default value is 'us-central1-a'.
       "
    exit 1
}

function parse_options() {
  opts=$(getopt -o g:a:c:k:z: \
  --longoptions gcs_oracle_binaries_path:,service_account:,cdb_name:,cluster_name:,gke_zone: -n "$(basename "$0")" -- "$@")
  eval set -- "$opts"
  while true; do
    case "$1" in
    -a | --service_account)
      shift
      GKE_SA=$1
      shift
      ;;
    -c | --cdb_name)
      shift
      CDB_NAME=$(echo "$1" | tr '[:lower:]' '[:upper:]')
      shift
      ;;
    -k | --cluster_name)
      shift
      CLUSTER_NAME=$1
      shift
      ;;
    -z | --gke_zone)
      shift
      ZONE=$(echo "$1" | tr '[:upper:]' '[:lower:]')
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

  if [[ -z ${GKE_SA+x} ]]; then
    usage
  fi
}

function init_env() {
  PROJECT=$(gcloud config get-value project 2>/dev/null)

  if [ -z "${PROJECT}" ]; then
    echo "could not determine current gcloud project"
    exit 1
  fi

  echo "current project: ${PROJECT}"

  readonly DB_IMAGE="gcr.io/$(echo "${PROJECT}" | tr : /)/oracle-database-images/oracle-18c-xe-seeded-$(echo "$CDB_NAME" | tr '[:upper:]' '[:lower:]')"
  readonly RELEASE_DIR="$(dirname "$0/")/.."
}

function enable_apis() {
  echo "enabling container.googleapis.com"
  gcloud services enable container.googleapis.com

  echo "enabling anthos.googleapis.com"
  gcloud services enable anthos.googleapis.com

  echo "enabling cloudbuild.googleapis.com"
  gcloud services enable cloudbuild.googleapis.com
}

function create_cluster() {
  if [ -z "$(gcloud beta container clusters list --filter "name=${CLUSTER_NAME} zone=${ZONE}")" ]; then

    local GKE_SA_EMAIL
    if [[ $GKE_SA = *@* ]]; then
      GKE_SA_EMAIL=${GKE_SA}
    else
      GKE_SA_EMAIL=$(gcloud iam service-accounts list --format='value(email)' --filter="name:${GKE_SA}@")
      if [ -z "${GKE_SA_EMAIL}" ]; then
        echo "Unknown account $GKE_SA"
        exit 1
      fi
    fi

    echo "adding monitoring and logging permissions to ${GKE_SA_EMAIL}"
    gcloud projects add-iam-policy-binding "${PROJECT}" \
    --member serviceAccount:${GKE_SA_EMAIL} \
    --role roles/monitoring.metricWriter
    gcloud projects add-iam-policy-binding "${PROJECT}" \
    --member serviceAccount:${GKE_SA_EMAIL} \
    --role roles/monitoring.viewer
    gcloud projects add-iam-policy-binding "${PROJECT}" \
    --member serviceAccount:${GKE_SA_EMAIL} \
    --role roles/logging.logWriter

    readonly GCR_GCS_PATH=$(gsutil ls | grep -E '^gs://artifacts.*appspot.com/$')
    echo "adding project container repository bucket ${GCR_GCS_PATH} read permission to ${GKE_SA_EMAIL}"
    gsutil iam ch serviceAccount:${GKE_SA_EMAIL}:roles/storage.objectViewer "${GCR_GCS_PATH}"

    gcloud beta container clusters create ${CLUSTER_NAME} --release-channel rapid \
    --machine-type=n1-standard-2 --num-nodes 2 --zone ${ZONE} \
    --scopes gke-default,compute-rw,cloud-platform,https://www.googleapis.com/auth/dataaccessauditlogging \
    --service-account "${GKE_SA_EMAIL}" \
    --image-type cos_containerd \
    --addons GcePersistentDiskCsiDriver
  else
    echo "cluster (name=${CLUSTER_NAME} zone=${ZONE}) already exists"

    if [ "$(kubectl config current-context)" != "gke_${PROJECT}_${ZONE}_${CLUSTER_NAME}" ]; then
      echo "current kubectl config is" "$(kubectl config current-context), please run:"
      echo "> gcloud container clusters get-credentials ${CLUSTER_NAME} --zone ${ZONE}"
      echo "> kubectl config set-context gke_${PROJECT}_${ZONE}_${CLUSTER_NAME}"
      exit 1
    fi
  fi
}

function install_csi_resources() {
  kubectl apply -f "${RELEASE_DIR}/deploy/csi"
}

function build_image() {
  echo "using Oracle database image: ${DB_IMAGE}"

  if [ -z "$(gcloud container images list-tags "${DB_IMAGE}" --filter="tags:latest" 2> /dev/null)" ]; then
    pushd "${RELEASE_DIR}/dbimage" > /dev/null
    bash image_build.sh --db_version=18c --create_cdb=true --cdb_name="${CDB_NAME}" --no_dry_run
    popd > /dev/null
  else
    echo "Oracle container image ${DB_IMAGE}:latest already exists"
  fi
}

function install_operator_resources() {
  kubectl apply -f "${RELEASE_DIR}/operator.yaml"
}

function create_demo_resources() {
  local -r CRD_DIR="${OPERATOR_DIR}/resources"
  local -r CRD_NS=db

  kubectl create ns ${CRD_NS} || kubectl get ns ${CRD_NS}
  mkdir -p "${CRD_DIR}"

  local -r CRD_INSTANCE_PATH="${CRD_DIR}/instance.yaml"
  local -r CRD_PDB_PATH="${CRD_DIR}/database_pdb1.yaml"

  cat "${RELEASE_DIR}/samples/v1alpha1_instance_18c_XE_express.yaml" | \
  sed "s|gcr.io/\${PROJECT_ID}/oracle-database-images/oracle-18c-xe-seeded-\${DB}|${DB_IMAGE}|g" | \
  sed "s/\${DB}/${CDB_NAME}/g" > "${CRD_INSTANCE_PATH}"

  cp "${RELEASE_DIR}/samples/v1alpha1_database_pdb1_express.yaml" "${CRD_PDB_PATH}"

  kubectl apply -f "${CRD_INSTANCE_PATH}" -n ${CRD_NS}
  kubectl apply -f "${CRD_PDB_PATH}" -n ${CRD_NS}
}

function wait_for_resource_creation() {
  local -r SLEEP=30
  local ITERATIONS=60
  local reason
  local db_reason
  local pdb_reason

  until [ $ITERATIONS -eq 0 ]; do
    reason=$(kubectl get instances -n db -o jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].reason}')
    db_reason=$(kubectl get instances -n db -o jsonpath='{.items[0].status.conditions[?(@.type=="DatabaseInstanceReady")].reason}')
    pdb_reason=$(kubectl get database pdb1 -n db -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}')

    echo "Waiting for startup, statuses:" "InstanceReady=$reason, InstanceDatabaseReady=$db_reason, DatabaseReady=$pdb_reason"

    if [ "$pdb_reason" = "CreateComplete" ]; then
      break
    fi

    sleep $SLEEP
    ITERATIONS=$(( $ITERATIONS-1 ))
  done

  if [ $ITERATIONS -eq 0 ] ; then
    echo "Timed out waiting for Instance to start up"
    exit 1
  fi
}

function print_connect_string() {
  local -r db_domain=$(kubectl get instances -n db -o jsonpath='{.items[0].spec.dbDomain}')
  local -r url=$(kubectl get instances -n db -o jsonpath='{.items[0].status.url}')

  echo "Oracle Operator is installed. Database connection command:"
  echo "> sqlplus scott/tiger@${url}/pdb1.${db_domain}"
}

parse_options "$@"
init_env

enable_apis
build_image
create_cluster
install_csi_resources
install_operator_resources
create_demo_resources
wait_for_resource_creation
print_connect_string
