#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

if [[ -n "${BUILD_WORKSPACE_DIRECTORY:-}" ]]; then # Running inside bazel
  echo "Updating bazel rules..." >&2
elif ! command -v bazel &>/dev/null; then
  echo "Install bazel at https://bazel.build" >&2
  exit 1
else
  (
    set -o xtrace
    bazel run //hack:update-bazel
  )
  exit 0
fi

cd "$BUILD_WORKSPACE_DIRECTORY"
set -x

bazel run //:gazelle-fix
bazel run //:gazelle-update-repos
bazel run //:kazel -- -root "$BUILD_WORKSPACE_DIRECTORY"