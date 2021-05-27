#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

if [[ -n "${BUILD_WORKSPACE_DIRECTORY:-}" ]]; then # Running inside bazel
  echo "Formatting source files..." >&2
elif ! command -v bazel &>/dev/null; then
  echo "Install bazel at https://bazel.build" >&2
  exit 1
else
  (
    set -o xtrace
    bazel run //hack:update-fmt
  )
  exit 0
fi

gofmt=$(realpath "$1")
clang_format=$(realpath "$2")

cd "$BUILD_WORKSPACE_DIRECTORY"

export GO111MODULE=on
find . -path './third_party' -prune -false -o -type f -name '*.go' -exec "$gofmt" -l -s -w {} +
find . -path './third_party' -prune -false -o -type f -name '*.proto' -exec "${clang_format}" --style google -i {} +
