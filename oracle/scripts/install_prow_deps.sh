#!/bin/bash
# Copyright 2021 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


# Install's all the build/test/run dependencies for prow, this meant to run on
# the k8s kubekins package which includes go/bazel/gcloud sdk/k8s and built on
# a debian container.

set -x #echo on
set -e

# Setup bazel caching
# DO NOT use this cache from your local machine.
CC_HASH=$(sha256sum $(which ${CC:-gcc}) | cut -c1-8)
PY_HASH=$(sha256sum $(which python) | cut -c1-8)
CACHE_KEY="CC:${CC_HASH:-err},PY:${PY_HASH:-err}"

cat << EOF >> .bazelrc
build --remote_cache=https://storage.googleapis.com/graybox-bazel-cache/${CACHE_KEY}
build --google_default_credentials
EOF

INSTALL_TMP_DIR=$(mktemp -d)
cd $INSTALL_TMP_DIR

# upgrade bazel
apt-get install -y apt-transport-https curl gnupg
curl -fsSL https://storage.googleapis.com/www.bazel.build/bazel-release.pub.gpg | gpg --dearmor > bazel.gpg
mv bazel.gpg /etc/apt/trusted.gpg.d/
echo "deb [arch=amd64] https://storage.googleapis.com/bazel-apt stable jdk1.8" | tee /etc/apt/sources.list.d/bazel.list

# everything we can get from debian packages.
apt-get update -qq
apt-get install -y \
  bazel clang-format gettext-base jq

# unlink the bazel from image
unlink /usr/local/bin/bazel
bazel --version

# Link the kubekins install to the typical debian location to match Dev
# machines.
ln -s /google-cloud-sdk /usr/lib/google-cloud-sdk

# If we need a specific kubectl from gcr.io/k8s-testimages/kubekins-e2e
# rm /usr/local/bin/kubectl
# ln -s /google-cloud-sdk/bin/kubectl.1.18 /usr/local/bin/kubectl

gcloud auth configure-docker --quiet

# cleanup
cd /
rm -rf /var/lib/apt/lists/*
rm -rf "$INSTALL_TMP_DIR"
