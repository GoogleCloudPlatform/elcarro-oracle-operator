# Oracle Database Parameter update using El Carro

El Carro provides the ability to declaratively set/update the parameters of your
database.


## Overview

Parameters fall under two categories: **static** and **dynamic**. Updating
static parameters require the database to be restarted, whereas dynamic
parameters can be updated without taking the database offline.

You can update any parameter via the Instance CR (Custom Resource) except for
the parameters [listed](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/blob/main/oracle/controllers/instancecontroller/instance_controller_parameters.go#L37)
below:

- audit_file_dest
- audit_trail
- compatible
- control_files
- db_block_size
- db_recovery_file_dest
- diagnostic_dest
- dispatchers
- enable_pluggable_database
- filesystemio_options
- local_listener
- remote_login_passwordfile
- undo_tablespace
- log_archive_dest_1
- log_archive_dest_state_1
- log_archive_format
- standby_file_management

El Carro prohibits updates to these parameters due to the semi-managed nature of
the operator. Modifying these parameters would affect the behavior of the
control plane of El Carro.

## How to update parameters

Database parameters can be updated via the Instance CR manifest (YAML) of your
El Carro instance. El Carro only carries out updates to database parameters
within maintenance windows, which you define in your [Instance CR](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/tree/main/oracle/config/samples/v1alpha1_instance_parameter_update.yaml). To update database parameters, follow
the steps below.

1. Modify your Instance CR.

Under the instance spec, specify the parameters you'd like to update and their
new values. For example:

  ```sh
  $ cat config/samples/v1alpha1_instance_parameter_update.yaml

  apiVersion: oracle.db.anthosapis.com/v1alpha1
  kind: Instance
  metadata:
    name: mydb
  spec:
    type: Oracle
    version: "19.3"
    edition: Enterprise
    dbDomain: "gke"
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
    sourceCidrRanges: [ 0.0.0.0/0 ]
    images:
      # Replace below with the actual URIs hosting the service agent images.
      service: "gcr.io/${PROJECT_ID}/oracle-database-images/oracle-19.3-ee-seeded-${DB}"
    cdbName: ${DB}
    databaseResources:
      requests:
        memory: 4.0Gi
    parameters:
      cpu_count: "4"
      processes: "1800"
      sga_max_size: "7900M"
    maintenanceWindow:
      timeRanges:
      - start: "2021-01-01T00:00:00Z"
        duration: "87660h" # good till 2031
  ```

2.  Submit the updated instance CR to your cluster.

    After updating the instance CR manifest (YAML), submit it to your cluster
    as follows:

  ```sh
  $ export NS=<namespace where your instance was created, for example: "db">
  $ kubectl apply -f config/samples/v1alpha1_instance_parameter_update.yaml -n $NS
  ```

3. Wait for the parameter update operation to complete and for the instance to
   transition to a ready state.

   You can monitor the state of the update process by running the following
   command:

    ```sh
    kubectl -n $NS get instances -w
    ```

   If the parameter update operation succeeds, the output of the command above
   should look like:

  ```sh
  NAME   DB ENGINE   VERSION   EDITION      ENDPOINT      URL                  DB NAMES   BACKUP ID   READYSTATUS   READYREASON      DBREADYSTATUS   DBREADYREASON
  mydb   Oracle      19.3      Enterprise   mydb-svc.db   36.133.270.25:6021                          True          CreateComplete   True            CreateComplete
  mydb   Oracle      19.3      Enterprise   mydb-svc.db   36.133.270.25:6021                          True          CreateComplete   True            CreateComplete
  mydb   Oracle      19.3      Enterprise   mydb-svc.db   36.133.270.25:6021                          False         ParameterUpdateInProgress   True            CreateComplete
  mydb   Oracle      19.3      Enterprise   mydb-svc.db   36.133.270.25:6021                          True          CreateComplete              True            CreateComplete
  mydb   Oracle      19.3      Enterprise   mydb-svc.db   36.133.270.25:6021                          True          CreateComplete              True            CreateComplete
```



## Parameter rollbacks

After you initiate a parameter update request via the Instance CR, El Carro
takes a backup of the current values of the parameters you're attempting to
update so that rollbacks can be applied if anything goes wrong during the update
process.

When updating one or more parameters, El Carro follows an all-or-nothing
approach. El Carro will automatically roll back all parameters in an update
request if at least one of the parameters cannot be updated to the specified
value.