# Restore from a backup

An Instance can be restored from a `backups.oracle.db.anthosapis.com` resource
representing either a snapshot-based backup or an RMAN backup.

The following variables used in the examples below:

```sh
export NAMESPACE=<kubernetes namespace where the instance was created>
export PATH_TO_EL_CARRO_RELEASE=<the complete path to the downloaded release directory>
```

### Locate a backup

The Instance resource contains the ID of the latest backup taken for the instance:

```sh
kubectl get instances.oracle.db.anthosapis.com -n $NAMESPACE
```

```sh
NAME     DB ENGINE   VERSION   EDITION      ENDPOINT        URL                DB NAMES      BACKUP ID                        READYSTATUS   READYREASON      DBREADYSTATUS   DBREADYREASON
mydb     Oracle      12.2      Enterprise   mydb-svc.db     10.128.0.33:6021   [pdb1, pdb2]  mydb-20210427-phys-885709718     True          CreateComplete   True            CreateComplete
```

Alternatively, IDs of older backups can be found by listing the
`backups.oracle.db.anthosapis.com` resources in the same namespace that the
database instance belongs to:

```sh
kubectl get backups.oracle.db.anthosapis.com -n $NAMESPACE
```

```sh
NAME            INSTANCE NAME   BACKUP TYPE   BACKUP SUBTYPE   DOP   BS/IC   GCS PATH   PHASE       BACKUP ID                        BACKUP TIME
rman1-inst      mydb            Physical      Instance         1     true               Succeeded   mydb-20210427-phys-885709718     20210427210913
snap1           mydb            Snapshot      Instance                                  Succeeded   mydb-20210427-snap-416248334     20210427182828
```

### Prepare an Instance Resource Manifest for Restore

Once the ID for the backup to restore from is determined, you can restore the
instance by uncommenting or adding the `restore` section in the Instance
manifest. The four mandatory attributes to uncomment are:

*   backupType (Snapshot or Physical)
*   backupId
*   force
*   requestTime

The backupId comes from the previous step.

To avoid accidental restores, the `force` attribute needs to be explicitly set
to `true`. Failure to do so trips the safeguard in the Instance controller and
leads to an error message stating that you need to be explicit in requesting a
restore (and acknowledging the downtime associated with it). Another safeguard
is the `requestTime` attribute which is recommended to be set to the current
time. If the same .yaml file that was previously used for a restore operation is
sent to Kubernetes with the same value of `requestTime`, the request will be
ignored. To invoke another restore operation, `requestTime` needs to be updated
to the current time - this will ensure that the new request’s timestamp is
later than the timestamp of the previous restore operation. Requests with
earlier timestamps in `requestTime` are ignored as well.

Take 12.2 instance as an example.
```sh
cat $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_instance.yaml
```

```yaml
apiVersion: oracle.db.anthosapis.com/v1alpha1
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
    Patching: true
  sourceCidrRanges: [ 0.0.0.0/0 ]
  minMemoryForDBContainer: 4.0Gi
  maintenanceWindow:
    timeRanges:
    - start: "2121-04-20T15:45:30Z"
      duration: "168h"

#  parameters:
#    parallel_servers_target: "15"
#    disk_asynch_io: "true"
#    memory_max_target: "0"

# Uncomment this section to trigger a restore.
#  restore:
#    backupType: "Snapshot" #(or "Physical")
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

### Limitations

There are currently limitations for restoring from an RMAN backup:

*   Only backups created with `spec.backupset` either omitted or set to `true`
    can be restored from
*   Only backups created with `spec.subType` either omitted or set to `Instance`
    can be restored from
*   For local backups (ones that don't specify `spec.gcsPath` attribute and thus
    do not persist backup data in GCS) restore can be only be done for the latest
    such backup.
