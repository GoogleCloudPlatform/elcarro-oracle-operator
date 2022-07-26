# Automated Data Migration using Data Guard

## Assumptions

1.  By default, El Carro assumes the database image was containerized with
    `docker.io/oraclelinux:7-slim`, this document assumes the primary database
    is also running on the Oracle Linux 7 system. Other Linux systems with the
    same endianness may also work, but they need to be verified.
2.  Both the primary database and El Carro database are using 12.2 or 19.3
    enterprise version Oracle with the same patchsets. If you have different
    patchsets, build an unseeded image with primary database patchsets see
    [image build user guide](../provision/image.md).

## Summary

Automatic data migration using Oracle Data Guard is supported via a declarative
Kubernetes-based API.

The features is built on top of Oracle RMAN (for initial data dump) and Physical
Data Guard (for online data replication after the initial data dump)

Migration includes 2 phases.

Phase 1. El Carro operator creates a standby instance, replicates initial data
dump and sets up Oracle Data Guard based on `.spec.replicationSettings`. Then
Oracle Data Guard keeps replicating data from the primary database to the El
Carro standby database.

Phase 2. Based on Data Guard status, you can decide when to promote the standby
instance by removing `.spec.replicationSettings`. Promotion will cleanup Data
Guard configuration, activate the standby as a standalone database and configure
the database with El Carro required settings (listeners, common users). After
the promotion the data migration is complete, and you can use it as a regular El
Carro instance.

## Steps

1.  Configure primary database

    You need to configure below settings before using automated data migration.

    Required settings:

    *   Ensure the primary database `"FORCE_LOGGING"` and `"ARCHIVELOG"` mode
        are enabled.

    *   Ensure the primary database parameter `"remote_login_passwordfile"` is
        either `"SHARED"` or `"EXCLUSIVE"`.

    *   Ensure there is network connectivity between the primary database and El
        Carro environment.

    Data migration preflight check validates required settings and reports them
    in instance CR status.

    For example

    ```sh
    $ kubectl get instance mydb -n db -o
    custom-columns=StandbyDRReady:'{.status.conditions[?(@.type=="StandbyDRReady")]}'
    -w StandbyDRReady

    map[lastTransitionTime:2021-06-24T10:48:44Z
    message:validate replication settings failed INVALID_LOGGING_SETUP: Primary
    database is not in FORCE_LOGGING mode reason:StandbyDRVerifyFailed
    status:False type:StandbyDRReady]
    ```

    Fix errors reported by preflight check. El Carro operator will automatically
    retry and continue data migration setup.

    Optional settings:

    *   Add standby log files, they must mirror your regular log files in size
        and number of threads. You should have n+1 members per thread.

        For standby database, El Carro helps you add standby log files if they
        are not replicated in RMAN duplication.

        Without standby log files in primary, Data Guard will show warnings.

2.  Prepare a standby instance CR Manifest

    El Carro standby instances are created from yaml configuration files. We
    have provided an example of this configuration file
    [v1alpha1_instance_standby.yaml](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/blob/main/oracle/config/samples/v1alpha1_instance_standby.yaml).

    For the example in this documentation:

    ```yaml
    apiVersion: oracle.db.anthosapis.com/v1alpha1
    kind: Instance
    metadata:
      name: mydb
    spec:
      type: Oracle
      version: "19.3"
      edition: Enterprise
      dbUniqueName: "GCLOUD_gke"
      dbDomain: "gke"
      services:
        Backup: true
        Monitoring: true
        Logging: true
    # Fill out replicationSettings section based on the instructions.
      replicationSettings:
        primaryHost: ""
        primaryPort: 6021
        primaryServiceName: ""
        primaryUser:
          name: "sys"
          gsmSecretRef:
            projectId: ${PROJECT_ID}
            secretId: ""
            version: ""
        passwordFileURI: ""
        backupURI: ""

      images:
        # Replace below with the actual URIs hosting the service agent images.
        # Use unseeded images to set up standby instance.
        service: "gcr.io/${PROJECT_ID}/oracle-database-images/oracle-19.3-ee-unseeded"

      sourceCidrRanges: [0.0.0.0/0]
      # Oracle SID character limit is 8, anything > gets truncated by Oracle
      cdbName: "GCLOUD"
    ```

    Required specifications:

    *   Update the `.spec.version` to match with the primary database version
        (12.2 or 19.3).

    *   Update the `.spec.dbUniqueName` to specify `"DB_UNIQUE_NAME"` for the
        standby database.

    *   Update the `.spec.images.service` to point El Carro to the location of
        the unseeded database container image that you created, see
        [image build user guide](../provision/image.md#using-google-cloud-build-to-create-a-containerized-oracle-database-image-recommended).

    *   Update the `.spec.cdbName` to match with the primary database CDB name.

    *   Update `.spec.replicationSettings` to match with the primary database
        environment.

        *   `.spec.replicationSettings.primaryHost`: The IPv4 or DNS address for
            the primary database listener.

        *   `.spec.replicationSettings.primaryPort`: The port for the primary
            database listener.

        *   `.spec.replicationSettings.primaryServiceName`: The service name of
            the primary database on the listener at primaryHost:primaryPort.

        *   `.spec.replicationSettings.primaryUser.name`: The name of the user
            account on the primary database server that has sysdba privileges.
            Currently, only "sys" is supported.

        *   `.spec.replicationSettings.primaryUser.gsmSecretRef`: A reference to
            a remote [GSM](https://cloud.google.com/secret-manager/docs) secret,
            where El Carro can retrieve primary user credentials to authenticate
            to the primary database.

            In the current version, we support
            [Google Secret Manager](https://cloud.google.com/secret-manager/docs).
            `.gsmSecretRef.projectId` `.gsmSecretRef.secretId`
            `.gsmSecretRef.version` are used to identify a
            [secret version](https://cloud.google.com/secret-manager/docs/reference/rpc/google.cloud.secretmanager.v1#google.cloud.secretmanager.v1.SecretManagerService.AccessSecretVersion).

            See [create a GSM secret](#create-a-gsm-secret) for detailed
            instruction.

        *   `.spec.replicationSettings.passwordFileURI`: A URI to a copy of the
            primary's password file for establishing a Data Guard connection.
            Currently only gs:// (GCS) schemes are supported.

            See
            [Upload an Oracle password file](#upload-an-oracle-password-file)
            for detailed instruction.

    Optional specifications:

    *   Update the `.spec.dbDomain` to specify `"DB_DOMAIN"` for the standby
        database, El Carro will keep using this value after promotion. The
        default value is `""`.

    *   Update the `.spec.replicationSettings.backupURI` to specify the URI to a
        copy of the primary's RMAN full backup. Standby will be created from
        this backup when provided. Currently only gs:// (GCS) schemes are
        supported. The default value is `""` and the standby will be created
        from active duplication.

        See [prepare a primary backup](#prepare-a-primary-backup) for detailed
        instruction.

3.  Submit the standby instance CR

    After completing the manifest, submit it to the local cluster as follows:

    ```sh
    export NS=<namespace of user choice, for example: "db">
    kubectl apply -f config/samples/v1alpha1_instance_standby.yaml -n $NS
    ```

4.  Wait for Data Guard to be active

    ```sh
    kubectl get instance mydb -n $NS -o custom-columns=StandbyDRReady:'{.status.conditions[?(@.type=="StandbyDRReady")]}' -w
    ```

    Once the output shows

    ```
    message:Data Guard data replication in progress reason:StandbyDRDataGuardReplicationInProgress status:False type:StandbyDRReady
    ```

    The Data Guard is active.

    You can check Data Guard status in instance CR by running the following
    command:

    ```sh
    kubectl get instance mydb -n $NS -o custom-columns=DataGuard:'{.status.dataGuardOutput}' -w
    ```

    instance CR will be refreshed every one minute by reading standby Data Guard
    configuration.

    See [data migration status](#data-migration-status).

5.  Promote the standby instance

    You can promote the instance by removing `.spec.replicationSettings` from
    the instance CR.

    Update the instance CR file, ensure `.spec.replicationSettings` are removed
    or commented out. Submit the updated CR file with

    ```sh
    kubectl apply -f config/samples/v1alpha1_instance_standby.yaml -n $NS
    ```

6.  Wait for instance to be ready

    You can monitor the state of the data migration by running the following
    command:

    ```sh
    kubectl get instance mydb -n $NS -o custom-columns=StandbyDRReady:'{.status.conditions[?(@.type=="StandbyDRReady")]}' -w
    ```

    Note StandbyDRReady fields denote the status of data migration, Once it
    turns `True`, the data migration has been completed successfully. See
    [data migration status](#data-migration-status).

    Once data migration has been completed, you can monitor the state of the
    Instance CR by running the following command:

    ```sh
    kubectl get instances -n $NS -w
    ```

    Note DBReadyStatus fields denote the status of the underlying database
    status. Once it turns `True`, the Instance is ready to use.

## Appendix

### Data migration status

Data migration uses the condition StandbyDRReady (Kubernetes JSONPath is
`.status.conditions[?(@.type=="StandbyDRReady")]`) to show migration progress.

The status (`.status.conditions[?(@.type=="StandbyDRReady")].status`) of
StandbyDRReady indicates whether the data migration is complete. `False`
indicates the data migration is in progress or failed. `True` indicates the data
migration has been completed successfully.

The reason (`.status.conditions[?(@.type=="StandbyDRReady")].reason`) of the
StandbyDRReady condition keeps track states of data migration. The message
(`.status.conditions[?(@.type=="StandbyDRReady")].message`) of the
StandbyDRReady shows error messages or action items to help you track and
troubleshoot.

*   Reason: StandbyDRVerifyFailed

    StandbyDRVerifyFailed indicates the El Carro preflight verifications failed.
    Try fix message reported issues, the El Carro operator will automatically
    retry verifications every minute until success.

*   Reason: StandbyDRCreateFailed

    El Carro creates a standby database for the primary database.

    StandbyDRCreateFailed indicates that the standby database creation failed,
    this is a final error state. We suggest cleanup and create a new standby
    instance for retry or report it to us, see
    [Contributing Guidelines](../../contributing.md#contributing-guidelines).

*   Reason: StandbyDRSetUpDataGuardFailed

    El Carro connects to the primary server with Oracle DGMGRL and adds the
    standby database as a physical standby.

    StandbyDRSetUpDataGuardFailed indicates that set up Data Guard configuration
    failed. This is not a final error state, El Carro keeps retrying every
    minute. If it is caused by a transient issue, El Carro retry may recover and
    continue data migration. If it is caused by a non-transient issue, for
    example the password file is outdated, you need to reupload the latest
    password file to fix the issue, El Carro will automatically retry and
    continue data migration.

*   Reason: StandbyDRDataGuardReplicationInProgress

    StandbyDRDataGuardReplicationInProgress indicates the Data Guard is active.
    The instance stays in this state until you decide to promote. El Carro
    updates dataGuardOutput (`.status.dataGuardOutput`) by reading the standby
    instance DGMGRL configuration every one minute.

*   Reason: StandbyDRPromoteFailed

    El Carro cleans up the Data Guard configuration on the primary server with
    DGMGRL, and then activates the standby database.

    StandbyDRPromoteFailed indicates that standby database promotion failed.
    This is not a final error state, El Carro keeps retrying every one minute.
    Based on the message, you may try to fix the issues in the database
    container, see
    [how to access database container](../database-env.md#to-get-a-shell-to-el-carro-database-container).
    El Carro will automatically retry and continue.

*   Reason: StandbyDRBootstrapFailed

    El Carro bootstraps the standby instance to ensure the standby instance have
    required settings to function as a regular instance.

    StandbyDRBootstrapFailed indicates that the bootstrap failed. This is not a
    final error state, El Carro keeps retrying every one minute. Bootstrap
    involves El Carro specific settings, if the bootstrap keeps failing, you may
    report it to us, see
    [Contributing Guidelines](../../contributing.md#contributing-guidelines).

*   Reason: StandbyDRBootstrapCompleted

    StandbyDRBootstrapCompleted indicates that bootstrap has been completed
    successfully. This is the final success state of data migration.

### Create a GSM secret

1.  Prepare a file to store the password

    ```sh
    export PW_FILE=<path to the file which stores the password>
    ```

2.  Create a new secret

    Tip: Ensure there is no newline at the end of $PW_FILE.

    ```sh
    export SECRET_ID=<secret id>
    gcloud secrets create $SECRET_ID --data-file="$PW_FILE" --project=$PROJECT_ID
    ```

3.  Verify the created secret

    ```sh
    gcloud secrets versions access 1 --secret="$SECRET_ID" --project=$PROJECT_ID
    ```

4.  Find the GCP service account for El Carro operator in GKE, see
    [instruction](../database-env.md#configure-gcp-service-account-for-el-carro)

    ```sh
    export OPERATOR_GCP_SA=<GCP service account for El Carro>
    ```

5.  Grant the read privilege to the El Carro operator GCP service account

    ```sh
    gcloud secrets add-iam-policy-binding $SECRET_ID --member=serviceAccount:$OPERATOR_GCP_SA --role='roles/secretmanager.secretAccessor' --project=$PROJECT_ID
    ```

6.  Update `.spec.replicationSettings.primaryUser.gsmSecretRef` to point to the
    secret

    ```yaml
    primaryUser:
      name: "sys"
      gsmSecretRef:
        projectId: $PROJECT_ID
        secretId: $SECRET_ID
        version: "1"
    ```

    Then El Carro is able to retrieve the password from the secret.

See
[GSM doc](https://cloud.google.com/secret-manager/docs/creating-and-accessing-secrets)
for more instructions.

### Upload an Oracle password file

1.  Transfer the primary database password file to a
    [GCS bucket](https://cloud.google.com/storage). If needed, a new GCS bucket
    can be created as follows:

    ```sh
    export GCS_BUCKET=<your globally unique Google Cloud Storage bucket name>
    gsutil mb gs://$GCS_BUCKET
    gsutil cp <path to the Oracle database password file> gs://$GCS_BUCKET/password/orapw<primary SID>
    ```

2.  Find the GCP service account for El Carro instance in GKE, see
    [instruction](../database-env.md#configure-gcp-service-account-for-el-carro)

    ```sh
    export INSTANCE_GCP_SA=<GCP service account for El Carro>
    ```

3.  Grant the IAM read privilege (roles/storage.objectViewer) to the El Carro
    GCP service account

    ```sh
    gsutil iam ch serviceAccount:${INSTANCE_GCP_SA}:roles/storage.objectViewer gs://$GCS_BUCKET
    ```

4.  Update `.spec.replicationSettings.passwordFileURI` to point to the uploaded
    password file

    ```yaml
    replicationSettings:
    ...
        passwordFileURI: gs://$GCS_BUCKET/password/orapw<primary SID>
    ```

    Then El Carro is able to download the password file.

### Prepare a primary backup

1.  Transfer the primary database full backup to a
    [GCS bucket](https://cloud.google.com/storage). If needed, a new GCS bucket
    can be created as follows:

    ```sh
    export GCS_BUCKET=<your globally unique Google Cloud Storage bucket name>
    gsutil mb gs://$GCS_BUCKET
    gsutil -m cp <path to the full backup> gs://$GCS_BUCKET/backup
    ```

2.  Find the GCP service account for El Carro in GKE, see
    [instruction](../database-env.md#configure-gcp-service-account-for-el-carro)

    ```sh
    export INSTANCE_GCP_SA=<GCP service account for El Carro>
    ```

3.  Grant the IAM read privilege (roles/storage.objectViewer) to the El Carro
    GCP service account

    ```sh
    gsutil iam ch serviceAccount:${INSTANCE_GCP_SA}:roles/storage.objectViewer gs://$GCS_BUCKET
    ```

4.  Update `.spec.replicationSettings.backupURI` to point to the uploaded backup

    ```yaml
    replicationSettings:
    ...
        backupURI: gs://$GCS_BUCKET/backup
    ```

    Then El Carro is able to download the backup.

