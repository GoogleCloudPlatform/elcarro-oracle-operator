# Backup: Snapshots

El Carro features two types of backups: **snapshot-based** and **RMAN-based**.

You're free to choose one over the other or use a combination of the two. In
general, snapshot backups allow creating thin clones of a database, which are
considerably faster and scale better as the databases grow in size. The same
applies for the restore. On the other hand, RMAN backups are done at the
database block level, with validations (some optional, for example: check
logical), which makes RMAN backups more trustworthy. For example, block
corruption (ORA-1578 and similar) may get unnoticed and propagate to the
snapshot-based backup, but is likely to get detected in the RMAN backupset.
Also, snapshots inherently rely on the same storage device, making it a
potential point of failure.

The choice between RMAN and a storage based snapshot is completely up to you.

## Steps to create Oracle Snapshot backup

The following variables used in the examples below:

```sh
export NAMESPACE = <kubernetes namespace where the instance was created>
export PATH_TO_EL_CARRO_RELEASE = <the complete path to the downloaded release directory>
```

### Locate an instance in ready state

```sh
kubectl get instances.oracle.db.anthosapis.com -n NAMESPACE
```

```sh
NAME   DB ENGINE   VERSION   EDITION      ENDPOINT      URL                    DB NAMES   BACKUP ID   READYSTATUS   READYREASON      DBREADYSTATUS   DBREADYREASON
mydb   Oracle      12.2      Enterprise   mydb-svc.db   *******                                       True          CreateComplete   True            CreateComplete
```

### Prepare a Backup CR Manifest

Depending on whether or not a Config CR was submitted earlier, the
[volumeSnapshotClass](https://kubernetes.io/docs/concepts/storage/volume-snapshot-classes/)
attribute may not be required. If it's not provided and there's no Config, El
Carro attempts to figure out the default value for the platform. We recommend
setting it explicitly, either here or in the Config.

For GCP platform, the default value for volumeSnapshotClass is
`csi-gce-pd-snapshot-class`.

For Minikube, the default value for volumeSnapshotClass is `csi-hostpath-snapclass`.

List installed Volume Snapshot Class in the cluster.

```sh
kubectl get volumeSnapshotClass
```

```sh
cat PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_backup_snap2.yaml
```

```yaml
apiVersion: oracle.db.anthosapis.com/v1alpha1
kind: Backup
metadata:
  name: snap2
spec:
  instance: mydb
  type: Snapshot
  subType: Instance
  volumeSnapshotClass: "csi-gce-pd-snapshot-class"
```

### Submit the Database CR

After completing the Backup manifest, submit it to the local cluster as follows:

```sh
kubectl apply -f PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_backup_snap2.yaml -n $NAMESPACE
```

### Review the Backup CR

An easy way to monitor the state of the Backup CR is by running the following
command:

```sh
kubectl get backups.oracle.db.anthosapis.com -n NAMESPACE -w
```

```sh
NAME    INSTANCE NAME   BACKUP TYPE   BACKUP SUBTYPE   DOP   BS/IC   GCS PATH   PHASE        BACKUP ID                        BACKUP TIME
snap2   mydb            Snapshot      Instance                                  Succeeded    mydb-20210505-snap-480271058     20210505233252
```

Once the backup phase changed to `Succeeded`, the created snapshot backup is
ready to restore an instance.

Note that there might be multiple disks used in an El Carro instance and the
snapshots of all three have to finish successfully for the Backup CR's status to
turn from InProgress to Ready. The latest backup ID is also copied to the
Instance CR.
