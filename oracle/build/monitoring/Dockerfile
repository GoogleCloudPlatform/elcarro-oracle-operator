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

FROM docker.io/golang:1.15 AS builder

WORKDIR /build

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# Copy the go source
COPY third_party/ third_party/
COPY oracle/pkg/ oracle/pkg/
COPY oracle/cmd/ oracle/cmd/
# Without this, monitoring agent would fail for not access to the file.
RUN chmod a+r /build/oracle/pkg/agents/monitoring/default-metrics.yaml

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o monitoring_agent oracle/cmd/monitoring/monitoring_agent.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /build/monitoring_agent .
COPY --from=builder /build/oracle/pkg/agents/monitoring/default-metrics.yaml .

EXPOSE 9161

ENTRYPOINT ["/monitoring_agent"]
