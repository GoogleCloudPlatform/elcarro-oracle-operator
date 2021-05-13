package common

import (
	"fmt"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/agents/consts"
)

// GetSourceOracleDataDirectory returns the Oracle data directory on the volume where Oracle is installed.
func GetSourceOracleDataDirectory(oracleVersion string) string {
	if oracleVersion == consts.Oracle18c {
		return consts.SourceOracleXeDataDirectory
	}
	return consts.SourceOracleDataDirectory
}

// GetSourceOracleBase returns the value of ORACLE_BASE where Oracle is installed.
func GetSourceOracleBase(oracleVersion string) string {
	if oracleVersion == consts.Oracle18c {
		return consts.SourceOracleXeBase
	}
	return consts.SourceOracleBase
}

// GetSourceOracleHome returns the value of ORACLE_HOME where Oracle is installed.
func GetSourceOracleHome(oracleVersion string) string {
	if oracleVersion == consts.Oracle18c {
		return fmt.Sprintf(consts.SourceOracleXeHome, oracleVersion)
	}
	return fmt.Sprintf(consts.SourceOracleHome, oracleVersion)
}

// GetSourceOracleInventory returns the source OraInventory path.
func GetSourceOracleInventory(oracleVersion string) string {
	if oracleVersion == consts.Oracle18c {
		return consts.SourceOracleXeInventory
	}
	return consts.SourceOracleInventory
}
