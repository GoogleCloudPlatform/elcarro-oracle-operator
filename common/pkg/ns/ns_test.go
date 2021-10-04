package ns

import (
	"testing"
)

func TestRedirectToAnotherDestNamespace(t *testing.T) {
	rdo := NewRedirectMapper("target-ns")
	got := rdo.DestNamespace("abc")
	if got != "target-ns" {
		t.Errorf("RedirectToAnother.DestNamespace(); got %v, wantName %v", got, "target-ns")
	}
}

func TestSwappingPrefixForDestNamespace(t *testing.T) {
	psm := NewPrefixSwappingNSMapper("g-", "gs-ods-")
	tests := []struct {
		name     string
		mapper   NamespaceMapper
		srcNS    string
		srcName  string
		wantNS   string
		wantName string
	}{
		{
			name:     "NewPrefixSwappingNSMapper: small ns and name",
			mapper:   psm,
			srcNS:    "g-a",
			srcName:  "b",
			wantNS:   "gs-ods-a",
			wantName: "b",
		},
		{
			name:     "NewPrefixSwappingNSMapper: another small ns and name",
			mapper:   psm,
			srcNS:    "g-g-a",
			srcName:  "b",
			wantNS:   "gs-ods-g-a",
			wantName: "b",
		},
		{
			name:     "NewPrefixSwappingNSMapper: empty ns and small name",
			mapper:   psm,
			srcNS:    "",
			srcName:  "a-b",
			wantNS:   "gs-ods-",
			wantName: "a-b",
		},
		{
			name:     "NewPrefixSwappingNSMapper: ns without old prefix and small name",
			mapper:   psm,
			srcNS:    "a",
			srcName:  "a-b",
			wantNS:   "gs-ods-a",
			wantName: "a-b",
		},
		{
			name:     "NewPrefixSwappingNSMapper: limit ns and name",
			mapper:   psm,
			srcNS:    "g-abcdefghijklmnopqrstuvwxyz-abcedfghijklmnopqrst",
			srcName:  "abcedfghijklmnopqrstuvwx",
			wantNS:   "gs-ods-abcdefghijklmnopqrstuvwxyz-abcedfghijklmnopqrst",
			wantName: "abcedfghijklmnopqrstuvwx",
		},
		{
			name:     "NewPrefixSwappingNSMapper: hashed name",
			mapper:   psm,
			srcNS:    "g-abcdefghijklmnopqrstuvwxyz-abcedfghijklmnopqrstuvwxyz0123456",
			srcName:  "abcdefghijklmnopqrstuvwxyz",
			wantNS:   "gs-ods-abcdefghijklmnopqrstuvwxyz-abcedfghijklmno-scc7pl7tbz5b1",
			wantName: "abcdefghijklmnopqrstuvwxyz",
		},
		{
			name:     "NewPrefixSwappingNSMapper: another hashed name; different suffix",
			mapper:   psm,
			srcNS:    "g-012345678901234567890123456789012345678901234567890abcdefghi",
			srcName:  "abcdefghijklmnopqrstuvwxyz",
			wantNS:   "gs-ods-012345678901234567890123456789012345678901-q0tbwjd49k53",
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
			wantName: "a-b-pppgvo5ka2qa3",
		},
		{
			name:     "NewRedirectMapper: empty ns and small name",
			mapper:   rdm,
			srcNS:    "",
			srcName:  "a-b",
			wantNS:   "target-ns",
			wantName: "a-b-sz0kmra7pime3",
		},
		{
			name:     "NewRedirectMapper: limit ns and name",
			mapper:   rdm,
			srcNS:    "abcdefghijklmnopqrstuvwxyz",
			srcName:  "abcedfghijklmnopqrstuvwx",
			wantNS:   "target-ns",
			wantName: "abcdefghijklmnopqrstuvwxyz-abcedfghijklmnopqrstuv-5bg95jkfpid41",
		},
		{
			name:     "NewRedirectMapper: hashed name",
			mapper:   rdm,
			srcNS:    "abcdefghijklmnopqrstuvwxyz",
			srcName:  "abcedfghijklmnopqrstuvwxy",
			wantNS:   "target-ns",
			wantName: "abcdefghijklmnopqrstuvwxyz-abcedfghijklmnopqrstuv-65ebw68ia50r3",
		},
		{
			name:     "NewRedirectMapper: another hashed name; different suffix",
			mapper:   rdm,
			srcNS:    "abcdefghijklmnopqrstuvwxyz",
			srcName:  "abcdefghijklmnopqrstuvwxyz",
			wantNS:   "target-ns",
			wantName: "abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuv-qv46vzpe5grk2",
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
			srcNS:    "abcdefghijklmnopqrstuvwxyz-abcedfghijklmnopqrstuvwxyz0123456",
			srcName:  "abcdefghijklmnopqrstuvwxyz",
			wantNS:   "ods-abcdefghijklmnopqrstuvwxyz-abcedfghijklmnopqr-0vors61vp6ku3",
			wantName: "abcdefghijklmnopqrstuvwxyz",
		},
		{
			name:     "NewNSPrefixMapper: another hashed name; different suffix",
			mapper:   pm,
			srcNS:    "012345678901234567890123456789012345678901234567890abcdefghi",
			srcName:  "abcdefghijklmnopqrstuvwxyz",
			wantNS:   "ods-012345678901234567890123456789012345678901234-mtdd7iga8jxe2",
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
