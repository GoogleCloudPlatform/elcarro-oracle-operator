#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

hack=$(dirname "${BASH_SOURCE[0]}")

"$hack/verify-bazel.sh"

bazel test --test_output=streamed \
  //hack:verify-fmt \
  //hack:verify-codegen
