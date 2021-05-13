package sql

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	createPDBCmd      = "create pluggable database %s admin user %s identified by %s create_file_dest='%s' default tablespace %s datafile '%s' size 1G autoextend on storage unlimited file_name_convert=('%s', '%s')"
	setContainerCmd   = "alter session set container=%s"
	createDirCmd      = "create directory %s as '%s'"
	createUserCmd     = "create user %s identified by %s"
	alterUserCmd      = "alter user %s identified by %s"
	grantPrivCmd      = "grant %s to %s"
	revokePrivCmd     = "revoke %s from %s"
	alterSystemSetCmd = "alter system set %s=%s"
)

var (
	// ErrQuoteInIdentifier is an error returned when an identifier
	// contains a double-quote.
	ErrQuoteInIdentifier      = errors.New("identifier contains double quotes")
	privilegeMatcher          = regexp.MustCompile(`^[A-Za-z ,_]+$`).MatchString
	parameterNonStringMatcher = regexp.MustCompile(`^[A-Za-z0-9-]+$`).MatchString
)

// QueryCreatePDB constructs a sql statement for creating a new pluggable database.
// It panics if one of the following params is not a valid identifier
// * pdbName
// * adminUser
// * adminUserPass
// * defaultTablespace
func QueryCreatePDB(pdbName, adminUser, adminUserPass, dataFilesDir, defaultTablespace, defaultTablespaceDatafile, fileConvertFrom, fileConvertTo string) string {
	return fmt.Sprintf(createPDBCmd,
		MustBeObjectName(pdbName),
		MustBeObjectName(adminUser),
		MustBeIdentifier(adminUserPass),
		StringParam(dataFilesDir),
		MustBeObjectName(defaultTablespace),
		StringParam(defaultTablespaceDatafile),
		StringParam(fileConvertFrom),
		StringParam(fileConvertTo),
	)
}

// QueryCreateDir constructs a sql statement for creating a new Oracle directory.
// It panics if dirName is not a valid identifier.
func QueryCreateDir(dirName, path string) string {
	return fmt.Sprintf(createDirCmd,
		MustBeObjectName(dirName),
		StringParam(path),
	)
}

// QueryCreateUser constructs a sql statement for creating a new database user.
// It panics if any parameter is not a valid identifier.
func QueryCreateUser(name, pass string) string {
	return fmt.Sprintf(createUserCmd,
		MustBeObjectName(name),
		MustBeIdentifier(pass),
	)
}

// QueryAlterUser constructs a sql statement for updating user password.
// It panics if any parameter is not a valid identifier.
func QueryAlterUser(name, pass string) string {
	return fmt.Sprintf(alterUserCmd,
		MustBeObjectName(name),
		MustBeIdentifier(pass),
	)
}

// QuerySetSessionContainer constructs a sql statement for changing session
// container to the given pdbName.
// It panics if pdbName is not a valid identifier.
func QuerySetSessionContainer(pdbName string) string {
	return fmt.Sprintf(setContainerCmd, MustBeObjectName(pdbName))
}

// Identifier escapes an Oracle identifier.
// If id is not a valid identifier the ErrQuoteInIdentifier error is returned.
func Identifier(id string) (string, error) {
	if strings.Contains(id, `"`) {
		return "", ErrQuoteInIdentifier
	}

	return `"` + id + `"`, nil
}

// ObjectName escapes an Oracle object name.
// If id is not a valid identifier the ErrQuoteInIdentifier error is returned.
func ObjectName(id string) (string, error) {
	return Identifier(strings.ToUpper(id))
}

// MustBeIdentifier escapes an Oracle identifier.
// It panics if id is not a valid identifier.
func MustBeIdentifier(id string) string {
	sanitizedID, err := Identifier(id)
	if err != nil {
		panic(err)
	}
	return sanitizedID
}

// MustBeObjectName escapes an Oracle object name.
// It panics if id is not a valid identifier.
func MustBeObjectName(id string) string {
	sanitizedID, err := ObjectName(id)
	if err != nil {
		panic(err)
	}
	return sanitizedID
}

// StringParam escapes a string parameter.
func StringParam(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// IsPrivilege returns true if parameter has the expected syntax of
// (comma separated) list of privileges.
func IsPrivilege(p string) bool {
	return privilegeMatcher(p)
}

func mustBePrivilege(p string) string {
	if !IsPrivilege(p) {
		panic(fmt.Errorf("not a privilege: %s", p))
	}
	return p
}

// QueryGrantPrivileges constructs a sql statement for granting privileges.
// It panics if privileges is not a valid list of privileges (syntactically) or
// grantee is not a valid identifier.
func QueryGrantPrivileges(privileges, grantee string) string {
	return fmt.Sprintf(grantPrivCmd,
		mustBePrivilege(privileges),
		MustBeObjectName(grantee),
	)
}

// QueryRevokePrivileges constructs a sql statement for revoking privileges.
// It panics if privileges is not a valid list of privileges (syntactically) or
// grantee is not a valid identifier.
func QueryRevokePrivileges(privileges, grantee string) string {
	return fmt.Sprintf(revokePrivCmd,
		mustBePrivilege(privileges),
		MustBeObjectName(grantee),
	)
}

// IsValidParameterValue returns false if parameter value is not a valid one
// based on the parameter type.
// It still can return true in cases when parameter value won't be accepted by the database
// e.g. int value set for a boolean parameter or vice versa, but the cases
// relevant for constructing a syntactically correct query are supported.
func IsValidParameterValue(value string, isTypeString bool) bool {
	if isTypeString {
		return true
	}
	return parameterNonStringMatcher(value)
}

// QuerySetSystemParameter constructs a sql statement for setting a database parameter.
// It returns an error if IsValidParameterValue(value, isTypeString) return false.
func QuerySetSystemParameterNoPanic(name, value string, isTypeString bool) (string, error) {
	if !IsValidParameterValue(value, isTypeString) {
		return "", fmt.Errorf("unsupported value %q for parameter %q", value, name)
	}

	if isTypeString {
		value = "'" + StringParam(value) + "'"
	}

	return fmt.Sprintf(alterSystemSetCmd, name, value), nil
}
