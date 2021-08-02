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

# Name:
#	redeploy.sh
# Usage:
#	redeploy.sh <fully qualified cluster name> [<project ID>]
#
#	Example: redeploy.sh gke_${USER}-playground-operator_us-central1-a_cluster4 ${USER}-playground-operator
#
#	If the project in the (2nd) parameter is not passed or empty, PROW_PROJECT is
#	used.
#
#	If a namespace in the (3rd) parameter is not passed or empty, DEV_NS is
#	used. If DEV_NS is not set then "db" is the default namespace.
#
#	If noinstall is provided as a (4th) parameter, only the cleanup part runs.
#	No CRD install, no Operator build/push, no deploy.
#	This option is useful for deploying from an official build by running:
#	kubectl apply -f operator.yaml
#
#	If an instance name in the (5th) parameter is not passed or empty, DEV_INSTNAME
#	is used. If DEV_INSTNAME is empty "mydb" is the default instance name.
#
# Function:
#	This script is meant to be used interactively during the dev cycle to
#	reset and redeploy Operator K8s resources, including rebuilding of the images
#	pushed to GCR.

usage="Usage: $(basename $0) <FQN of a user cluster> [<project ID> [<kubernetes namespace> [ <install|noinstall> [<instance name>]]]]"

CLUSTER="${1:-$PROW_CLUSTER}"
PROJECTID="${2:-$PROW_PROJECT}"
NS=${3:-${DEV_NS:-db}}

NOINSTALL=false
if [[ "${4}" == "noinstall" ]] ;then
	NOINSTALL=true
fi

INSTNAME=${5:-${DEV_INSTNAME:-mydb}}

kubectl config use-context ${CLUSTER:?"$usage"}
kubectl config current-context
echo "Deployment project: ${PROJECTID:?"$usage"}"
echo "Deployment namespace: ${NS:?"$usage"}"
echo "No install? ${NOINSTALL}"
echo "Instance name: ${INSTNAME:?"$usage"}"
echo "*** verify cluster context and the project before proceeding ***"
echo "Press any key to continue..."
read -n 1 input

# set -x
set -o pipefail

kubectl get all -n operator-system
kubectl delete deployment.apps/operator-controller-manager -n operator-system
kubectl delete service/operator-controller-manager-metrics-service -n operator-system
kubectl get all -n operator-system

kubectl delete deployment.apps/"${INSTNAME}"-agent-deployment -n $NS
kubectl delete service/"${INSTNAME}"-agent-svc -n $NS
kubectl delete service/"${INSTNAME}"-dbdaemon-svc -n $NS

kubectl get all -n $NS
kubectl get storageclasses,volumesnapshotclasses
kubectl get pv,pvc,sts -n $NS
gcloud compute disks list --project ${PROJECTID} --filter=name~${CLUSTER}.*pvc
for pvc in $(kubectl get pvc -n $NS -o jsonpath='{range .items[*]}{@.spec.volumeName}{"\n"}'); do gcloud compute disks list --project ${PROJECTID} |grep $pvc; done
kubectl delete sts "${INSTNAME}"-sts -n $NS
for i in $(seq 2 4); do kubectl patch pvc "${INSTNAME}"-pvc-u0${i}-"${INSTNAME}"-sts-0 -n $NS -p '{"metadata":{"finalizers": []}}' --type=merge; kubectl delete pvc "${INSTNAME}"-pvc-u0${i}-"${INSTNAME}"-sts-0 -n $NS; done
kubectl delete service/"${INSTNAME}"-svc -n $NS
kubectl delete service/"${INSTNAME}"-svc-node -n $NS
kubectl get pv,pvc,sts -n $NS
gcloud compute disks list --project ${PROJECTID} --filter=name~${CLUSTER}.*pvc
for pvc in $(kubectl get pvc -n $NS -o jsonpath='{range .items[*]}{@.spec.volumeName}{"\n"}'); do gcloud compute disks list --project ${PROJECTID} |grep $pvc; done
kubectl get all -n $NS

make uninstall

if [[ ${NOINSTALL} == true ]] ;then
	echo "No install option requested. Cleanup done. Exiting..."
	exit 0
fi

# Setup image targets for make.
export PROW_IMAGE_REPO=${PROW_IMAGE_REPO:-gcr.io/${PROJECTID}}
export PROW_PROJECT=${PROW_PROJECT:-${PROJECTID}}
export PROW_IMAGE_TAG=${PROW_IMAGE_TAG:-latest}
date; make deploy

kubectl get instances -n $NS
kubectl get databases -n $NS
kubectl get backups -n $NS
kubectl get configs -n $NS
kubectl get events --sort-by=.metadata.creationTimestamp -n operator-system

