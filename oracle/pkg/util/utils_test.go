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

package util

import (
	"testing"
)

func TestGcsUtilImplSplitURI(t *testing.T) {
	tests := []struct {
		url        string
		wantBucket string
		wantName   string
	}{
		{"gs://path/to/a/file", "path", "to/a/file"},
		{"gs://bucket/file.ext", "bucket", "file.ext"},
	}

	for _, test := range tests {
		g := &GCSUtilImpl{}
		gotBucket, gotName, err := g.SplitURI(test.url)

		if err != nil || gotBucket != test.wantBucket || gotName != test.wantName {
			t.Errorf("gcsUtilImpl.SplitURI(%q)=(%q, %q, %q); wanted (%q, %q, nil)",
				test.url, gotBucket, gotName, err, test.wantBucket, test.wantName)
		}
	}
}

func TestGcsUtilImplSplitURIError(t *testing.T) {
	tests := []struct {
		url string
	}{
		{"missing/prefix/in/url"},
		{"gs://missing-bucket"},
	}

	for _, test := range tests {
		g := &GCSUtilImpl{}
		gotBucket, gotName, err := g.SplitURI(test.url)

		if err == nil {
			t.Errorf("gcsUtilImpl.splitURI(%q)=(%q, %q, nil); wanted an error",
				test.url, gotBucket, gotName)
		}
	}
}
