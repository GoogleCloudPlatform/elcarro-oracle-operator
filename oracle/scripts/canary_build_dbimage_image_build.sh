#!/usr/bin/env bash
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

# Rebuild shared images
set -o errexit
set -o nounset
set -o pipefail
set -x

export DBNAME=MYDB
export GCS_BUCKET=graybox-canary-build
export PROJECT_ID=prow-build-graybox
export PROJECT_NUMBER=1068261481923
export TIME_TAG=$(date +%Y%m%dT%H%M%S)

export DB_VERSION=19.3
export IMG="gcr.io/prow-build-graybox/oracle-database-images/oracle-${DB_VERSION}-ee-seeded-mydb"
export IMAGE_TAG_TIME="${IMG}:${TIME_TAG}"
export IMAGE_TAG_LATEST="${IMG}:latest"

# # TODO: cover more images in this process
# # echo $TEST_IMAGE_ORACLE_18_XE_SEEDED
# # echo $TEST_IMAGE_ORACLE_19_3_EE_SEEDED
# # echo $TEST_IMAGE_ORACLE_12_2_EE_UNSEEDED_31741641
# # echo $TEST_IMAGE_ORACLE_12_2_EE_SEEDED_BUGGY
# # echo $TEST_IMAGE_ORACLE_12_2_EE_SEEDED
# # echo $TEST_IMAGE_ORACLE_19_3_EE_UNSEEDED_32545013

# # NOTE: permissions need to be set to read the files needed for build from GCS
# # gsutil iam ch serviceAccount:${PROJECT_NUMBER}@cloudbuild.gserviceaccount.com:roles/storage.objectViewer gs://${GCS_BUCKET}

cd build/dbimage
./image_build.sh \
--install_path=gs://$GCS_BUCKET/install \
--db_version=$DB_VERSION \
--create_cdb=true \
--cdb_name=$DBNAME \
--mem_pct=45 \
--no_dry_run \
--patch_version=33192793 \
--project_id=$PROJECT_ID \
--tag=$IMAGE_TAG_TIME

# Tag the image with latest, if we can tag it, then that implies the image succeeded in build/push
gcloud container images add-tag $IMAGE_TAG_TIME $IMAGE_TAG_LATEST -q

