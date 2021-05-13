// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package common provides general utilities.
//
// Some utility function(s) for oracle connection strings.
//
// Helpers for communicating with Database Daemon.
// Both *nix domain socket and TCP/IP communication
// with the Database Daemon are supported with domain sockets being the default
// mechanism.
package common

import (
	"fmt"
	"net"
	"strings"
)

// EZ returns EZConnect string compatible with oracle tooling.
// All parameters except host are optional, refer to documentation.
// See https://docs.oracle.com/database/121/NETAG/naming.htm#NETAG1112.
func EZ(user, pass, host, port, db, domain string, asSysDba bool) string {
	svc := db
	if domain != "" {
		svc = fmt.Sprintf("%s.%s", db, domain)
	}
	if host == "" {
		return ""
	}
	// username[/password]@[//]host[:port][/service_name][:server][/instance_name]
	cs := strings.TrimRight(net.JoinHostPort(host, port), ":")
	uPart := user
	if pass != "" {
		uPart = fmt.Sprintf("%s/%s", user, pass)
	}
	if uPart != "" {
		cs = fmt.Sprintf("%s@%s", uPart, cs)
	}
	if svc != "" {
		cs = fmt.Sprintf("%s/%s", cs, svc)
	}
	if asSysDba {
		cs = fmt.Sprintf("%s AS SYSDBA", cs)
	}
	return cs
}
