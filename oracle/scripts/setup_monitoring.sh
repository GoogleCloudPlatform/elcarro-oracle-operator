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


function apply_mainfests {
  if [ "$(ls -A manifests/setup)" ]; then
    kubectl apply -f manifests/setup
  fi

  kubectl apply -f manifests/
  if [ $? -ne 0 ]; then
    echo "failed to update prometheus operator to access all namespaces "
    exit 1
  fi
}

function help_msg {
  echo "Please run the script either as $0 install or $0 uninstall"
  exit 1
}

function envcheck {
  if [[ -z "${PATH_TO_RELEASE}" ]]; then
    PATH_TO_RELEASE=${PWD}
  fi
  if [[ -z "${GOPATH}" ]]; then
    echo "GOPATH environment variable is not set. Please rerun after setting GOPATH."
    exit 1
  fi
  export PATH="${GOPATH}/bin":$PATH
}

function install {
  # Installing pre-requisites
  GO111MODULE="on" go get github.com/jsonnet-bundler/jsonnet-bundler/cmd/jb && echo "jb CMD installed" || { echo "jb CMD install failed."; exit 1; }

  go get github.com/brancz/gojsontoyaml && echo "jsontoyaml CMD installed" || { echo "jsontoyaml CMD install failed."; exit 1; }

  go get github.com/google/go-jsonnet/cmd/jsonnet && echo "jsonnet CMD installed" || { echo "jsonnet CMD install failed."; exit 1; }

  git clone -b release-0.7 https://github.com/prometheus-operator/kube-prometheus && echo " kube-prometheus installed" || { echo "kube-prometheus install failed."; exit 1; }

  # Copy dashboards to kube-prometheus
  cp ${PATH_TO_RELEASE}/dashboards/db-dashboard.json kube-prometheus/db-dashboard.json
  if [ $? -ne 0 ]; then
    echo "dashboards not found"
    exit 1
  fi
  cp ${PATH_TO_RELEASE}/dashboards/install-dashboards.jsonnet kube-prometheus/install-dashboards.jsonnet
  if [ $? -ne 0 ]; then
    echo "dashboards installer not found"
    exit 1
  fi

  cd kube-prometheus

  # Setup the kube-prometheus
  kubectl create -f manifests/setup
  until kubectl get servicemonitors --all-namespaces ; do date; sleep 1; echo ""; done
  kubectl create -f manifests/

  # Modify the prometheus operator to allow access to all namespaces.
  # Prometheus runs in monitoring namespace
  jb update || { echo "failed to update jsonnet config"; exit 1; }

  ${PWD}/build.sh install-dashboards.jsonnet
  apply_mainfests
}

function uninstall {
  git clone -b release-0.7 https://github.com/prometheus-operator/kube-prometheus && echo " kube-prometheus staged" || { echo "kube-prometheus staging failed."; exit 1; }
  cd kube-prometheus
  kubectl delete --ignore-not-found=true -f manifests/ -f manifests/setup
}


if [ $# -eq 1 ]; then
  envcheck
  case $1 in
    "install")
      install
      ;;
    "uninstall")
      uninstall
      ;;
    *)
      echo "Unrecognized parameter $1."
      help_msg
      ;;
  esac
else
  help_msg
fi
