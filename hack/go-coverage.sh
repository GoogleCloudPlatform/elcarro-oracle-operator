#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

if [[ -n "${BUILD_WORKSPACE_DIRECTORY:-}" ]]; then # Running inside bazel
  echo "Visualizing go coverage..." >&2
elif ! command -v bazel &>/dev/null; then
  echo "Install bazel at https://bazel.build" >&2
  exit 1
else
  (
    set -o xtrace
    bazel run //hack:go-coverage -- "$@"
  )
  exit 0
fi

go=$(realpath "$1")
shift
gocovmerge=$(realpath "$1")
shift

cd "$BUILD_WORKSPACE_DIRECTORY"

if [[ $# -eq 0 ]]; then
  echo "go-coverage.sh: Run go tests and visualized coverage."
  echo "Usage: go-coverage.sh <targets>"
  echo "  e.g. go-coverage.sh //..."
  exit 1
fi

exit_code=0
bazel coverage --define nogo=false --test_output=errors --test_tag_filters=-integration "$@" || exit_code=$?
case $exit_code in
  0) ;;
  3) ;; # We want to continue in case of test failures.
  *) exit $exit_code;;
esac

# Merge all coverage.dat with gocovmerge and display it.
"$go" tool cover -html <(find bazel-testlogs/ -type f -name coverage.dat -exec "$gocovmerge" {} +)
