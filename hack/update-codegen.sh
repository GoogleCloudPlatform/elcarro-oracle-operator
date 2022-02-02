#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

if [[ -n "${BUILD_WORKSPACE_DIRECTORY:-}" ]]; then # Running inside bazel
  echo "Updating generated code..." >&2
elif ! command -v bazel &>/dev/null; then
  echo "Install bazel at https://bazel.build" >&2
  exit 1
else
  (
    set -o xtrace
    bazel run //hack:update-codegen
  )
  exit 0
fi

go=$(realpath "$1")
controllergen=$(realpath "$2")
protoc=$(realpath "$3")
protoc_gen_go=$(realpath "$4")
protoc_gen_go_grpc=$(realpath "$5")
mockgen=$(realpath "$6")
kustomize=$(realpath "$7")
wkt_path=$(realpath external/com_google_protobuf/src)
googleapis_path=$(realpath external/go_googleapis)
headerfile=$(realpath hack/boilerplate.go.txt)
PATH=$(dirname "$go"):$(dirname "$protoc_gen_go"):$(dirname "$protoc_gen_go_grpc"):$(dirname "$mockgen"):$PATH
export PATH

cd "$BUILD_WORKSPACE_DIRECTORY"

# List of directories using kubebuilder project structure.
kubebuilder_dirs=(
  ./oracle
)
# List of directories containing CRD objects.
kubebuilder_object_dirs=(
  ./common/api
)

export GO111MODULE=on
export CGO_ENABLED=0
GOROOT=$(dirname "$(dirname "$go")")
export GOROOT
controller_gen_version=$("$go" list -m sigs.k8s.io/controller-tools | awk '{print $2}')

# Generate .pb.go and _grpc.pb.go
find . -path './third_party' -prune -false -o -type f -name '*.proto' -exec \
  "$protoc" -I "$googleapis_path" -I "$wkt_path" -I . \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative {} +

for dir in "${kubebuilder_dirs[@]}"; do
  "$controllergen" paths="$dir/..." \
    object:headerFile="$headerfile" \
    crd output:crd:artifacts:config="$dir/config/crd/bases" \
    rbac:roleName=manager-role output:rbac:artifacts:config="$dir/config/rbac" \
    webhook output:webhook:artifacts:config="$dir/config/webhook"
  # controller-gen is built from `vendor` and losts its go module version info.
  # Patch the CRD yamls to add back the version.
  sed -i "s/controller-gen\\.kubebuilder\\.io\\/version: (unknown)/controller-gen.kubebuilder.io\\/version: $controller_gen_version/" "$dir/config/crd/bases"/*.yaml
done

for dir in "${kubebuilder_object_dirs[@]}"; do
  "$controllergen" paths="$dir/..." object:headerFile="$headerfile"
done

# TODO(yfcheng) Do we really need this file?
"$kustomize" build oracle/config/default > oracle/operator.yaml

# Run go generate
go generate ./...
