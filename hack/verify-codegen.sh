#!/bin/bash

set -o nounset
set -o errexit
set -o pipefail

if [[ -n "${TEST_WORKSPACE:-}" ]]; then # Running inside bazel
  echo "Checking generated code for changes..." >&2
elif ! command -v bazel &>/dev/null; then
  echo "Install bazel at https://bazel.build" >&2
  exit 1
else
  (
    set -o xtrace
    bazel test --test_output=streamed //hack:verify-codegen
  )
  exit 0
fi

# controller-gen doesn't support dry-run mode. We have to copy the whole source tree, run
# "update-codegen.sh" and use diff to compare the difference.
tmpfiles=$TEST_TMPDIR/files
(
  mkdir -p "$tmpfiles"
  cp -aL . "$tmpfiles"
  rm -r "${tmpfiles:?}/external"
  export BUILD_WORKSPACE_DIRECTORY=$tmpfiles
  export HOME=$TEST_TMPDIR/home # HOME is required to enable GOCACHE
  "$@"
)

# diff can only exclude "file name pattern" instead of "file path".
# So we remove the binaries before diff.
rm -r external
diff=$(diff -upr . "$tmpfiles" || true)

if [[ -n "${diff}" ]]; then
  echo "${diff}" >&2
  echo >&2
  echo "Generated code are out of date. Please run './hack/update-codegen.sh'" >&2
  exit 1
fi
