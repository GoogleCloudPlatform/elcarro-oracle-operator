# Manual Data Migration through Data Pump

This guide provides steps to migrate data from your Oracle 12.2 or 19.3 CDB into
an El Carro instance manually. Migration using datapump is performed one PDB at
a time. Repeat following steps for each PDB in your source database to do a full
data migration.

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

4.  Stage existng dump file to the target El Carro instance.

    See
    [Oracle official Data Pump playbook](https://oracle-base.com/articles/10g/oracle-data-pump-10g)
    regarding how to create a complete database dump.

    Below is an example expdp command to run in source database to create a dump
    file:

    ```sh
    SQL> CREATE OR REPLACE DIRECTORY dpdump AS '/u01/app/oracle/oradata/dpdump';

    Directory created.

    $ expdp system/password@db10g full=Y directory=TEST_DIR
    ```

    Assume a complete database dump file from a source PDB is ready, copy it to
    the El Carro instance database container, see
    [how to copy file/dir](../database-env.md#to-copy-a-filedir-to-el-carro-database-environment-from-local-environment).

    ```sh
    $ export BACKUP_PATH=/u02/dpdump
    $ kubectl exec -it <database pod name> -c oracledb -n ${NS} -- bash -c "mkdir -p ${BACKUP_PATH}"
    $ kubectl cp <path to the backup> <database pod name>:${BACKUP_PATH} -c oracledb -n ${NS}
    ```

5.  Import dump file to El Carro database which is created in step 3.

    *   Get a shell to the target instance database container. See
        [how to access database container](../database-env.md#to-get-a-shell-to-el-carro-database-container).
    *   Set env variables, see
        [El Carro database container useful env variables for Oracle 12.2](../database-env.md#oracle-122-env-variables-for-the-el-carro-database-container).
    *   Invoke impdp utility

    ```sh
    SQL> CREATE DIRECTORY dpdump AS '/u02/dpdump';

    Directory created.

    $ impdp pdbadmin/adminpassword@localhost:6021/pdb1.graybox.gke full=yes directory=dpdump
    ```

6.  Repeat steps 3-5 for other PDBs you have to do a full data migration.
