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

# Base image can be either RHEL8 UBI or OEL8 slim
# BASE_IMAGE=registry.access.redhat.com/ubi8/ubi
# ARG BASE_IMAGE=oraclelinux:8-slim
# Oracle 8 does not support 12c.
# ARG BASE_IMAGE=oraclelinux:7-slim

FROM docker.io/oraclelinux:7-slim as base
ARG DB_VERSION
ARG CREATE_CDB
ARG CDB_NAME
ARG CHARACTER_SET
ARG MEM_PCT
ARG EDITION
ARG PATCH_VERSION
ARG INSTALL_SCRIPT
ENV STAGE_DIR=/tmp/stage/

RUN mkdir $STAGE_DIR && chmod ug+w $STAGE_DIR
ADD ./* $STAGE_DIR
RUN /bin/bash -c \
    'if [ "$DB_VERSION" = "18c" ] && [ "$EDITION" = "xe" ]; then \
      export INSTALL_SCRIPT=install-oracle-18c-xe.sh; \
      chmod +x $STAGE_DIR$INSTALL_SCRIPT; \
      $STAGE_DIR/$INSTALL_SCRIPT $CDB_NAME $CHARACTER_SET && \
      rm -rf $INSTALL_SCRIPT && \
      rm -rf $STAGE_DIR; \
    else \
      export INSTALL_SCRIPT=install-oracle.sh; \
      chmod +x $STAGE_DIR$INSTALL_SCRIPT; \
      $STAGE_DIR/$INSTALL_SCRIPT $DB_VERSION $EDITION $CREATE_CDB $CDB_NAME $CHARACTER_SET $MEM_PCT $PATCH_VERSION && \
      rm -rf $INSTALL_SCRIPT && \
      rm -rf $STAGE_DIR; \
    fi'

VOLUME ["/u02", "/u03"]
# TODO: make the port number configurable
EXPOSE 1521

# Define default command to start Oracle Database.
CMD exec /bin/bash
