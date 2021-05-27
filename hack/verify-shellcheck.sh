#!/bin/bash

set -o nounset
set -o errexit
set -o pipefail

if [[ -n "${TEST_WORKSPACE:-}" ]]; then # Running inside bazel
  echo "Checking shell scripts..." >&2
elif ! command -v bazel &>/dev/null; then
  echo "Install bazel at https://bazel.build" >&2
  exit 1
else
  (
    set -o xtrace
    bazel test --test_output=streamed //hack:verify-shellcheck
  )
  exit 0
fi

shellcheck=$(realpath "$1")

# TODO: Fix Oracle shell scripts and include in the shell check below.
find . -path './third_party' -prune -false -o -path './oracle' -prune -false -o -name '*.sh' -exec "$shellcheck" {} +
