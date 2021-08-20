# El Carro: The Oracle Operator for Kubernetes

[![Go Report Card](https://goreportcard.com/badge/github.com/GoogleCloudPlatform/elcarro-oracle-operator)](https://goreportcard.com/report/github.com/GoogleCloudPlatform/elcarro-oracle-operator)

# Run Oracle on Kubernetes with El Carro

El Carro is a new project that offers a way to run Oracle databases in
Kubernetes as a portable, open source, community driven, no vendor lock-in
container orchestration system. El Carro provides a powerful declarative API for
comprehensive and consistent configuration and deployment as well as for
real-time operations and monitoring.

## High Level Overview

El Carro helps you with the deployment and management of Oracle database
software in Kubernetes. You must have appropriate licensing rights to allow you
to use it with El Carro (BYOL).

With the current release, you download the El Carro installation bundle, stage
the Oracle installation software, create a containerized database image (with or
without a seed database), and then create an Instance (known as CDB in Oracle
parlance) and add one or more Databases (known as PDBs).

After the El Carro Instance and Database(s) are created, you can take
snapshot-based or RMAN-based backups and get basic monitoring and logging
information. Additional database services will be added in future releases.

### License Notice

You can use El Carro to automatically provision and manage Oracle Database
Express Edition (XE) or Oracle Database Enterprise Edition (12c and 19c). In
each case, it is your responsibility to ensure that you have appropriate
licenses to use any such Oracle software with El Carro.

Please also note that each El Carro “database” will create a pluggable database,
which may require licensing of the Oracle Multitenant option.

Oracle and Java are registered trademarks of Oracle and/or its affiliates. Other
names may be trademarks of their respective owners.

### Quickstart

We recommend starting with the quickstart first, but as you become more familiar
with El Carro, consider trying more advanced features by following the user
guides linked below.

If you have a valid license for Oracle 12c EE or 19c EE and would like to get
your Oracle database up and running on Kubernetes, you can follow the
[quickstart guide for Oracle 12c](docs/content/quickstart-12c-ee.md) or the
[quickstart guide for Oracle 19c](docs/content/quickstart-19c-ee.md).

As an alternative to Oracle 12c EE or 19c EE, you can use
[Oracle 18c XE](https://www.oracle.com/database/technologies/appdev/xe.html)
which is free to use by following the
[quickstart guide for Oracle 18c XE](docs/content/quickstart-18c-xe.md) instead.

If you prefer to run El Carro locally on your personal computer, you can follow
the [user guide for Oracle on minikube](docs/content/minikube.md) or the
[user guide for Oracle on kind](docs/content/kind.md).

### Preparation

To prepare the El Carro download and deployment, follow
[this guide](docs/content/preparation.md).

### Provisioning

El Carro helps you to easily create, scale, and delete Oracle databases.

Firstly, you need to
[create a containerized database image](docs/content/provision/image.md).

You can optionally create a default Config to set namespace-wide defaults for
configuring your databases, following
[this guide](docs/content/provision/config.md).

Then you can create Instances (known as CDBs in Oracle parlance), following
[this guide](docs/content/provision/instance.md). Afterward, create Databases
(known as PDBs) and users following
[this guide](docs/content/provision/database.md).

### Backup and Recovery

El Carro provides both storage snapshot based backup/restore and Oracle native
RMAN based backup/restore features to support your database backup and recovery
strategy.

After the El Carro Instance and Database(s) are created, you can create storage
snapshot based backups, following
[this guide](docs/content/backup-restore/snapshot-backups.md).

You can also create Oracle native RMAN based backups, following
[this guide](docs/content/backup-restore/rman-backups.md).

To restore from a backup, follow
[this guide](docs/content/backup-restore/restore-from-backups.md).

### Data Import & Export

El Carro provides data import/export features based on Oracle Data Pump.

To import data to your El Carro database, follow
[this guide](docs/content/data-pump/import.md).

To export data from your El Carro database, follow
[this guide](docs/content/data-pump/export.md).

### What's More?

There are more features supported by El Carro and more to be added soon! For
more information, check [logging](docs/content/monitoring/logging.md),
[monitoring](docs/content/monitoring/monitoring.md),
[connectivity](docs/content/monitoring/connectivity.md),
[UI](docs/content/monitoring/ui.md), etc.

## Contributing

You're very welcome to contribute to the El Carro Project!

We've put together a set of contributing and development guidelines that you can
review in [this guide](docs/contributing.md).

## Support

To report a bug or log a feature request, please open a
[GitHub issue](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/issues)
and follow the guidelines for submitting a bug.

For general questions or community support, we welcome you to join the
[El Carro community mailing list](https://groups.google.com/forum/#!forum/el-carro)
and ask your question there.
