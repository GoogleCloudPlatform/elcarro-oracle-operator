# (Optional) Creating a Default Config

This is an optional step designed to set namespace-wide (and if a single
namespace is used, then cluster-wide) defaults. The default configuration is
designed to achieve the following:

*   Ensure consistency across all the subsequent Instance, Database, Backup
    manifests because the parameters would be taken from the Config created in
    this step.

*   Make the subsequent manifests smaller because the common parameters would be
    taken from a default config, for example: storage class, location of a
    database image, disk type and sizes, etc.

## Before you begin

The following variables will be used in this quickstart:

```sh
export PROJECT_ID=<your GCP project id>
export NS=<Namespace to which your config should be applied. This is typically the namespace where your database instances are running. For example: db>
export PATH_TO_EL_CARRO_RELEASE=<the complete path to the downloaded release directory>
```

You should set these variables in your environment.

To get El Carro up and running, you need to do one the following:

1.  Prepare a Config CR Manifest

    Depending on the deployment platform you are using the default Config
    manifests should look like one of the following examples:

    GCP:

    ```sh
    $ cat ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_config_gcp2.yaml
    apiVersion: oracle.db.anthosapis.com/v1alpha1
    kind: Config
    metadata:
      name: config
    spec:
      images:
        # Replace below with the actual URIs hosting the service agent images.
        service: "gcr.io/${PROJECT_ID}/oracle-database-images/oracle-12.2-ee-unseeded"
        config: "gcr.io/${PROJECT_ID}/oracle.db.anthosapis.com/configagent:latest"
      platform: "GCP"
      disks: [
      {
        name: "DataDisk",
        type: "pd-standard",
        size: "100Gi",
      },
      {
        name: "LogDisk",
        type: "pd-standard",
        size: "150Gi",
      }
      ]
      volumeSnapshotClass: "csi-gce-pd-snapshot-class"
    ```

    For the Preview release El Carro relies on three storage volumes to host the
    following:

    *   Oracle binary tree.
    *   The data files.
    *   The archive redo logs and the RMAN backups.

    The type of storage (for example: PD vs. PD-SSD) and the size of each volume
    can be defined here in the Config manifest. Otherwise you would need to
    specify it for each of the Instance manifests. El Carro currently only
    supports a single Instance per cluster/namespace, but it's a good practice
    to set these attributes globally in the Config CR.

1.  Submit the Config CR

    After completing the Config manifest, submit it to the local cluster as
    follows:

    ```sh
    $ kubectl apply -f ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_config_gcp2.yaml -n ${NS}
    ```

1.  (Optional) Review the Config CR

    ```sh
    $ kubectl get configs -n $NS
    NAME             PLATFORM    DISK SIZES   STORAGE CLASS   VOLUME SNAPSHOT CLASS
    config           GCP                                      csi-gce-pd-snapshot-class

    $ kubectl describe config config -n $NS
    Name:         config
    Namespace:    db
    Labels:       <none>
    Annotations:  <none>
    API Version:  oracle.db.anthosapis.com/v1alpha1
    Kind:         Config
    Metadata:
      Creation Timestamp:  2020-09-05T03:26:31Z
      Generation:          1
      Resource Version:    17692020
      Self Link:           /apis/oracle.db.anthosapis.com/v1alpha1/namespaces/db/configs/config
      UID:                 a4883c72-ab65-4c56-9f06-df8ff68b526c
    Spec:
      Disks:
        Name:  DataDisk
        Size:  100Gi
        Type:  pd-standard
        Name:  LogDisk
        Size:  150Gi
        Type:  pd-standard
      Images:
        Config:               gcr.io/${PROJECT_ID}/oracle.db.anthosapis.com/configagent:latest
        Service:              gcr.io/${PROJECT_ID}/oracle-database-images/oracle-12.2ee-unseeded
      Platform:               GCP
      Volume Snapshot Class:  csi-gce-pd-snapshot-class
    Events:                   <none>
    ```
