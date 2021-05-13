# workflows

## Description
Operator kpt package

# SYNOPSIS

  kpt cfg set config/workflows namespace db
  kpt cfg set config/workflows services Backup Logging Monitoring
  kpt cfg set config/workflows dbimage "gcr.io/<your-gcp-project>/oracle_12ee_database"

# Description

Operator kpt package can be used to build declarative workflows for databases
with services that are based on common org DRY templates.
The templates then can then be hydrated for specific databases, env or fleet wide.
