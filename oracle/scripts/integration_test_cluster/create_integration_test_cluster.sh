#!/usr/bin/env bash
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

# Create a new GKE integration test cluster
# Use environment variables to get the name of the cluster/zone/project

set -o errexit
set -o nounset
set -o pipefail

[[ -z "$PROW_CLUSTER" ]] && { echo "PROW_CLUSTER envvar was not set. Did you try to test without make?" ; exit 1; }
[[ -z "$PROW_CLUSTER_ZONE" ]] && { echo "PROW_CLUSTER_ZONE envvar was not set. Did you try to test without make?" ; exit 1; }
[[ -z "$PROW_PROJECT" ]] && { echo "PROW_PROJECT envvar was not set. Did you try to test without make?" ; exit 1; }
[[ -z "$INT_TEST_CLUSTER_NODE_COUNT" ]] && { echo "INT_TEST_CLUSTER_NODE_COUNT envvar was not set. Did you try to test without make?" ; exit 1; }

MACHINE="n1-standard-4"

echo "Creating cluster '${PROW_CLUSTER}' (this may take a few minutes)..."
echo "If this fails due to insufficient project quota, request more quota at GCP console"
echo

set -x #echo on
time gcloud beta container clusters create "${PROW_CLUSTER}" \
--release-channel rapid \
--machine-type="${MACHINE}" \
--num-nodes="${INT_TEST_CLUSTER_NODE_COUNT}" \
--zone="${PROW_CLUSTER_ZONE}" \
--project="${PROW_PROJECT}" \
--scopes "gke-default,compute-rw,cloud-platform,https://www.googleapis.com/auth/dataaccessauditlogging" \
--enable-gcfs \
--workload-pool="${PROW_PROJECT}.svc.id.goog"

gcloud container clusters get-credentials ${PROW_CLUSTER} --zone ${PROW_CLUSTER_ZONE} --project ${PROW_PROJECT}
kubectl config set-context gke_${PROW_PROJECT}_${PROW_CLUSTER_ZONE}_${PROW_CLUSTER}

# Create the csi-gce-pd storage class and the csi-gce-pd-snapshot-class volume snapshot class
kubectl create -f scripts/deploy/csi/gce_pd_storage_class.yaml
kubectl create -f scripts/deploy/csi/gce_pd_volume_snapshot_class.yaml

# Create service account for this k8s cluster
scripts/integration_test_cluster/create_service_account.sh

set +x #echo off