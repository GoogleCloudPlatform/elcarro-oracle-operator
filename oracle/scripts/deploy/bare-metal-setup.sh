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

# Exit the script instead of continuing when:
#
# -e -- a command fails
# -u -- an unset variable is dereferenced
# -o pipefail -- a command in a sequence of pipes fails

set -uo pipefail
PROJECT_ID=""
CLUSTER_NAME=""
WORKING_DIR="baremetal"
CWD=""

function usage() {
  echo "------USAGE------
  The script currently automates the following steps
  * bmctl binary installation
  * Cluster creation
  * Connect cluster and Pantheon UI
  * install trident CRDs and orchestrators

  bare-metal-setup.sh --project_id PROJECT_ID --cluster_name CLUSTER_NAME

  REQUIRED FLAGS
     -p, --project_id
        Name of the project where the GKE cluster will be created.
     -c, --cluster_name
        Name of GKE cluster to be created,
       "
  exit 1
}

function parse_options() {
  opts=$(getopt -o p:c: \
  --longoptions project_id:,cluster_name: -n "$(basename "$0")" -- "$@")
  eval set -- "$opts"
  while true; do
    case "$1" in
    -p | --project_id)
      shift
      PROJECT_ID=$(echo "$1" | tr '[:lower:]' '[:upper:]')
      shift
      ;;
    -c | --cluster_name)
      shift
      CLUSTER_NAME=$1
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

sanity_check_params() {
  if [[ -z "${PROJECT_ID}" ]]; then
    echo "PROJECT_ID parameter is required to create a bare-metal cluster."
    usage
  fi
  if [[ -z "${CLUSTER_NAME}" ]]; then
    echo "CLUSTER_NAME parameter is required to create a bare-metal cluster."
    usage
  fi
}

install_dependencies() {
  echo "Installing docker and dependent packages"
  cd $CWD
  sudo apt install containerd
  sudo apt install docker.io
  sudo groupadd docker
  sudo usermod -aG docker $USER
  #newgrp docker
  sudo systemctl restart docker.service
}

install_bmctl() {
  echo "Downloading bmctl binaries"
  mkdir -p ~/$WORKING_DIR
  mkdir -p ~/$WORKING_DIR/.sa-keys
  cd ~/$WORKING_DIR
  CWD=$(pwd)
  gsutil cp gs://anthos-baremetal-release/bmctl/1.7.1/linux-amd64/bmctl bmctl
  chmod a+x bmctl
}

init() {
  cd ~/$WORKING_DIR
  CWD=$(pwd)
}

create_service_accounts() {
  echo "Creating required required service accounts"
  cd $CWD
  gcloud iam service-accounts create ${CLUSTER_NAME}-gcr
  gcloud iam service-accounts create ${CLUSTER_NAME}-connect
  gcloud iam service-accounts create ${CLUSTER_NAME}-register
  gcloud iam service-accounts create ${CLUSTER_NAME}-cloud-ops
  gcloud projects add-iam-policy-binding ${PROJECT_ID} --member=serviceAccount:${CLUSTER_NAME}-gcr@${PROJECT_ID}.iam.gserviceaccount.com --role=roles/owner
  gcloud projects add-iam-policy-binding ${PROJECT_ID} --member=serviceAccount:${CLUSTER_NAME}-connect@${PROJECT_ID}.iam.gserviceaccount.com --role=roles/owner
  gcloud projects add-iam-policy-binding ${PROJECT_ID} --member=serviceAccount:${CLUSTER_NAME}-register@${PROJECT_ID}.iam.gserviceaccount.com --role=roles/owner
  gcloud projects add-iam-policy-binding ${PROJECT_ID} --member=serviceAccount:${CLUSTER_NAME}-cloud-ops@${PROJECT_ID}.iam.gserviceaccount.com --role=roles/owner
  gcloud iam service-accounts keys create ${CWD}/.sa-keys/${CLUSTER_NAME}-gcr.json --iam-account=${CLUSTER_NAME}-gcr@${PROJECT_ID}.iam.gserviceaccount.com
  gcloud iam service-accounts keys create ${CWD}/.sa-keys/${CLUSTER_NAME}-connect.json --iam-account=${CLUSTER_NAME}-connect@${PROJECT_ID}.iam.gserviceaccount.com
  gcloud iam service-accounts keys create ${CWD}/.sa-keys/${CLUSTER_NAME}-register.json --iam-account=${CLUSTER_NAME}-register@${PROJECT_ID}.iam.gserviceaccount.com
  gcloud iam service-accounts keys create ${CWD}/.sa-keys/${CLUSTER_NAME}-cloud-ops.json --iam-account=${CLUSTER_NAME}-cloud-ops@${PROJECT_ID}.iam.gserviceaccount.com
}

create_cluster() {
  echo "Creating bare metal cluster"
  cd $CWD
  ./bmctl create config -c $CLUSTER_NAME
  echo "Cluster creation config template is created at bmctl-workspace/$CLUSTER_NAME/$CLUSTER_NAME.yaml"
  echo "Modify the file with the required args and press any key to continue"
  read continue
  ./bmctl create cluster -c ${CLUSTER_NAME} --force
  if [ $? -eq 0 ]; then
    echo "Successfully created cluster"
  else
    echo "Error while creating cluster, resetting cluster"
    ./bmctl reset cluster -c ${CLUSTER_NAME}
  fi
}

connect_cluster_and_ui() {
  echo "Setting up connection between UI and cluster"
  export KUBECONFIG="$CWD/bmctl-workspace/$CLUSTER_NAME/$CLUSTER_NAME-kubeconfig"
  cd $CWD
  KSA_NAME="${CLUSTER_NAME}-sa"
  echo "KSA_name is $KSA_NAME"
  kubectl create serviceaccount ${KSA_NAME}
  echo "Created the following service account: $KSA_NAME"
  kubectl create clusterrolebinding ${KSA_NAME}-cluster-admin --clusterrole cluster-admin --serviceaccount default:${KSA_NAME}
  SECRET_NAME=$(kubectl get serviceaccount ${KSA_NAME} -o jsonpath='{$.secrets[0].name}')
  SECRET_CONTENT=$(kubectl get secret ${SECRET_NAME} -o jsonpath='{$.data.token}' | base64 -d)
  echo $SECRET_CONTENT
  echo 'Paste the above secret after selecting token in "Log in to cluster" section of the UI and press any key to continue'
  read continue
}

install_trident() {
  echo "Installing trident storage drivers"
  export KUBECONFIG="$CWD/bmctl-workspace/$CLUSTER_NAME/$CLUSTER_NAME-kubeconfig"
  cd $CWD
  wget https://github.com/NetApp/trident/releases/download/v21.04.0/trident-installer-21.04.0.tar.gz
  tar -xf trident-installer-21.04.0.tar.gz
  cd trident-installer
  echo "Uninstalling existing trident drivers"
  kubectl create -f deploy/crds/trident.netapp.io_tridentorchestrators_crd_post1.16.yaml
  if [ $? -eq 0 ]; then
    echo "Successfully installed trident drivers"
  else
    echo "Error while installing trident drivers"
  fi

  kubectl apply -f deploy/namespace.yaml
  kubectl create -f deploy/bundle.yaml
  #By default, your cluster will not schedule Pods on the control-plane node for security reasons, thereby we run the following untaint command
  kubectl taint nodes --all node-role.kubernetes.io/master-

  kubectl get deployment -n trident -owide
  kubectl get pods -n trident -owide

  kubectl create -f deploy/crds/tridentorchestrator_cr.yaml
  kubectl describe torc trident
  kubectl get pods -n trident -owide
  ./tridentctl -n trident version
}

uninstall_trident() {
  echo "Installing trident storage drivers"
  export KUBECONFIG="$CWD/bmctl-workspace/$CLUSTER_NAME/$CLUSTER_NAME-kubeconfig"
  cd $CWD/trident-installer
  echo "Uninstalling existing trident drivers"
  kubectl delete -f deploy/crds/tridentorchestrator_cr.yaml
  kubectl delete -f deploy/bundle.yaml
  kubectl delete -f deploy/namespace.yaml
  kubectl delete -f deploy/crds/trident.netapp.io_tridentorchestrators_crd_post1.16.yaml
}

main() {
  parse_options "$@"
  sanity_check_params
  install_dependencies
  install_bmctl
  create_service_accounts
  create_cluster
  connect_cluster_and_ui
  install_trident
}

main "$@"
