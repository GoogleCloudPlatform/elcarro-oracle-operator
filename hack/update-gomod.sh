#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

if [[ -n "${BUILD_WORKSPACE_DIRECTORY:-}" ]]; then # Running inside bazel
  echo "Updating go dependencies..." >&2
elif ! command -v bazel &>/dev/null; then
  echo "Install bazel at https://bazel.build" >&2
  exit 1
else
  (
    set -o xtrace
    bazel run //hack:update-gomod
  )
  exit 0
fi

go=$(realpath "$1")

cd "$BUILD_WORKSPACE_DIRECTORY"

export GO111MODULE=on
"$go" mod tidy -v

