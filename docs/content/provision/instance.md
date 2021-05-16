# Create El Carro Instance(s): Basic

This step depends on the previous one of successfully creating a containerized
database image in GCR. Once the image is ready, you need to tell El Carro the
location of that image in GCR as part of the Instance manifest. For more
advanced cases you can review the parameterized template manifests described in
[Appendix A](../custom-resources/instance.md).

1. Prepare an Instance CR Manifest

   El Carro instances are created from yaml configuration files. We have
   provided an example of this configuration file. As a bare minimum, update the
   Instance.Spec.Images.Service to
   point El Carro to the location of the database container image that you
   created in the previous step.

   ```sh
   cat ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_instance_custom_seeded.yaml
   apiVersion: oracle.db.anthosapis.com/v1alpha1
   kind: Instance
   metadata:
     name: mydb
   spec:
     type: Oracle
     version: "12.2"
     edition: Enterprise
     dbDomain: "gke"
     disks:
     - name: DataDisk
       size: 45Gi
       type: pd-standard
       storageClass: "csi-gce-pd"
     - name: LogDisk
       size: 55Gi
       type: pd-standard
       storageClass: "csi-gce-pd"
     services:
       Backup: true
       Monitoring: true
       Logging: true
     images:
       service: "gcr.io/${PROJECT_ID}/oracle-database-images/oracle-12.2-ee-seeded-${DB}"
     sourceCidrRanges: [0.0.0.0/0]
     databaseUID: 54321
     databaseGID: 54322
     # Oracle SID character limit is 8, anything > gets truncated by Oracle
     cdbName: ${DB}

     # Uncomment this section to trigger a restore.
     #  restore:
     #    backupType: "Snapshot" (or "Physical")
     #    backupId: "mydb-20200705-snap-996678001"
     #    force: True
     #    # once applied, new requests with same or older time will be ignored,
     #    # current time can be generated using the command: date -u '+%Y-%m-%dT%H:%M:%SZ'
     #    requestTime: "2000-01-19T01:23:45Z"
     #    # Physical backup specific attributes:
     #    dop: 2
     #    # The unit for time limit is minutes (but specify just an integer).
     #    timeLimit: 180
   ```

1. Submit the Instance CR

   After completing the Instance manifest, submit it to the local cluster as
   follows:

   ```sh
   export NS=<namespace of user choice, for example: "db">
   kubectl apply -f ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_instance_custom_seeded.yaml -n $NS
   ```

1. Review the Instance CR

   You can monitor the state of the Instance CR is by running the following
   command:

   ```sh
   kubectl get instances.oracle.db.anthosapis.com -n $NS -w
   ```

   Note the ReadyStatus and the DBReadyStatus fields that denote the status of
   an Instance K8s CR and the status of the underlying database instance
   respectively. Once both turn "True", the Instance is ready to use.

1. (Optional) List the database processes

   At this point a database instance should be fully operational. You can "exec"
   into a database container and inspect the background processes as described
   below:

```sh
kubectl get instances.oracle.db.anthosapis.com -n $NS
NAME     DB ENGINE   VERSION   EDITION      ENDPOINT        URL                  READYSTATUS   DBREADYSTATUS   DB NAMES
mydb     Oracle      12.2      Enterprise   mydb-svc.db     34.122.76.205:6021   True          True

kubectl exec -ti $(kubectl get pod -n $NS -l instance=mydb -o jsonpath="{.items[0].metadata.name}") -c oracledb -n $NS -- /bin/bash

[oracle@mydb-sts-0 /]source ~/MYDB.env
[oracle@mydb-sts-0 ~]sqlplus / as sysdba

SQL> select dbid, open_mode, database_role from v$database;

      DBID OPEN_MODE		DATABASE_ROLE
---------- -------------------- ----------------
1591708746 READ WRITE		PRIMARY

SQL> show pdbs

    CON_ID CON_NAME			  OPEN MODE  RESTRICTED
---------- ------------------------------ ---------- ----------
	 2 PDB$SEED			  READ ONLY  NO
```

## What's Next

Check out the [database provisioning guide](database.md) to learn how to create
a database (PDB) in this instance (CDB) using El Carro.