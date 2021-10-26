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

# Remove stale integration test clusters from the PROW_PROJECT

set -o errexit
set -o nounset
set -o pipefail

[[ -z "$PROW_CLUSTER_ZONE" ]] && { echo "PROW_CLUSTER_ZONE envvar was not set. Did you try to test without make?" ; exit 1; }
[[ -z "$PROW_PROJECT" ]] && { echo "PROW_PROJECT envvar was not set. Did you try to test without make?" ; exit 1; }

# -6 hours from now
STALE_TIME="-P6H"

# Look for clusters inttests-XXX created more than STALE_TIME hours ago
STALE_CLUSTERS=($(gcloud beta container clusters list --project "${PROW_PROJECT}" \
--filter "name:inttests- AND createTime<${STALE_TIME}" --format="value(name)"))

for c in "${STALE_CLUSTERS[@]}"; do
  echo " * Deleting stale cluster * ${c}";
  set -x #echo on
  gcloud beta container clusters delete --async -q "${c}" --zone="${PROW_CLUSTER_ZONE}" --project="${PROW_PROJECT}"
  set +x #echo off
done

# Look for PVCs created more than STALE_TIME hours ago
STALE_PVCS=($(gcloud compute disks list --project ${PROW_PROJECT} \
--filter "creationTimestamp<${STALE_TIME} AND users=null" --format="value(name)"))

for c in "${STALE_PVCS[@]}"; do
  echo " * Deleting orphan pvc * ${c}";
  set -x #echo on
  # Ignore errors as there might be concurrent jobs running
  gcloud compute disks delete -q "${c}" --zone="${PROW_CLUSTER_ZONE}" --project="${PROW_PROJECT}" || true
  set +x #echo off
done

STALE_FORWARDING_RULES=($(gcloud compute forwarding-rules list --project ${PROW_PROJECT} --format="value(selfLink)" --filter "creationTimestamp<${STALE_TIME} AND description:-test-"))
for fr in "${STALE_FORWARDING_RULES[@]}"; do
  echo " * Deleting stale forwarding rule * ${fr}";
  set -x #echo on
  # Ignore errors as there might be concurrent jobs running
  gcloud compute forwarding-rules delete -q "${fr}" --project="${PROW_PROJECT}" || true
  set +x #echo off
done

STALE_TARGET_POOLS=($(gcloud compute target-pools list --project ${PROW_PROJECT} --format="value(selfLink)" --filter "creationTimestamp<${STALE_TIME} AND description:-test-"))
for tp in "${STALE_TARGET_POOLS[@]}"; do
  echo " * Deleting stale target pool * ${tp}";
  set -x #echo on
  # Ignore errors as there might be concurrent jobs running
  # gcloud will not delete target pools that are being referenced by forwarding rules
  gcloud compute target-pools delete -q "${tp}" --project="${PROW_PROJECT}" || true
  set +x #echo off
done

# Look for Firewall rules created more than STALE_TIME hours ago
STALE_FIREWALL_RULES=($(gcloud compute firewall-rules list --project "${PROW_PROJECT}" \
--filter "creationTimestamp<${STALE_TIME} AND targetTags.list(show=\"keys\"):gke-inttests*" --format="value(name)"))

for c in "${STALE_FIREWALL_RULES[@]}"; do
  echo " * Deleting firewall rule * ${c}";
  set -x #echo on
  # Ignore errors as there might be concurrent jobs running
  gcloud compute firewall-rules delete -q "${c}" --project="${PROW_PROJECT}" || true
  set +x #echo off
done

# Cleanup GCS bucket bindings for deleted service accounts
STALE_GCS_SA=($(gsutil iam get gs://"${PROW_PROJECT}" | jq -r ".bindings[].members[]|select(startswith(\"deleted\"))"))

for c in "${STALE_GCS_SA[@]}"; do
  echo " * Deleting GCS binding ${c}";
  set -x #echo on
  # Ignore errors as there might be concurrent jobs running
  gsutil iam ch -d "${c}" gs://"${PROW_PROJECT}" || true
  set +x #echo off
done
