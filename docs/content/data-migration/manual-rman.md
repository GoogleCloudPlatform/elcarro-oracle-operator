# RMAN backup data migration

## Assumptions

1.  This document assumes the audience has some basic knowledge on Oracle RMAN
    backup and Oracle database
    [concepts](https://docs.oracle.com/en/database/oracle/oracle-database/21/bradv/rman-backup-concepts.html#GUID-B3380142-ABCD-437F-9E06-B219D74E6738).
2.  By default, El Carro assumes the database image was containerized with
    `docker.io/oraclelinux:7-slim`. This document assumes the source database is
    also running on the oracle linux 7 system. Other linux systems with the same
    endianness may also work, but they need to be verified.
3.  Both the source database and El Carro database are using 12.2 or 19.3
    enterprise version with the same patchsets. If you have different patchsets,
    build the image with source database patchsets. See [image build user guide](../provision/image.md).

## Steps

1.  Create an El Carro instance as the data migration target

    *   Prepare an instance CR manifest based on
        [the sample file](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/blob/main/oracle/config/samples/v1alpha1_instance_standby.yaml)

        Update the `.spec.version` to match with the primary database version
        (12.2 or 19.3).

        Update the `.spec.cdbName` to match with the source database name.

        Update the `.spec.images` service to an unseeded image gcr. For how to build an
        image, see
        [image build user guide](../provision/image.md)

        Uncomment `# mode: "ManuallySetUpStandby"`. With this specification, the
        operator will create an El Carro instance without database processes.

        Optionally update the  `.spec.dbUniqueName`, this is required if you plan to
        specify
        [DB_UNIQUE_NAME](https://docs.oracle.com/en/database/oracle/oracle-database/12.2/refrn/DB_UNIQUE_NAME.html#GUID-3547C937-5DDA-49FF-A9F9-14FF306545D8)
        for migrated database. See step 3 pfile preparation.

        Optionally update the `.spec.dbDomain`, this is required if you plan to specify
        [DB_DOMAIN](https://docs.oracle.com/en/database/oracle/oracle-database/12.2/refrn/DB_DOMAIN.html#GUID-8D30613F-3BC5-4B02-9574-F722DA6E3D44)
        for migrated database. See step 3 pfile preparation.

    *   Submit the instance CR

        Check the current context to ensure it points to the cluster you want to
        create the target instance.

        ```
        kubectl config current-context
        ```

        After completing the Instance manifest, submit it to the local cluster
        as follows:

        ```
        export NS=<namespace of user choice, for example: "db">
        export PATH_TO_EL_CARRO_RELEASE=<the complete path to the downloaded release directory>
        kubectl apply -f ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_instance_standby.yaml -n $NS
        ```

    *   Wait for instance to be ready

        You can monitor the state of the Instance CR is by running the following
        command:

        ```
        kubectl get instances.oracle.db.anthosapis.com -n $NS -w
        ```

        Note the ReadyStatus that denote the status of an El Carro Instance.
        Once it turns to "ManuallySetUpStandbyInProgress", the Instance is ready
        to perform data migration.

2.  Stage backup to the target El Carro instance

    Assume we have a full RMAN backup of the source database. Regarding how to
    create a full backup, you can refer to
    [Oracle documentation](https://docs.oracle.com/en/database/oracle/oracle-database/12.2/rcmrf/BACKUP.html#GUID-73642FF2-43C5-48B2-9969-99001C52EB50)
    or [playbooks](https://oracle-base.com/articles/9i/recovery-manager-9i) from
    oracle-base.com

    For the simple example in this document, in source database environment, you
    can run

    ```
    export ORACLE_HOME=<source database Oracle home>
    export ORACLE_SID=<source database sid>
    rman target /
    RMAN> CONFIGURE CHANNEL DEVICE TYPE DISK FORMAT '/u03/backup/backup%d_DB_%u_%s_%p';
    RMAN> run { BACKUP DATABASE include current controlfile PLUS ARCHIVELOG; }
    ```

    A full backup will be created in `/u03/backup/` .

    Copy backup pieces to the El Carro instance database container, see
    [how to copy file/dir](../database-env.md#to-copy-a-filedir-to-el-carro-database-environment-from-local-environment)

    ```
    export BACKUP_PATH=/u03/rman
    kubectl exec -it <database pod name> -c oracledb -n $NS -- bash -c "mkdir -p $BACKUP_PATH"
    kubectl cp <path to the backup> <database pod name>:$BACKUP_PATH -c oracledb -n $NS
    ```

3.  Create a standby database on the target El Carro instance

    Get a shell to the target instance database container. See
    [how to access database container](../database-env.md#to-get-a-shell-to-el-carro-database-container).

    Set env variables, See
    [El Carro database container useful env variables for Oracle 12.2](../database-env.md#oracle-122-env-variables-for-the-el-carro-database-container)
    [El Carro database container useful env variables for Oracle 19.3](../database-env.md#oracle-193-env-variables-for-the-el-carro-database-container)

    *   Prepare a pfile for standby database in `/u02/init.ora`

        ```
        # We suggest setting below parameters based on the source DB name.
        *.db_name=<var>db_name</var>
        *.audit_file_dest='/u02/app/oracle/admin/<var>db_name</var>/adump'
        *.control_files='/u02/app/oracle/oradata/<var>db_name</var>/control01.ctl'
        *.db_create_file_dest='/u02/app/oracle/oradata/<var>db_name</var>/'
        *.db_recovery_file_dest='/u03/app/oracle/fast_recovery_area/<var>db_name</var>’

        # We suggest setting file name convert parameters to relocate datafiles and
        # online log files on the target instance to /u02/app/oracle/oradata/<var>db_name</var>
        *.db_file_name_convert='<var>source database datafiles path</var>','/u02/app/oracle/oradata/<var>db_name</var>/'
        *.log_file_name_convert='<var>source database online logs path</var>','/u02/app/oracle/oradata/<var>db_name</var>/'

        # We suggest setting below parameters based on the kubernetes node capacity.
        # example value based on node machine with 7.5GB memory
        *.pga_aggregate_target=442M
        *.sga_target=1776M
        *.open_cursors=300

        # Below parameters are required for El Carro functionalities.
        *.common_user_prefix='gcsql$'
        *.enable_pluggable_database=true
        # *.compatible=19.3.0 for 19.3
        *.compatible=12.2.0
        *.remote_login_passwordfile='EXCLUSIVE'
        *.standby_file_management='AUTO'
        *.local_listener='(DESCRIPTION=(ADDRESS=(PROTOCOL=ipc)(KEY=REGLSNR_6021)))'

        # We suggest setting below parameters to match with instance CR manifest.
        # match with spec.disks LogDisk.size, El Carro default log disk size is 150GB
        *.db_recovery_file_dest_size=150G
        # match with instance spec.dbUniqueName, remove this parameter if spec.dbUniqueName is not specified
        *.db_unique_name=
        # match with instance spec.dbDomain, remove this parameter if spec.dbDomain is not specified
        *.db_domain=

        # New parameters can be added when necessary.
        # Proof of concept is recommended to verify if they are compatible with El Carro env.
        ```

    *   Create required directories specified by audit_file_dest, control_files,
        db_create_file_dest, db_recovery_file_dest and an El Carro config directory

        ```
        mkdir -p /u02/app/oracle/admin/<var>db_name</var>/adump
        mkdir -p /u02/app/oracle/oradata/<var>db_name</var>/
        mkdir -p /u03/app/oracle/fast_recovery_area/<var>db_name</var>
        mkdir -p /u02/app/oracle/oraconfig/<var>db_name</var>
        ```

    *   Start the Oracle instance in nomount

        ```
        SQL>  startup nomount pfile='/u02/init.ora';
        ```

4.  Restore and recover data from backup on the target instance

    You can follow
    [Oracle documentation](https://docs.oracle.com/en/database/oracle/oracle-database/12.2/rcmrf/DUPLICATE.html#GUID-E13D8A02-80F9-49A2-9C31-92DD3A795CE4)
    to perform backup-based RMAN duplication

    For the simple example in this document, you can run

    ```
    rman <<EOF
    connect AUXILIARY /
    DUPLICATE TARGET DATABASE FOR STANDBY
    BACKUP LOCATION '/u03/rman' DORECOVER NOFILENAMECHECK;
    EOF
    ```

    Optionally, you can use incremental backups to restore data incrementally
    for the standby database, by following existing playbooks, for example
    [Oracle documentation](https://support.oracle.com/knowledge/Oracle%20Database%20Products/1531031_1.html)
    Steps to perform for Rolling forward a standby database using RMAN
    incremental backup when datafile is added to primary.

5.  Promote the standby database on the target instance

    If there is no direct connection between the standby database and source
    database, we can activate the standby database directly

    ```
    SQL>  alter database activate physical standby database;
    SQL>  alter database open;
    SQL>  alter pluggable database all open;
    ```

6.  Bootstrap the El Carro instance

    *   Update the instance CR manifest, comment out or remove spec.mode

        ```
        apiVersion: oracle.db.anthosapis.com/v1alpha1
        kind: Instance
        metadata:
          name: mydb
        spec:
          type: Oracle
          ...
          # mode: "ManuallySetUpStandby"
        ```

    *   Submit the instance CR

        After updating the Instance manifest, submit it to the local cluster as
        follows:

        ```
        export NS=<namespace of user choice, for example: "db">
        export PATH_TO_EL_CARRO_RELEASE=<the complete path to the downloaded release directory>
        kubectl apply -f ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_instance_standby.yaml -n $NS
        ```

    *   Wait for instance to be ready

        You can monitor the state of the Instance CR is by running the following
        command:

        ```
        kubectl get instances.oracle.db.anthosapis.com -n $NS -w
        ```

        Note DBReadyStatus fields that denote the status of the underlying
        database status. Once it turns "True", the Instance is ready to use.

