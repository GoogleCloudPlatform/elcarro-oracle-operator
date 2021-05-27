#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

hack=$(dirname "${BASH_SOURCE[0]}")

"$hack/update-gomod.sh"
"$hack/update-bazel.sh"
"$hack/update-fmt.sh"
"$hack/update-codegen.sh"
