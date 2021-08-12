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
ARG ORACLE_HOME
ARG ORACLE_BASE
ARG CREATE_CDB
ARG CDB_NAME
ENV STAGE_DIR=/tmp/stage/
COPY oracle-preinstall.sh $STAGE_DIR
RUN /bin/bash -c $STAGE_DIR/oracle-preinstall.sh

FROM base as installer

ARG DB_VERSION
ARG CREATE_CDB
ARG CDB_NAME
ARG CHARACTER_SET
ARG MEM_PCT
ARG EDITION
ARG PATCH_VERSION
ARG INSTALL_SCRIPT

ADD ./* $STAGE_DIR
RUN /bin/bash -c \
    'if [ "${DB_VERSION}" = "18c" ] && [ "${EDITION}" = "xe" ]; then \
      export INSTALL_SCRIPT=install-oracle-18c-xe.sh; \
      chmod +x $STAGE_DIR$INSTALL_SCRIPT; \
      $STAGE_DIR/$INSTALL_SCRIPT "${CDB_NAME}" "${CHARACTER_SET}" && \
      rm -rf $INSTALL_SCRIPT && \
      rm -rf $STAGE_DIR; \
    else \
      export INSTALL_SCRIPT=install-oracle.sh; \
      chmod +x $STAGE_DIR$INSTALL_SCRIPT; \
      $STAGE_DIR/$INSTALL_SCRIPT "${DB_VERSION}" "${EDITION}" "${CREATE_CDB}" "${CDB_NAME}" "${CHARACTER_SET}" "${MEM_PCT}" "${PATCH_VERSION}" && \
      rm -rf $INSTALL_SCRIPT && \
      rm -rf $STAGE_DIR; \
    fi'

FROM base as database_image

ARG ORACLE_HOME
ARG ORACLE_BASE
ARG CDB_NAME

ENV ORACLE_HOME=$ORACLE_HOME
ENV ORACLE_BASE=$ORACLE_BASE
ENV ORACLE_SID=$CDB_NAME
ENV LD_LIBRARY_PATH=$ORACLE_HOME/lib:/usr/lib

USER oracle
COPY --chown=oracle:dba --from=installer $ORACLE_BASE $ORACLE_BASE
COPY --chown=oracle:dba --from=installer /etc/oratab /etc/oratab
COPY --chown=oracle:dba --from=installer /etc/oraInst.loc /etc/oraInst.loc

USER root
RUN $ORACLE_HOME/root.sh && rm $STAGE_DIR/oracle-preinstall.sh

VOLUME ["/u02", "/u03"]
# TODO: make the port number configurable
EXPOSE 1521

# Define default command to start Oracle Database.
CMD exec /bin/bash
