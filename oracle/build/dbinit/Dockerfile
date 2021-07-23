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

# Build the Database Daemon binary.
FROM docker.io/golang:1.15 as builder

WORKDIR /build

COPY go.mod go.mod
COPY go.sum go.sum

# Copy the go source
COPY oracle/pkg/ oracle/pkg/
COPY oracle/cmd/ oracle/cmd/

RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o dbdaemon oracle/cmd/dbdaemon/dbdaemon.go
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o dbdaemon_proxy oracle/cmd/dbdaemon_proxy/dbdaemon_proxy.go
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o init_oracle oracle/cmd/init_oracle/init_oracle.go

FROM docker.io/busybox
COPY --from=builder /build/dbdaemon \
/build/dbdaemon_proxy \
/build/init_oracle \
/build/oracle/cmd/init_oracle/init_container.sh \
/build/oracle/cmd/init_oracle/stop_oracle.sh \
/build/oracle/cmd/dbdaemon/init_dbdaemon.sh \
/build/oracle/pkg/database/provision/bootstrap-database-initfile.template \
/build/oracle/pkg/database/provision/bootstrap-database-initfile-oracle-xe.template \
/build/oracle/pkg/database/provision/bootstrap-database-crcf.template \
/build/oracle/pkg/database/provision/bootstrap-database-listener.template \
/build/oracle/pkg/database/provision/bootstrap-database-tnsnames.template \
/build/oracle/pkg/database/provision/sqlnet.ora \
./agent_repo/
RUN chmod -R 755 ./agent_repo/
