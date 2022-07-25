# Point-in-Time Recovery

Point-in-time recovery(PITR) helps you recover an El Carro instance to a specific point in time. For example, if an error causes a loss of data, you can recover a database to its state before the error occurred.

This guide describes how to use point-in-time recovery on your El Carro instance.

## Before you begin

The following variables will be used in this guide:

```sh
$ export PROJECT_ID=<your GCP project id>
$ export SERVICE_ACCOUNT=<fully qualified name of the compute service account to be used by El Carro (i.e. SERVICE_ACCOUNT@PROJECT_NAME.iam.gserviceaccount.com)>
$ export PATH_TO_EL_CARRO_RELEASE=<the complete path to directory that contains the downloaded El Carro release>
$ export CDBNAME=<Source CDB name to migrate data from. For example: MYDB>
$ export PDBNAME=<Source PDB name to migrate data from. For example: pdb1>
$ export NAMESPACE=<kubernetes namespace where the instance was created>
```

## Enable point-in-time recovery

When you create a new El Carro instance following the [instance provisioning guide](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/blob/main/docs/content/provision/instance.md), point-in-time recovery is disabled by default.

This procedure enables point-in-time recovery for an existing El Carro instance.

1. Prepare a [Google Cloud Storage bucket](https://cloud.google.com/storage/docs/creating-buckets) to store backups and archive logs, for example: "gs://mydb-pitr-bucket". It's recommended to use separate bucket for each El Carro instance.
    ```shell
    export PITR_STORAGE_URI=<prepared GCS bucket. For example: "gs://mydb-pitr-bucket">
    ```

2. Set permissions for El Carro operator to access the bucket.

   a. If you're not using workload identity, use the Compute Engine default service account for GCS access.

   b. If you have enabled workload identity on GKE, you can configure the Kubernetes service account to act as a Google service account.

   You can run the following command to see whether workload identity is enabled for the GKE cluster or not:
    ```sh
    gcloud container clusters describe $CLUSTER --zone=$ZONE  --project=$PROJECT_ID | grep workload

   workloadMetadataConfig:
      workloadMetadataConfig:
   workloadIdentityCon fig:
      workloadPool: $PROJECT.svc.id.goog
    ```

   Grant permissions using the appropriate service account:

    ```shell
    gsutil iam ch serviceAccount:$SERVICE_ACCOUNT:admin $PITR_STORAGE_URI
    ```

   TIPs: you can also use the [configure-service-account.sh](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/blob/main/hack/configure-service-account.sh) script to find out which service account to grant permission for if workload identity is disabled. Or specify an existing service account to for El Carro to use if workload identity is enabled.
   ```shell
   configure-service-account.sh --cluster_name CLUSTER_NAME --gke_zone GKE_ZONE [--service_account SERVICE_ACCOUNT_[NAME|EMAIL]] [--namespace NAMESPACE]
   ```

3. Prepare and apply an El Carro PITR CR.

   Edit the following manifest by replacing "mydb" with the El Carrro Instance CR name, replacing the "storageURI" with the bucket you prepared in step 1.
   You can also optionally adjust the backup frequency by specifying a [cron schedule expression](https://en.wikipedia.org/wiki/Cron), the default value is every 4 hours.
    ```shell
    cat $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_pitr.yaml
    ```

    ```yaml
    apiVersion: oracle.db.anthosapis.com/v1alpha1
    kind: PITR
    metadata:
      name: mydb-pitr
    spec:
      images:
        agent: "gcr.io/elcarro/oracle.db.anthosapis.com/pitragent:latest"
      instanceRef:
        name: "mydb"
      storageURI: "gs://mydb-pitr-bucket"
      # Uncomment and change the backupSchedule value for customized backup schedule.
      # For allowed syntax, see en.wikipedia.org/wiki/Cron and godoc.org/github.com/robfig/cron.
      # Default to backup every 4 hours if not specified.
      # backupSchedule: "0 */4 * * *"
    ```

   After the manifest is ready, submit it to the cluster as follows:

    ```shell
    kubectl apply -f $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_pitr.yaml -n $NAMESPACE
    ```
   NOTE: The PITR CR should be created in the same namespace as the Instance CR and you should make sure there is at most one PITR CR for an El Carro Instance.


## Monitor recovery window

El Carro presents avaiable recovery windows in two ways: range of SCNs and range of times. You can monitor the current available recovery window for your instance in status of the associated PITR CR.

```shell
kubectl get pitrs.oracle.db.anthosapis.com/mydb-pitr -n $NAMESPACE -o json | jq .status
```

Here is an example output:
```shell
status:
  availableRecoveryWindowSCN:
  - begin: "1537472"
    end: "1537805"
  availableRecoveryWindowTime:
  - begin: "2021-09-10T06:27:49Z"
    end: "2021-09-10T06:36:13Z"
  backupTotal: 1
```

## Perform point-in-time recovery

A point-in-time recovery can either restore the instance in place or bring up a new instance.

To restore the original instance, add the `instance.spec.restore` section in the original Instance manifest.

To bring up a new instance, prepare a new Instance manifest with the `instance.spec.restore` section added.

The mandatory attributes to add are:
* `instance.spec.restore.force`: specify value as "True".
* `instance.spec.restore.requestTime`: specify value as current time. This field works as a safeguard to prevent restore from being triggered for the second time: any restore with `requestTime` no later than the previously used value will be ignored.
* `instance.spec.restore.backupType`: specify value as "Physical".
* `instance.spec.restore.pitrRestore.pitrRef.name`: specify value as the PITR CR name.
* `instance.spec.restore.pitrRestore.pitrRef.namespace`: specify value as the namespace of Instance CR.
* One of {`instance.spec.restore.pitrRestore.timestamp`|`instance.spec.restore.pitrRestore.scn`}: specify either a timestamp OR a SCN to be used as the target restore point. This value should be selected from current available recovery window as shown in PITR CR status.

You can also optionally specifiy an [incarnation](https://docs.oracle.com/cd/B19306_01/server.102/b14237/dynviews_1075.htm#REFRN30049) number if you'd like to restore to an incarnation different from current one.

Here is an example of a valid `instance.spec.restore` section to perform point-in-time recovery
```yaml
 restore:
    backupType: "Physical"
    pitrRestore:
      pitrRef:
        namespace: db
        name: mydb-pitr
      timestamp: 2021-09-10T06:34:07Z
    force: True
    # once applied, new requests with same or older time will be ignored,
    # current time can be generated using the command: date -u '+%Y-%m-%dT%H:%M:%SZ'
    requestTime: "2000-06-19T04:23:45Z"
```

Apply the prepared Instance manifest to trigger point-time-in recovery.

You can monitor the progress in instance status. Restore completed when Instance status reaches "Ready".
