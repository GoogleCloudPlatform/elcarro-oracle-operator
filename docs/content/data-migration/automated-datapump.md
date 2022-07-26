# Automated Data Migration using Data Pump

This guide provides steps to migrate data from your Oracle 12.2 or 19.3 CDB into
an El Carro instance using El Carro "Import" resource. Migration using datapump
is performed one PDB at a time. Repeat following steps for each PDB in your
source database to do a full data migration.

## Before you begin

The following variables will be used in this guide:

```sh
$ export PROJECT_ID=<your GCP project id>
$ export SERVICE_ACCOUNT=<fully qualified name of the compute service account to be used by El Carro (i.e. SERVICE_ACCOUNT@PROJECT_NAME.iam.gserviceaccount.com)>
$ export PATH_TO_EL_CARRO_RELEASE=<the complete path to directory that contains the downloaded El Carro release>
$ export GCS_BUCKET=<your globally unique Google Cloud Storage bucket name>
$ export CDBNAME=<Source CDB name to migrate data from. For example: MYDB>
$ export PDBNAME=<Source PDB name to migrate data from. For example: pdb1>
$ export PDB_ADMIN_PW=<Source PDB admin user password. For example: TestPassword-1>
```

## Steps

1.  Build either a seeded or unseeded database image following the
    [image build guide](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/blob/main/docs/content/provision/image.md).

    For example, run the following script to build an 12.2 seeded image using
    Google Cloud Build.

    ```sh
    cd ${PATH_TO_EL_CARRO_RELEASE}/dbimage
    ./image_build.sh --install_path=gs://${GCS_BUCKET}/install --db_version=12.2 --create_cdb=true --cdb_name=${CDBNAME} --mem_pct=45 --no_dry_run --project_id=${PROJECT_ID}
    ```

2.  Create an El Carro instance as the data migration target following the
    [instance provisioning guide](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/blob/main/docs/content/provision/instance.md).

    Remember to update the following specs when preparing the instance CR
    manifest:

    *   Update the `instance.spec.images.service` to point to the location of
        the database container image that you created in the previous step.
    *   Update the `instance.spec.cdbName` to source CDB name.
    *   Update the `instance.spec.version` to source CDB version, either 12.2 or
        19.3.

3.  Once the El Carro instance is ready to use, create an El Carrro database
    following the
    [database provisiong guide](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/blob/main/docs/content/provision/database.md).

    Remember to update the following specs when preparing the database CR
    manifest:

    *   Update the `database.spec.name` to ${PDBNAME}.
    *   Update the `database.spec.adminPassword` to ${PDB_ADMIN_PW}.
    *   Leave the `database.spec.users` empty.

4.  Assume the complete database dump file from a source PDB is ready, create an
    El Carro import CR following the
    [datapump import guide](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/blob/main/docs/content/data-pump/import.md).

    Import is complete once import CR's ReadyStatus reaches `ImportComplete`.

5.  Repeat steps 3-4 for other PDBs you have to do a full data migration.
