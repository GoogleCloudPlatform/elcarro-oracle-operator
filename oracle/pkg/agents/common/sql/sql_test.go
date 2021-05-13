package sql

import (
	"testing"
)

func TestObjectName(t *testing.T) {
	tests := []struct {
		id      string
		want    string
		wantErr bool
	}{
		{
			id:   "scott",
			want: `"SCOTT"`,
		},
		{
			id:   "SCOTT",
			want: `"SCOTT"`,
		},
		{
			id:      `scott"; DROP TABLE USERS; "`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got, err := ObjectName(tt.id)

			if (err != nil) != tt.wantErr {
				t.Errorf("ObjectName(%q) error = %q, wantErr %v", tt.id, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ObjectName(%q) got = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestMustBeObjectName(t *testing.T) {
	tests := []struct {
		id        string
		want      string
		wantPanic bool
	}{
		{
			id:   "scott",
			want: `"SCOTT"`,
		},
		{
			id:   "SCOTT",
			want: `"SCOTT"`,
		},
		{
			id:        `scott"; DROP TABLE USERS; "`,
			wantPanic: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			defer func() {
				gotPanic := recover() != nil
				if gotPanic != tt.wantPanic {
					t.Errorf("MustBeObjectName(%q) panic = %v, wantPanic %v", tt.id, gotPanic, tt.wantPanic)
					return
				}
			}()

			got := MustBeObjectName(tt.id)

			if got != tt.want {
				t.Errorf("MustBeObjectName(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestIdentifier(t *testing.T) {
	tests := []struct {
		id      string
		want    string
		wantErr bool
	}{
		{
			id:   "scott",
			want: `"scott"`,
		},
		{
			id:   "SCOTT",
			want: `"SCOTT"`,
		},
		{
			id:      `scott"; DROP TABLE USERS; "`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got, err := Identifier(tt.id)

			if (err != nil) != tt.wantErr {
				t.Errorf("Identifier(%q) error = %q, want %v", tt.id, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("Identifier(%q) got = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestMustBeIdentifier(t *testing.T) {
	tests := []struct {
		id        string
		want      string
		wantPanic bool
	}{
		{
			id:   "scott",
			want: `"scott"`,
		},
		{
			id:   "SCOTT",
			want: `"SCOTT"`,
		},
		{
			id:        `scott"; DROP TABLE USERS; "`,
			wantPanic: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			defer func() {
				gotPanic := recover() != nil
				if gotPanic != tt.wantPanic {
					t.Errorf("MustBeIdentifier(%q) panic = %v, wantPanic %v", tt.id, gotPanic, tt.wantPanic)
					return
				}
			}()

			got := MustBeIdentifier(tt.id)

			if got != tt.want {
				t.Errorf("MustBeIdentifier(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestIsPrivilege(t *testing.T) {
	tests := []struct {
		priv string
		want bool
	}{
		{
			priv: "create session",
			want: true,
		},
		{
			priv: "create session, resource, datapump_imp_full_database, datapump_exp_full_database, unlimited tablespace",
			want: true,
		},
		{
			priv: "RESOURCE",
			want: true,
		},
		{
			priv: "connect; drop table users",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.priv, func(t *testing.T) {
			got := IsPrivilege(tt.priv)
			if got != tt.want {
				t.Errorf("IsPrivilege(%q) got = %v, want %v", tt.priv, got, tt.want)
			}
		})
	}
}

func TestMustBePrivilege(t *testing.T) {
	tests := []struct {
		priv      string
		wantPanic bool
	}{
		{
			priv:      "create session",
			wantPanic: false,
		},
		{
			priv:      "create session, resource, datapump_imp_full_database, datapump_exp_full_database, unlimited tablespace",
			wantPanic: false,
		},
		{
			priv:      "RESOURCE",
			wantPanic: false,
		},
		{
			priv:      "connect; drop table users",
			wantPanic: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.priv, func(t *testing.T) {
			defer func() {
				gotPanic := recover() != nil
				if gotPanic != tt.wantPanic {
					t.Errorf("mustBePrivilege(%q) panic = %v, wantPanic %v", tt.priv, gotPanic, tt.wantPanic)
					return
				}
			}()

			got := mustBePrivilege(tt.priv)

			if got != tt.priv {
				t.Errorf("mustBePrivilege(%q) got = %v, want %v", tt.priv, got, tt.priv)
			}
		})
	}
}

func TestStringParam(t *testing.T) {
	tests := []struct {
		str  string
		want string
	}{
		{
			str:  "text",
			want: "text",
		},
		{
			str:  "SOME TEXT",
			want: "SOME TEXT",
		},
		{
			str:  "That's '' single quotes",
			want: "That''s '''' single quotes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.str, func(t *testing.T) {
			got := StringParam(tt.str)
			if got != tt.want {
				t.Errorf("StringParam(%q) got = %v, want %v", tt.str, got, tt.want)
			}
		})
	}
}

func TestIsValidParameterValue(t *testing.T) {
	tests := []struct {
		name     string
		isString bool
		value    string
		want     bool
	}{
		{
			name:     "string parameter",
			isString: true,
			value:    "/u01/bin, /u02/bin, 'etc'",
			want:     true,
		},
		{
			name:     "injection attempt",
			isString: false,
			value:    "true; DROP TABLE USERS",
			want:     false,
		},
		{
			name:     "positive number",
			isString: false,
			value:    "120",
			want:     true,
		},
		{
			name:     "negative number",
			isString: false,
			value:    "-120",
			want:     true,
		},
		{
			name:     "true",
			isString: false,
			value:    "true",
			want:     true,
		},
		{
			name:     "FALSE",
			isString: false,
			value:    "FALSe",
			want:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidParameterValue(tt.value, tt.isString)
			if got != tt.want {
				t.Errorf("IsValidParameterValue(%q, %v) got = %v, want %v", tt.value, tt.isString, got, tt.want)
			}
		})
	}
}

func TestQuerySetSystemParameterNoPanic(t *testing.T) {
	testParamName := "p"

	tests := []struct {
		name     string
		isString bool
		value    string
		want     string
		wantErr  bool
	}{
		{
			name:     "string parameter",
			isString: true,
			value:    "/u01/bin, /u02/bin, 'etc'",
			want:     `alter system set p='/u01/bin, /u02/bin, ''etc'''`,
			wantErr:  false,
		},
		{
			name:     "injection attempt",
			isString: false,
			value:    "true; DROP TABLE USERS",
			wantErr:  true,
		},
		{
			name:     "string parameter, with SQL",
			isString: true,
			value:    "true; DROP TABLE USERS",
			want:     `alter system set p='true; DROP TABLE USERS'`,
			wantErr:  false,
		},
		{
			name:     "positive number",
			isString: false,
			value:    "120",
			want:     `alter system set p=120`,
			wantErr:  false,
		},
		{
			name:     "negative number",
			isString: false,
			value:    "-120",
			want:     "alter system set p=-120",
			wantErr:  false,
		},
		{
			name:     "true",
			isString: false,
			value:    "true",
			want:     "alter system set p=true",
			wantErr:  false,
		},
		{
			name:     "FALSE",
			isString: false,
			value:    "FALSE",
			want:     "alter system set p=FALSE",
			wantErr:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := QuerySetSystemParameterNoPanic(testParamName, tt.value, tt.isString)
			if (err != nil) != tt.wantErr {
				t.Errorf("QuerySetSystemParameterNoPanic(%q, %q, %v) error = %q, wantErr %v", testParamName, tt.value, tt.isString, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("QuerySetSystemParameterNoPanic(%q, %q, %v) got = %v, want %v", testParamName, tt.value, tt.isString, got, tt.want)
			}
		})
	}
}
