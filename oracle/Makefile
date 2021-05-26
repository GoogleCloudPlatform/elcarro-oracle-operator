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

# Image URL to use all building/pushing image targets
# Override these with environment variables or via command line
# To push the operator to autopush manually for example,
# make buildah-build-operator buildah-push-operator PROW_IMAGE_REPO=gcr.io/<GCR_REPO> PROW_IMAGE_TAG=latest
SHELL := /bin/bash

# Use the bazel version, important not to use quotes so that the command is expanded.
bazel_noui=--ui_event_filters=-INFO --noshow_loading_progress --show_result=0
KUSTOMIZE=bazel run --run_under="cd $$PWD &&" $(bazel_noui) -- //:kustomize
CONTROLLER_GEN=bazel run --run_under="cd $$PWD &&" $(bazel_noui) -- //:controller-gen
PROTOC=bazel run --run_under="cd $$PWD/.. &&" $(bazel_noui) -- //:protoc
GAZELLE=bazel run $(bazel_noui) -- //:gazelle

export PROW_IMAGE_REPO ?= $(shell\
	if [ -n "${PROW_JOB_ID}" ]; then \
		echo "gcr.io/prow-build-graybox"; \
	else echo "gcr.io/${USER}-playground-operator"; \
	fi \
)
# Prefer Louhi refSha, then check for general prow, finally assume dev.
export PROW_IMAGE_TAG ?= $(shell\
	if [ -n "${refSha}" ]; then \
		echo "$${refSha:0:8}"; \
	elif [ -n "${PROW_JOB_ID}" ]; then \
		echo "${PROW_JOB_ID}"; \
	else echo ${USER}-dev; \
	fi \
)
export PROW_PROJECT ?= $(shell\
	if [ -n "${PROW_JOB_ID}" ]; then \
		echo "prow-build-graybox"; \
	else echo "${USER}-playground-operator"; \
	fi \
)
export RELEASE_NAME ?= $(cat /workspace/louhi_ws/_release_name)
# Name of the cluster for integration tests
# If this is a PROW job create a 40-chars unique cluster name from PROW JOB ID
# e.g. inttests-2808d604-8644-11eb-bc7
# Otherwise use cluster4
export PROW_JOB_ID_PART=$(shell echo ${PROW_JOB_ID} | cut -c 1-22)
export PROW_CLUSTER ?= $(shell\
	if [ -n "${PROW_JOB_ID}" ]; then \
		echo "inttests-${PROW_JOB_ID_PART}"; \
	else echo "cluster4"; \
	fi \
)
export PROW_CLUSTER_ZONE ?= $(shell\
	if [ -n "${PROW_JOB_ID}" ]; then \
		echo "us-central1-a"; \
	else echo "us-central1-a"; \
	fi \
)

# Create unique service account name for integration tests
# Service account name can't be more than 30 chars.
# Create unique ID from the PROW_JOB_ID or use 'sa-local'
export PROW_INT_TEST_SA=$(shell\
	if [ -n "${PROW_JOB_ID}" ]; then \
		echo "sa-${PROW_JOB_ID_PART}"; \
	else echo "sa-local"; \
	fi \
)

env:
	@echo "export PROW_IMAGE_REPO=${PROW_IMAGE_REPO}"
	@echo "export PROW_IMAGE_TAG=${PROW_IMAGE_TAG}"
	@echo "export PROW_PROJECT=${PROW_PROJECT}"
	@echo "export PROW_CLUSTER=${PROW_CLUSTER}"
	@echo "export PROW_CLUSTER_ZONE=${PROW_CLUSTER_ZONE}"
	@echo "export PROW_INT_TEST_SA=${PROW_INT_TEST_SA}"

export _BUILDAH_STARTED_IN_USERNS
export BUILDAH_ISOLATION
# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)
# CRD_OPTIONS ?= "crd:trivialVersions=true"
CRD_OPTIONS ?= "crd"

# allow builds outside $GOPATH/src
export GO111MODULE=on

# Unit and functional tests
unit-test:
	bazel test --test_output=errors --test_tag_filters="-integration" ...

# Run test, if we are in prow and ARTIFACTS is set, copy junit xmls and test logs for upload.
test:
	rm -f failed # clear any old flags
	# Unit/Functional tests, unlimited jobs
	bazel test --test_output=errors --test_tag_filters="-integration" ... || touch failed
	# Load oracle image paths from env variables to .bazelrc
	envsubst < controllers/inttest/.bazelrc.tmpl > controllers/inttest/.bazelrc
	# Integration tests, build unlimited, but limit test jobs.
	bazel build --test_tag_filters="integration" ... || touch failed
	bazel \
		--bazelrc=controllers/inttest/.bazelrc \
		test \
 		--jobs=8 \
 		--test_tag_filters="integration" \
 		... \
		--test_output=errors \
		--spawn_strategy=local \
		--genrule_strategy=local \
		--test_env=PROW_IMAGE_REPO=${PROW_IMAGE_REPO} \
		--test_env=PROW_IMAGE_TAG=${PROW_IMAGE_TAG} \
		--test_env=PROW_PROJECT=${PROW_PROJECT} \
		--test_env=PROW_CLUSTER=${PROW_CLUSTER} \
		--test_env=PROW_INT_TEST_SA=${PROW_INT_TEST_SA} \
		--test_env=PROW_CLUSTER_ZONE=${PROW_CLUSTER_ZONE} || touch failed
	if [[ -n "$$ARTIFACTS" ]]; then find ../bazel-testlogs/ -name '*.xml' -o -name '*.log' | xargs cp --parents -t "$$ARTIFACTS/" ; fi
	if [[ -f "failed" ]]; then rm failed; exit 100; fi

# Run go fmt against code
fmt:
	scripts/fmt_fixup.sh

# Run go vet against code
vet:
	go vet ./...

glaze:
	$(GAZELLE) fix

glaze-deps:
	$(GAZELLE) update-repos -from_file=go.mod -to_macro=deps.bzl%go_dependencies
	go mod tidy # glaze dirties the go.sum file.

# Static checks to be run on presubmit.
# Not parallel to prevent output interleaving.
# We cannot generate protobufs in prow because we dont have equivalent compilers available.
check: generate-config generate-go
	# Cant use vet/fmt targets or ordering between generate and vet/fmt is unknown.
	go vet ./...
	$(MAKE) glaze-deps glaze
	scripts/fmt_fixup.sh
	scripts/fmt_check.sh
	# Check bazel builds
	bazel build ...

# Generate all code for building
generate-go: generate-proto
	$(CONTROLLER_GEN) object:headerFile=../hack/boilerplate.go.txt paths="../common/..."
	$(CONTROLLER_GEN) object:headerFile=../hack/boilerplate.go.txt paths="./..."

# Generate config manifests e.g. CRD, RBAC, controller etc. for k8s.
generate-config:
	$(CONTROLLER_GEN) $(CRD_OPTIONS) rbac:roleName=manager-role webhook paths="./..." output:rbac:artifacts:config=config/rbac output:webhook:artifacts:config=config/webhook output:crd:artifacts:config=config/crd/bases
	sed -i 's/controller-gen.kubebuilder.io\/version: (unknown)/controller-gen.kubebuilder.io\/version: v0.5.0/g' config/crd/bases/*.yaml
	cd config/manager && $(KUSTOMIZE) edit set image controller="gcr.io/elcarro/oracle.db.anthosapis.com/operator:latest"
	$(KUSTOMIZE) build config/default > operator.yaml

# Generate proto/grpc code
# Prerequisites: google-cloud-sdk
# execute:
# sudo apt install google-cloud-sdk
protoc-gen-go-target="@com_github_golang_protobuf//protoc-gen-go:protoc-gen-go"
protoc-gen-go="bazel-bin/external/com_github_golang_protobuf/protoc-gen-go/protoc-gen-go_/protoc-gen-go"
protoc-gen-go-grpc-target="@org_golang_google_grpc_cmd_protoc_gen_go_grpc//:protoc-gen-go-grpc"
protoc-gen-go-grpc="bazel-bin/external/org_golang_google_grpc_cmd_protoc_gen_go_grpc/protoc-gen-go-grpc_/protoc-gen-go-grpc"
protos := $(shell cd ../ && find ./ -name '*.proto')
generate-proto:
	# Make sure our tools are available.
	bazel build $(bazel_noui) $(protoc-gen-go-target) $(protoc-gen-go-grpc-target)
	$(PROTOC) \
		--plugin=$(protoc-gen-go) \
		--plugin=$(protoc-gen-go-grpc) \
		-I /usr/lib/google-cloud-sdk/lib/third_party \
		-I bazel-$(shell basename $$(readlink -f ../))/external/com_google_protobuf/src \
		-I . \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		$(protos)

buildah-push-operator:
	bazel run operator_image_push

buildah-push-dbinit:
	bazel run //oracle/build:dbinit_push

buildah-push-configagent:
	bazel run //oracle/build:configagent_push

buildah-push-dbdaemon-client:
	bazel run //oracle/build:dbdaemonclient_push

buildah-push-logging:
	bazel run //oracle/build:loggingsidecar_push

buildah-push-monitoring:
	bazel run //oracle/build:monitoring_push

# Build and push everything except the db image for integration tests.
buildah-push-all: buildah-push-operator buildah-push-dbinit buildah-push-configagent buildah-push-logging buildah-push-monitoring

# Install CRDs into a cluster
install: generate-config
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

# Uninstall CRDs from a cluster
uninstall: generate-config
	$(KUSTOMIZE) build config/crd | kubectl delete -f -

# Deploy controller in the configured Kubernetes cluster in ~/.kube/config
deploy: generate-config
	$(MAKE) buildah-push-all -j8
	cd config/manager && $(KUSTOMIZE) edit set image controller=${PROW_IMAGE_REPO}/oracle.db.anthosapis.com/operator:${PROW_IMAGE_TAG}
	$(KUSTOMIZE) build config/default | kubectl apply -f -

# Install all the required dependencies for prow.
prepare-prow:
	scripts/install_prow_deps.sh || (echo "*** Retrying install prow deps*** "; scripts/install_prow_deps.sh)
	touch prepare-prow

# Prow job entry point: operator-checks
operator-checks: prepare-prow
	$(MAKE) check

# Prow presubmit job entry point: operator-presubmit
operator-presubmit: prepare-prow
	$(MAKE) -j8 buildah-push-all
	# Remove stale clusters from the project
	scripts/integration_test_cluster/cleanup_integration_test_clusters.sh
	# Create a new GKE cluster for int tests
	scripts/integration_test_cluster/create_integration_test_cluster.sh
	# Run tests, remove temp cluster in case of failure
	$(MAKE) test || (scripts/integration_test_cluster/delete_integration_test_cluster.sh; exit 100)
	# Delete the GKE cluster
	scripts/integration_test_cluster/delete_integration_test_cluster.sh

# Prow canary test job entry point: operator-canary
operator-canary: prepare-prow
	$(MAKE) operator-presubmit

# Github presubmit Prow job entry point
operator-oss-presubmit: prepare-prow
	$(MAKE) check
	$(MAKE) unit-test

# Louhi test job entry point: louhi-operator-canary
louhi-operator-canary: prepare-prow
	# Remove stale clusters from the project
	scripts/integration_test_cluster/cleanup_integration_test_clusters.sh
	# Create a new GKE cluster for int tests (can also be flaky, retry this step once more)
	scripts/integration_test_cluster/create_integration_test_cluster.sh || (echo "*** Retrying cluster creation ***"; scripts/integration_test_cluster/create_integration_test_cluster.sh)
	# Run tests, remove temp cluster in case of failure
	$(MAKE) test || (scripts/integration_test_cluster/delete_integration_test_cluster.sh; exit 100)
	# Delete the GKE cluster
	scripts/integration_test_cluster/delete_integration_test_cluster.sh

# Louhi step to prepare for a release.
prepare-release:
	echo "Generating version"
	./scripts/generate_version.sh "main" > version.go
	echo "updating projects"
	sed -i "s/gcr.io\/elcarro/gcr.io\/${PROW_PROJECT}/g" operator.yaml
	sed -i "s/gcr.io\/elcarro/gcr.io\/${PROW_PROJECT}/g" main.go
	echo "updating image tags for operator.yaml..."
	sed -i "s/operator:latest/operator:${RELEASE_NAME}/g" operator.yaml
	echo "updating image tags for main.go..."
	sed -i "s/dbinit:latest/dbinit:${RELEASE_NAME}/g" main.go
	sed -i "s/configagent:latest/configagent:${RELEASE_NAME}/g" main.go
	sed -i "s/monitoring:latest/monitoring:${RELEASE_NAME}/g" main.go
	sed -i "s/loggingsidecar:latest/loggingsidecar:${RELEASE_NAME}/g" main.go


export EL_CARRO_RELEASE_ARTIFACTS_DIR ?= release-artifacts
copy-release-artifacts:
	mkdir -p ${EL_CARRO_RELEASE_ARTIFACTS_DIR}
	cp -R -t ${EL_CARRO_RELEASE_ARTIFACTS_DIR} build/dbimage \
		operator.yaml \
		ui.yaml \
		config/samples \
		config/workflows \
		scripts/get_all_logs.sh \
		scripts/setup_monitoring.sh \
		config/prometheus/db_monitor.yaml \
		../third_party/dashboards \
		dashboards \
		scripts/deploy
