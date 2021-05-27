#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

bazel test --test_output=streamed \
  //hack:verify-bazel \
  //hack:verify-fmt \
  //hack:verify-codegen \
  //hack:verify-shellcheck
