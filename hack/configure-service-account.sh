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


set -o errexit

function usage() {
  echo "------USAGE------
  This tool configures El Carro service account in Google kubernetes Cluster.

  configure-service-account.sh --cluster_name CLUSTER_NAME --gke_zone GKE_ZONE [--service_account SERVICE_ACCOUNT_[NAME|EMAIL]] [--namespace NAMESPACE]

  REQUIRED FLAGS
     -k, --cluster_name
        Name of the GKE cluster to be configured.
     -z, --gke_zone
        Zone of the cluster, for example 'us-central1-a'.

  OPTIONAL FLAGS
     -n, --namespace
        k8s namespace where the El Carro instance will be deployed. Required if Workload Identity is enabled for the GKE cluster.
     -a, --service_account
        An existing GCP service account which will be used to bind with k8s service account. Required if Workload Identity is enabled for the GKE cluster.
       "
    exit 1
}

function parse_options() {
  opts=$(getopt -o k:z:n:a: \
  --longoptions cluster_name:,gke_zone:,namespace:,service_account: -n "$(basename "$0")" -- "$@")
  eval set -- "$opts"
  while true; do
    case "$1" in
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
    -a | --service_account)
      shift
      GCP_SA=$1
      shift
      ;;
    -n | --namespace)
      shift
      NAMESPACE=$1
      shift
      ;;
    --)
      shift
      break
      ;;
    *)
      echo "Invalid argument $1"
      usage
      exit 1
      ;;
    esac
  done

  if [[ -z "${CLUSTER_NAME}" || -z "${ZONE}" ]]; then
    usage
  fi
}

function init_env() {
  PROJECT=$(gcloud config get-value project 2>/dev/null)

  if [[ -z "${PROJECT}" ]]; then
    echo "could not determine current gcloud project"
    exit 1
  fi

  echo "current project: ${PROJECT}"
}

function sa_wi_enabled() {
  echo "Workload Identity is enabled for cluster ${CLUSTER_NAME}"
  if [[ -z "${GCP_SA}" || -z "${NAMESPACE}" ]]; then
    usage
  fi
  local GCP_SA_EMAIL
  if [[ "${GCP_SA}" == *@* ]]; then
    GCP_SA_EMAIL=${GCP_SA}
  else
    GCP_SA_EMAIL=$(gcloud iam service-accounts list --format='value(email)' --filter="name:${GCP_SA}@" --project="${PROJECT}")
    if [[ -z "${GCP_SA_EMAIL}" ]]; then
      echo "Unknown account ${GCP_SA}"
      exit 1
    fi
  fi
  echo "Found an existing GCP service account ${GCP_SA_EMAIL}"
  echo "Will bind Kubernetes service account \"${NAMESPACE}/default\" with GCP service account \"${GCP_SA_EMAIL}\""
  echo "Verify accounts before proceeding"
  read -r -p "Do you want to continue? [y/n] " response
  if ! [[ "$response" =~ ^([yY])$ ]]; then
    exit 1
  fi

  gcloud iam service-accounts add-iam-policy-binding --role roles/iam.workloadIdentityUser --member "serviceAccount:${PROJECT}.svc.id.goog[${NAMESPACE}/default]" "${GCP_SA_EMAIL}"
  kubectl annotate serviceaccount --namespace "${NAMESPACE}" default iam.gke.io/gcp-service-account="${GCP_SA_EMAIL}" --overwrite
  kubectl describe serviceaccount --namespace "${NAMESPACE}" default
  echo "El Carro in cluster \"${CLUSTER_NAME}\" namespace \"${NAMESPACE}\" will be authenticated as \"${GCP_SA_EMAIL}\" to access Google Cloud services"
}

function sa_wi_disabled() {
  echo "Workload Identity is not enabled for cluster ${CLUSTER_NAME}"
  echo "It is recommended to enable Workload Identity to access Google Cloud services"
  local GKE_SA
  GKE_SA=$(gcloud container clusters describe "${CLUSTER_NAME}" --format="value(nodeConfig.serviceAccount)" --zone="${ZONE}" --project="${PROJECT}")
  if [[ "${GKE_SA}" == "default" ]]; then
    GKE_SA="$(gcloud projects describe "${PROJECT}" --format="value(projectNumber)")-compute@developer.gserviceaccount.com"
    echo "${CLUSTER_NAME} used Compute Engine default service account: ${GKE_SA}"
  fi
  echo "El Carro in \"${CLUSTER_NAME}\" will be authenticated as \"${GKE_SA}\" to access Google Cloud services"
}

parse_options "$@"
init_env
WI_CONFIG=$(gcloud container clusters describe "${CLUSTER_NAME}" --format="value(workloadIdentityConfig.workloadPool)" --zone="${ZONE}" --project="${PROJECT}")
if [[ -z "${WI_CONFIG}" ]]; then
  sa_wi_disabled
else
  sa_wi_enabled
fi