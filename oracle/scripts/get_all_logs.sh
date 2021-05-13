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


# Dump all the image logs for an instance.
# Call by passing in the operator namespace, db namespace, and target instance.
# e.g. get_all_logs.sh operator-system db mydb

OPERATOR_NS=${1:-operator-system}
DATABASE_NS=${2:-db}
DATABASE_TARGET=${3:-mydb}

echo "--output0: input parameters: Operator NS: ${OPERATOR_NS}, Instance NS: ${DATABASE_NS}, Instance: ${DATABASE_TARGET}"

echo "--output1: gcloud config list"
gcloud config list

echo "--output2: Database Container Images"
gcloud container images list

OPERATOR_POD=$(kubectl get pods -n ${OPERATOR_NS} -o=name)
echo "--output3: Operator Pod: ${OPERATOR_POD}"

echo "--output4: all resources in NS=${OPERATOR_NS}"
kubectl get all -n ${OPERATOR_NS}

echo "--output5-a: version: agent"
kubectl describe deployment ${DATABASE_TARGET}-agent-deployment  -n ${DATABASE_NS} | grep Image

echo "--output5-b: version: STS and Database Pod"
kubectl describe sts ${DATABASE_TARGET}-sts -n db | grep Image
kubectl describe pod ${DATABASE_TARGET}-sts-0 -n db | grep "Image:"

echo "--output5-c: version: Release CR"
kubectl get releases -n ${OPERATOR_NS}

echo "--output6: Operator log:"
kubectl logs -n $OPERATOR_NS $OPERATOR_POD -c manager

NCSA_CONTAINERS=(
  # instance
  config-agent
  oracle-monitoring
)

CSA_CONTAINERS=(
  oracledb
  dbdaemon
  alert-log-sidecar
  listener-log-sidecar
)

CSA_POD=$(kubectl get pod -l instance=${DATABASE_TARGET} -n ${DATABASE_NS} -o=name)
NCSA_POD=$(kubectl get pod -l deployment=${DATABASE_TARGET}-agent-deployment -n ${DATABASE_NS} -o=name)

for container in ${CSA_CONTAINERS[@]}; do
  echo "--output7-a: ${CSA_POD}:${container} logs"
  kubectl logs -n ${DATABASE_NS} ${CSA_POD} -c ${container}
done

for container in ${NCSA_CONTAINERS[@]}; do
  echo "--output7-b: ${NCSA_POD}:${container} logs"
  kubectl logs -n ${DATABASE_NS} ${NCSA_POD} -c ${container}
done

