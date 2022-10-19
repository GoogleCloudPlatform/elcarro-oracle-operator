package monitoring

import (
	"io/ioutil"
	"net/url"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/klog/v2"
)

func writeTmpOrFail(t *testing.T, data []byte, simpleName string) *os.File {
	tmp, err := ioutil.TempFile("", "user")
	if err != nil {
		t.Fatalf("Unable to prepare %s file: %s", simpleName, err)
	}
	if _, err := tmp.Write([]byte(data)); err != nil {
		t.Fatalf("Unable to write to %s file: %s", simpleName, err)
	}
	return tmp
}

func mustParseURI(t *testing.T, uri string) *url.URL {
	ret, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("Unable to parse URI %s: %s", uri, err)
	}
	return ret
}

func TestGetDefaultDSN(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		user    string
		pass    string
		wantURI *url.URL
	}{{
		name:    "All vars set",
		uri:     "dbspecific://host:1234/service",
		user:    "user",
		pass:    "pass",
		wantURI: mustParseURI(t, "dbspecific://user:pass@host:1234/service"),
	}, {
		/* These cases should cause the program to exit, but we cannot test in that.
			name:      "Bad URI",
			uri:       "host=localhost port=1234 user=user pass=pass",
			user:      "user",
			pass:      "pass",
			wantExit: true,
		}, {
			name:      "no URI",
			uri:       "",
			user:      "user",
			pass:      "pass",
			wantExit: true,
		}, {
		*/
		name:    "No user file",
		uri:     "dbspecific://some:user@host:1234/service",
		user:    "",
		pass:    "pass",
		wantURI: mustParseURI(t, "dbspecific://:pass@host:1234/service"),
	}, {
		name:    "No pass file",
		uri:     "dbspecific://some:user@host:1234/service",
		user:    "user",
		pass:    "",
		wantURI: mustParseURI(t, "dbspecific://user:@host:1234/service"),
	}}

	for _, test := range tests {
		os.Unsetenv("DATA_SOURCE_URI")
		os.Unsetenv("DATA_SOURCE_USER_FILE")
		os.Unsetenv("DATA_SOURCE_PASS_FILE")

		if test.uri != "" {
			os.Setenv("DATA_SOURCE_URI", test.uri)
		}
		if test.user != "" {
			tmp := writeTmpOrFail(t, []byte(test.user), "user")
			defer tmp.Close()
			os.Setenv("DATA_SOURCE_USER_FILE", tmp.Name())
		}
		if test.pass != "" {
			tmp := writeTmpOrFail(t, []byte(test.pass), "pass")
			defer tmp.Close()
			os.Setenv("DATA_SOURCE_PASS_FILE", tmp.Name())
		}

		cmpOpts := []cmp.Option{
			cmp.AllowUnexported(url.Userinfo{}),
		}

		uri := GetDefaultDSN(klog.NewKlogr())
		if diff := cmp.Diff(uri, test.wantURI, cmpOpts...); diff != "" {
			t.Errorf("%v: GetDefaultDSN() returned diff (-want, +got):\n%s", test.name, diff)
		}
	}
}

func TestReadConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		expectedErr bool
	}{{
		"simple input",
		`
- name: process
  namespace: ora
  query: SELECT COUNT(*) as count FROM v$process
  metrics:
    - name: count
      desc: Gauge metric with count of processes.
      usage: gauge

		`,
		false,
	}, {
		"all usage types",
		`
- name: some
  namespace: test
  query: select * from dual
  metrics:
    -  name: a
       desc: a
       usage: label
    -  name: b
       desc: b
       usage: counter
    -  name: c
       desc: c
       usage: gauge
    -  name: d
       desc: d
       usage: histogram
`,
		false,
	}, {
		"malformed input",
		"- 11193101jf1",
		true,
	}, {
		"invalid set name",
		`
- name: some/invalid
  namespace: test
  query: select * from dual
  metrics:
    -  name: a
       desc: b
       usage: gauge
`,
		true,
	}, {
		"invalid metric name",
		`
- name: some
  namespace: test
  query: select * from dual
  metrics:
    -  name: a+
       desc: b
       usage: gauge
`,
		true,
	}, {
		"no value metrics",
		`
- name: some
  namespace: test
  query: select * from dual
  metrics:
    -  name: a
       desc: b
       usage: label
`,
		true,
	}}

	for _, test := range tests {
		ms, err := ReadConfig([]byte(test.config))
		if test.expectedErr && err == nil {
			t.Errorf("%v: ReadConfig() got nil err, but expected error: ms=%+v", test.name, ms)
		}
	}
}
