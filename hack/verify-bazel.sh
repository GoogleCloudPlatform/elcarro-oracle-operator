#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

if [[ -n "${TEST_WORKSPACE:-}" ]]; then # Running inside bazel
  echo "Validating bazel rules..." >&2
elif ! command -v bazel &> /dev/null; then
  echo "Install bazel at https://bazel.build" >&2
  exit 1
else
  (
    set -o xtrace
    bazel test --test_output=streamed //hack:verify-bazel
  )
  exit 0
fi

gazelle=$(realpath "$1")
kazel=$(realpath "$2")

gazelle_diff=$("$gazelle" fix --mode=diff || true)
kazel_diff=$("$kazel" --dry-run --print-diff)
if [[ -n "${gazelle_diff}${kazel_diff}" ]]; then
  echo "Current rules (-) do not match expected (+):" >&2
  echo "${gazelle_diff}"
  echo "${kazel_diff}"
  echo
  echo "ERROR: bazel rules out of date. Fix with ./hack/update-bazel.sh" >&2
  exit 1
fi
