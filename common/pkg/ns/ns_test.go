package ns

import "testing"

func TestRedirectToAnotherDestNamespace(t *testing.T) {
	rdo := NewRedirectMapper("target-ns")
	got := rdo.DestNamespace("abc")
	if got != "target-ns" {
		t.Errorf("RedirectToAnother.DestNamespace(); got %v, wantName %v", got, "target-ns")
	}
}

func TestRedirectToAnotherNS(t *testing.T) {
	rdm := NewRedirectMapper("target-ns")
	pm := NewNSPrefixMapper("ods")
	tests := []struct {
		name     string
		mapper   NamespaceMapper
		srcNS    string
		srcName  string
		wantNS   string
		wantName string
	}{
		{
			name:     "NewRedirectMapper: small ns and name",
			mapper:   rdm,
			srcNS:    "a",
			srcName:  "b",
			wantNS:   "target-ns",
			wantName: "a-b-YnZ12Te9xRe",
		},
		{
			name:     "NewRedirectMapper: empty ns and small name",
			mapper:   rdm,
			srcNS:    "",
			srcName:  "a-b",
			wantNS:   "target-ns",
			wantName: "a-b-r2ZyZy9wnnf",
		},
		{
			name:     "NewRedirectMapper: limit ns and name",
			mapper:   rdm,
			srcNS:    "abcdefghijklmnopqrstuvwxyz",
			srcName:  "abcedfghijklmnopqrstuvwx",
			wantNS:   "target-ns",
			wantName: "abcdefghijklmnopqrstuvwxyz-abcedfghijklmnopqrstuvwx-d1B2eQE0Vn5",
		},
		{
			name:     "NewRedirectMapper: hashed name",
			mapper:   rdm,
			srcNS:    "abcdefghijklmnopqrstuvwxyz",
			srcName:  "abcedfghijklmnopqrstuvwxy",
			wantNS:   "target-ns",
			wantName: "abcdefghijklmnopqrstuvwxyz-abcedfghijklmnopqrstuvwx-wEHR2lf6B2h",
		},
		{
			name:     "NewRedirectMapper: another hashed name; different suffix",
			mapper:   rdm,
			srcNS:    "abcdefghijklmnopqrstuvwxyz",
			srcName:  "abcdefghijklmnopqrstuvwxyz",
			wantNS:   "target-ns",
			wantName: "abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwx-QPxNa2k0Ynb",
		},
		{
			name:     "NewNSPrefixMapper: small ns and name",
			mapper:   pm,
			srcNS:    "a",
			srcName:  "b",
			wantNS:   "ods-a",
			wantName: "b",
		},
		{
			name:     "NewNSPrefixMapper: empty ns and small name",
			mapper:   pm,
			srcNS:    "",
			srcName:  "b",
			wantNS:   "ods",
			wantName: "b",
		},
		{
			name:     "NewNSPrefixMapper: limit ns and name",
			mapper:   pm,
			srcNS:    "abcdefghijklmnopqrstuvwxyz-abcedfghijklmnopqrst",
			srcName:  "abcedfghijklmnopqrstuvwx",
			wantNS:   "ods-abcdefghijklmnopqrstuvwxyz-abcedfghijklmnopqrst",
			wantName: "abcedfghijklmnopqrstuvwx",
		},
		{
			name:     "NewNSPrefixMapper: hashed name",
			mapper:   pm,
			srcNS:    "abcdefghijklmnopqrstuvwxyz-abcedfghijklmnopqrstuvwxyz",
			srcName:  "abcdefghijklmnopqrstuvwxyz",
			wantNS:   "ods-abcdefghijklmnopqrstuvwxyz-abcedfghijklmnopqrst-ssf0qZp8vw7",
			wantName: "abcdefghijklmnopqrstuvwxyz",
		},
		{
			name:     "NewNSPrefixMapper: another hashed name; different suffix",
			mapper:   pm,
			srcNS:    "012345678901234567890123456789012345678901234567890",
			srcName:  "abcdefghijklmnopqrstuvwxyz",
			wantNS:   "ods-01234567890123456789012345678901234567890123456-tVvZbfl0sDd",
			wantName: "abcdefghijklmnopqrstuvwxyz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.mapper.DestName(tt.srcNS, tt.srcName)
			if got != tt.wantName {
				t.Errorf("mapper.DestName(); got %v, wantName %v; len(got) %v, len(wat) %v", got, tt.wantName, len(got), len(tt.wantName))
			}
			got = tt.mapper.DestNamespace(tt.srcNS)
			if got != tt.wantNS {
				t.Errorf("mapper.DestNamespace(); got %v, wantName %v; len(got) %v, len(wat) %v", got, tt.wantNS, len(got), len(tt.wantNS))
			}
		})
	}
}
