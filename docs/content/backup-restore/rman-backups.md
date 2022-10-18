# Backup: RMAN

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

The choice between RMAN and a storage based snapshot for backup is completely up
to you. This guide is intended for RMAN based backups. If you want to use
snapshot based backups, please follow the guide for [Backup: Snapshots](snapshot-backups.md) instead.

## Steps to create Oracle RMAN backup

The following variables used in the examples below:

```sh
export NAMESPACE=<kubernetes namespace where the instance was created>
export PATH_TO_EL_CARRO_RELEASE=<the complete path to the downloaded release directory>
```

### Prepare a Backup CR Manifest

In Backup CR Manifest the following fields are required:
* name: backup name.
* instance: instance name to create RMAN backup for.
* type: this must be set to "Physical" for an RMAN backup.

El Carro also provides the following optional fields to manage RMAN backup
creation:

* subtype: used to specify level at which RMAN backup is to be taken. Choose between "Instance" and "Database". Default to "Instance".
* backupItems: used to specify PDBs that need to be backuped. Must be used along with "subtype: Database". Default is empty.
* backupSet: a boolean flag to control RMAN backup type. "true" for BackupSets, 'false' for Images Copies. Default is true.
* compressed: a boolean flag to turn on compression. Must used along with "backupSet: true". Default is false.
* filesperset: used to set the number of files to be allowed in a backup set. Must be used along with "backupSet: true". Default is 64.
* checkLogical: a boolean flag to turn on RMAN "check logical" option. Default is false.
* dop: used to set degree of parallelism. Default is 1.
* level: used to set incremental level (0=Full Backup, 1=Incremental, 2=Cumulative). Default is 0.
* sectionSize: a reource.Quantity used to set section size in various units (K M G). See also [resource.Quantity](https://pkg.go.dev/k8s.io/apimachinery/pkg/api/resource#Quantity)
* timeLimitMinutes: an integer used to set the time threshold for creating an RMAN backup in minutes. Default is 60.
* localPath: used to specify local backup directory. Default is '/u03/app/oracle/rman'.
* gcsPath: used to specify a GCS bucket to transfer backup to. User need to ensure proper write access to the bucket from the Oracle Operator. "localPath" will be ignored if this is set.

A sample Backup CR Manifest may look like the following:
```sh
cat $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_backup_rman3.yaml
```

```yaml
# Physical backup config for the whole Instance with all the options.
apiVersion: oracle.db.anthosapis.com/v1alpha1
kind: Backup
metadata:
  name: rman3-inst-opts
spec:
  instance: mydb
  type: Physical
  subType: Instance
  backupset: true
  checkLogical: true
  compressed: true
  # DOP = Degree of Parallelism.
  dop: 4
  # Level: 0=Full Backup, 1=Incremental, 2=Cumulative
  # level: 0
  filesperset: 10
  # Backup Section Size in MB.
  sectionSize: "100M"
  # Backup threshold is expressed in minutes (don't specify the unit, just the integer).
  timeLimitMinutes: 30
  localPath: "/u03/app/oracle/rman"
```

### Submit the Backup CR

```sh
kubectl apply -f $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_backup_rman3.yaml -n $NAMESPACE
```


### Watch backup status

```sh
kubectl get backups.oracle.db.anthosapis.com  -w -n $NAMESPACE
```

```sh
NAME              INSTANCE NAME   BACKUP TYPE   BACKUP SUBTYPE   DOP   BS/IC   GCS PATH    PHASE        BACKUP ID                        BACKUP TIME
rman3-inst-opts   mydb            Physical      Instance         4                         InProgress   mydb-20210430-phys-826537073     20210420173733
rman3-inst-opts   mydb            Physical      Instance         4                         Succeeded    mydb-20210430-phys-826537073     20210420173733
```

Once the backup phase changed to `Succeeded`, the physical backup creation is complete and ready to use.

## Scheduling regular backups

El Carro can manage schedules and retention periods for RMAN backups.

### Prepare the backup schedule manifest

To schedule a backup, create a manifest of type `BackupSchedule` with the following fields:

* backupSpec: the parameters for the backup itself, as for a [one-off backup](#preparing-a-backup-cr-manifest)
* schedule: the backup schedule, in [cron schedule syntax](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/#cron-schedule-syntax)
* backupRetentionPolicy: a list of backup retention policy parameters, most importantly:
  * backupRetention: the number of backups to keep on disk;  additional backups are deleted automatically

A sample backup schedule CR manifest may look like the following:
```sh
cat $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_backupschedule_rman1.yaml
```

```yaml
apiVersion: oracle.db.anthosapis.com/v1alpha1
kind: BackupSchedule
metadata:
  name: backupschedule-rman1
spec:
  backupSpec:
    instance: mydb
    type: Physical
    subType: Instance
  # Run at 3:01am daily, server time
  schedule: "01 03 * * *"
  backupRetentionPolicy:
    backupRetention: 3
```

### Submit the backup schedule CR

Submit the schedule using `kubectl apply`:

```sh
kubectl apply -f $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_backupschedule_rman1.yaml -n $NAMESPACE
```

### Inspect the backup schedule

```sh
kubectl describe backupschedules.oracle.db.anthosapis.com  -n $NAMESPACE
```

```
Name:         backupschedule-rman1
Namespace:    db
...
Spec:
  Backup Retention Policy:
    Backup Retention:  2
  Backup Spec:
    Backupset:                true
    Compressed:               true
    Instance:                 mydb
    Sub Type:                 Instance
    Type:                     Physical
  Schedule:                   01 03 * * *
```

Once the scheduled time passes, a status display will show the backup in progress:

```
Status:
  Backup History:
    Backup Name:    backupschedule-rman1-20221018-201200
    Creation Time:  2022-10-18T20:12:00Z
    Phase:          InProgress
  Backup Total:     1
```

And the backup can be watched [as with one-off backups](#watch-backup-status)

## What's Next?

Check out the [restore guide](restore-from-backups.md) to learn how to restore
your instance from backups.
