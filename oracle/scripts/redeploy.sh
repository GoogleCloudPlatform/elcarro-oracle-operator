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
# 	redeploy.sh
# Usage:
#	redeploy.sh <fully qualified cluster name> [<project ID>]
#
#	Example: redeploy.sh gke_${USER}-playground-operator_us-central1-a_cluster4 ${USER}-playground-operator
#
#	If the project is not provided as a (2nd) parameter, a standard SWE
#	project pattern is assumed.
#
#	If a namespace is not provided as a (3rd) parameter, ns defaults to db.
#	(can also be set via DEV_NS env variable).
#
#	If noinstall is provided as a (4th) parameter, only the cleanup part runs.
#	No CRD install, no Operator build/push, no deploy.
#	This option is useful for deploying from an official build by running:
#	kubectl apply -f operator.yaml
#
# Function:
#  This script is meant to be used interactively during the dev cycle to
#	 reset and redeploy Operator K8s resources, including rebuilding of the images
#	 pushed to GCR.

[[ "$#" -lt 1 ]] && { echo "Usage: $(basename $0) <FQN of a user cluster> [<project ID> [<kubernetes namespace> [ <install|noinstall> [<instance name>]]]]"; exit 1; }

CLUSTER="${1}"

if [[ ! -z "${2}" ]] ;then
        PROJECTID="${2}"
else
        PROJECTID="${USER}-playground-operator"
fi

if [[ ! -z "${3}" ]] ;then
        NS="${3}"
else
        # use DEV_NS env var or 'db' as default
        NS=${DEV_NS:-db}
fi

NOINSTALL=false
if [[ ! -z "${4}" && "${4}" == "noinstall" ]] ;then
	NOINSTALL=true
fi

if [[ ! -z "${5}" ]] ;then
        INSTNAME="${5}"
else
        # use DEV_INSTNAME env var or 'mydb' as default
        INSTNAME=${DEV_INSTNAME:-mydb}
fi

kubectl config use-context ${CLUSTER}
kubectl config current-context
echo "Deployment project: ${PROJECTID}"
echo "Deployment namespace: ${NS}"
echo "No install? ${NOINSTALL}"
echo "Instance name: ${INSTNAME}"
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
export PROW_IMAGE_REPO=gcr.io/${PROJECTID}
export PROW_PROJECT=${PROJECTID}
export PROW_IMAGE_TAG=latest
date; make deploy

kubectl get instances -n $NS
kubectl get databases -n $NS
kubectl get backups -n $NS
kubectl get configs -n $NS
kubectl get events --sort-by=.metadata.creationTimestamp -n operator-system

