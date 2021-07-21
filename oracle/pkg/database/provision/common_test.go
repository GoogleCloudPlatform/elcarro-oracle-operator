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

package provision

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestInitParamOverridesAndMerges(t *testing.T) {

	testCases := []struct {
		name           string
		dbName         string
		dbDomain       string
		userParams     []string
		defaultParams  map[string]string
		expectedParams map[string]string
	}{
		{
			name: "Verify override of common_user_prefix by user params doesn't happen",
			userParams: []string{
				"common_user_prefix='aravsql$'",
			},
			defaultParams: map[string]string{
				"common_user_prefix":        "'gcsql$'",
				"-control_files":            "'/u02/app/oracle/oradata/mydb/control01.ctl'",
				"DB_DOMAIN":                 "'gke'",
				"log_archive_dest_1":        "'LOCATION=USE_DB_RECOVERY_FILE_DEST'",
				"enable_pluggable_database": "TRUE",
			},
			expectedParams: map[string]string{
				"common_user_prefix":        "'gcsql$'",
				"-control_files":            "'/u02/app/oracle/oradata/mydb/control01.ctl'",
				"DB_DOMAIN":                 "'gke'",
				"log_archive_dest_1":        "'LOCATION=USE_DB_RECOVERY_FILE_DEST'",
				"enable_pluggable_database": "TRUE",
			},
		},
		{
			name: "Verify override of enable_pluggable_database  by user params doesn't happen",
			userParams: []string{
				"enable_pluggable_database=FALSE",
			},
			defaultParams: map[string]string{
				"common_user_prefix":        "'gcsql$'",
				"-control_files":            "'/u02/app/oracle/oradata/mydb/control01.ctl'",
				"DB_DOMAIN":                 "'gke'",
				"log_archive_dest_1":        "'LOCATION=USE_DB_RECOVERY_FILE_DEST'",
				"enable_pluggable_database": "TRUE",
			},
			expectedParams: map[string]string{
				"common_user_prefix":        "'gcsql$'",
				"-control_files":            "'/u02/app/oracle/oradata/mydb/control01.ctl'",
				"DB_DOMAIN":                 "'gke'",
				"log_archive_dest_1":        "'LOCATION=USE_DB_RECOVERY_FILE_DEST'",
				"enable_pluggable_database": "TRUE",
			},
		},
		{
			name: "Verify merge of non-internal params happens correctly",
			userParams: []string{
				"open_cursors=300",
				"db_block_size=8192",
				"processes=300",
			},
			defaultParams: map[string]string{
				"common_user_prefix":        "'gcsql$'",
				"-control_files":            "'/u02/app/oracle/oradata/mydb/control01.ctl'",
				"DB_DOMAIN":                 "'gke'",
				"log_archive_dest_1":        "'LOCATION=USE_DB_RECOVERY_FILE_DEST'",
				"enable_pluggable_database": "TRUE",
			},
			expectedParams: map[string]string{
				"common_user_prefix":        "'gcsql$'",
				"-control_files":            "'/u02/app/oracle/oradata/mydb/control01.ctl'",
				"DB_DOMAIN":                 "'gke'",
				"log_archive_dest_1":        "'LOCATION=USE_DB_RECOVERY_FILE_DEST'",
				"enable_pluggable_database": "TRUE",
				"open_cursors":              "300",
				"db_block_size":             "8192",
				"processes":                 "300",
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resultantParams, err := MergeInitParams(tc.defaultParams, tc.userParams)
			if err != nil {
				t.Fatalf("provision.MergeInitParams merging failed for %v, %v: %v", tc.defaultParams, tc.userParams, err)
			}
			t.Logf("expected params is %s", resultantParams)
			if diff := cmp.Diff(tc.expectedParams, resultantParams); diff != "" {
				t.Errorf("Imported data is incorrect (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGetOracleVersionUsingOracleHome(t *testing.T) {
	testCases := []struct {
		oracleHome            string
		expectedOracleVersion string
	}{
		{
			oracleHome:            "/opt/oracle/product/12.2.0.1/dbhome_1",
			expectedOracleVersion: "12.2.0.1",
		},
		{
			oracleHome:            "/u01/app/oracle/product/19c/db",
			expectedOracleVersion: "19c",
		},
	}

	for _, tc := range testCases {
		if oracleVersion := getOracleVersionUsingOracleHome(tc.oracleHome); oracleVersion != tc.expectedOracleVersion {
			t.Errorf("getOracleVersionUsingOracleHome(%s) = %s instead of %s", tc.oracleHome, oracleVersion, tc.expectedOracleVersion)
		}
	}
}
