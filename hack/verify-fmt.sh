#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

if [[ -n "${TEST_WORKSPACE:-}" ]]; then # Running inside bazel
  echo "Validating source file formatting..." >&2
elif ! command -v bazel &> /dev/null; then
  echo "Install bazel at https://bazel.build" >&2
  exit 1
else
  (
    set -o xtrace
    bazel test --test_output=streamed //hack:verify-fmt
  )
  exit 0
fi

gofmt=$(realpath "$1")
clang_format=$(realpath "$2")

export GO111MODULE=on
go_output=$(find . -path './third_party' -prune -false -o -name '*.go' -exec "$gofmt" -s -d {} +)
proto_output=$(find . -path './third_party' -prune -false -o -name '*.proto' -exec "${clang_format}" --style google -n {} + 2>&1)
if [[ -n "${go_output}${proto_output}" ]]; then
  echo "${go_output}"
  echo "${proto_output}"
  echo "Please run './hack/update-fmt.sh'" >&2
  exit 1
fi
