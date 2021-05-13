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

# Delete the integration test cluster
# Use environment variables to get the name of the cluster/zone/project

set -o errexit
set -o nounset
set -o pipefail

[[ -z "$PROW_CLUSTER" ]] && { echo "PROW_CLUSTER envvar was not set. Did you try to test without make?" ; exit 1; }
[[ -z "$PROW_CLUSTER_ZONE" ]] && { echo "PROW_CLUSTER_ZONE envvar was not set. Did you try to test without make?" ; exit 1; }
[[ -z "$PROW_PROJECT" ]] && { echo "PROW_PROJECT envvar was not set. Did you try to test without make?" ; exit 1; }

echo "Deleting cluster '${PROW_CLUSTER}' (this may take a few minutes)..."

set -x #echo on
time gcloud beta container clusters delete --async -q "${PROW_CLUSTER}" --zone="${PROW_CLUSTER_ZONE}" --project="${PROW_PROJECT}"

# Delete service account
scripts/integration_test_cluster/delete_service_account.sh

set +x #echo off
