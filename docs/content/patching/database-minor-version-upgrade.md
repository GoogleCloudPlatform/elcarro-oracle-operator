# Database Minor version upgrades in El Carro

El Carro automates the minor version database upgrade process. To ensure safety,
El Carro takes a snapshot backup of the database and then initiates the patching
process. If a failure occurs during the patching process, El Carro rolls back
the buggy image and resets the database contents and software to the
pre-patching state. The steps for creating a new patched database image and
updating the El Carro instance with the new image are as follows.

## Steps

1.  Create the patched service image

    *   Download the specific patch from the Oracle repository as described in
        [Oracle documentation](https://docs.oracle.com/en/database/oracle/oracle-database/19/ssdbi/downloading-and-installing-patch-updates.html)
    *   The patch zip filename will have the following naming convention
        p[patch_version]_[oracle_version]_[os]-[cpu_platform].zip

    *   Follow the steps specified in
        [Create Containerized Database Image](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/blob/main/docs/content/provision/image.md)
        to create a new containerized database image with the patch file
        obtained from the previous step.

2.  Update your Instance CR Manifest (YAML) with a link to your new service
    image, under the *images* section

    For example:

    ```yaml
    $ cat config/samples/v1alpha1_instance_patching.yaml

    apiVersion: oracle.db.anthosapis.com/v1alpha1
    kind: Instance
    metadata:
      name: mydb
    spec:
      type: Oracle
      version: "19.3"
      edition: Enterprise
      disks:
      - name: DataDisk
        size: 45Gi
        storageClass: "standard-rwo"
      - name: LogDisk
        size: 55Gi
        storageClass: "standard-rwo"
      services:
        Backup: true
        Monitoring: true
        Logging: true
        Patching: true
      sourceCidrRanges: [ 0.0.0.0/0 ]
      images:
        # Replace this with a newly patched image once your database is
        # created to begin the patching workflow. You must have a maintenance
        # window defined for patching to take effect.
        service: "gcr.io/${PROJECT_ID}/oracle-database-images/oracle-19.3-ee-seeded-${DB}"
      maintenanceWindow:
        timeRanges:
          start: "2021-01-01T00:00:00Z"
          duration: "87660h" # good till 2031
      dbDomain: "gke"
      cdbName: ${DB}
      databaseResources:
        requests:
          memory: 4.0Gi
    ```

3.  Submit the instance CR

    After completing the manifest, submit it to your cluster as follows:

    ```sh
    $ export NS=<namespace where your instance was created, for example: "db">
    $ kubectl apply -f config/samples/v1alpha1_instance_patching.yaml -n $NS
    ```

4.  Wait for the patching operation to complete and for the instance to
    transition to a ready state.

    You can monitor the state of the patching workflow by running the following
    command:

    If the patching operation completes successfully, the command above outputs:

    ```sh
    [2022-09-30 20:03:40] NAME   DB ENGINE   VERSION   EDITION      ENDPOINT      URL                  DB NAMES   BACKUP ID   READYSTATUS   READYREASON      DBREADYSTATUS   DBREADYREASON
    [2022-09-30 20:03:40] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021                          True          CreateComplete   True            CreateComplete
    [2022-09-30 20:04:47] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021                          True          CreateComplete   True            CreateComplete
    [2022-09-30 20:04:47] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831538726065   False         PatchingBackupStarted   True            CreateComplete
    [2022-09-30 20:05:47] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831538726065   False         PatchingBackupCompleted   True            CreateComplete
    [2022-09-30 20:05:47] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831538726065   False         DeploymentSetPatchingComplete   True            CreateComplete
    [2022-09-30 20:05:48] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831538726065   False         StatefulSetPatchingInProgress   True            CreateComplete
    [2022-09-30 20:05:49] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831538726065   False         StatefulSetPatchingInProgress   True            CreateComplete
    [2022-09-30 20:05:59] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831538726065   False         StatefulSetPatchingInProgress   True            CreateComplete
    [2022-09-30 20:09:36] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831538726065   False         StatefulSetPatchingComplete     True            CreateComplete
    [2022-09-30 20:10:33] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831538726065   False         DatabasePatchingInProgress      True            CreateComplete
    [2022-09-30 20:14:24] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831538726065   False         DatabasePatchingComplete        True            CreateComplete
    ```

    If the patching process fails for any reason the workflow automatically
    rolls back the patched image, the database contents are also be reverted to
    the pre-patching state.

    ```sh
    [2022-09-30 20:16:48] NAME   DB ENGINE   VERSION   EDITION      ENDPOINT      URL                  DB NAMES   BACKUP ID                               READYSTATUS   READYREASON      DBREADYSTATUS   DBREADYREASON
    [2022-09-30 20:16:48] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831538726065   True          CreateComplete   True            CreateComplete
    [2022-09-30 20:18:13] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831538726065   True          CreateComplete   True            CreateComplete
    [2022-09-30 20:18:13] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831512419495   False         PatchingBackupStarted   True            CreateComplete
    [2022-09-30 20:18:43] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831512419495   False         PatchingBackupCompleted   True            CreateComplete
    [2022-09-30 20:18:43] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831512419495   False         DeploymentSetPatchingComplete   True            CreateComplete
    [2022-09-30 20:18:44] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831512419495   False         StatefulSetPatchingInProgress   True            CreateComplete
    [2022-09-30 20:18:44] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831512419495   False         StatefulSetPatchingInProgress   True            CreateComplete
    [2022-09-30 20:18:50] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831512419495   False         StatefulSetPatchingInProgress   True            CreateComplete
    [2022-09-30 20:22:20] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831512419495   False         StatefulSetPatchingComplete     True            CreateComplete
    [2022-09-30 20:23:44] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831512419495   False         DatabasePatchingInProgress      True            CreateComplete
    [2022-09-30 20:24:58] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831512419495   False         DatabasePatchingFailure         True            CreateComplete
    [2022-09-30 20:24:58] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831512419495   False         PatchingRecoveryInProgress      True            CreateComplete
    [2022-09-30 20:24:58] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831512419495   False         PatchingRecoveryInProgress      True            CreateComplete
    [2022-09-30 20:25:19] mydb   Oracle      19.3      Enterprise   mydb-svc.db   35.202.109.30:6021              patching-backup-mydb20210831512419495   False         PatchingRecoveryInProgress      True            CreateComplete
    ```