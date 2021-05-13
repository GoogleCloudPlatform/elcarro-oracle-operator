# Steps to create Oracle RMAN backup

The following variables used in the examples below:

```sh
export NAMESPACE=<kubernetes namespace where the instance was created>
export PATH_TO_EL_CARRO_RELEASE=<the complete path to the downloaded release directory>
```

## Prepare a Backup CR Manifest

In Backup CR Manifest the following fields are required:
* name: backup name.
* instance: instance name to create RMAN backup for.
* type: this must be set to "Physical" for a RMAN backup.

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
* sectionSize: an integer used to set section size in MB.
* timeLimit: an integer used to set the time threshold for creating a RMAN backup in minutes. Default is 60.
* localPath: used to specify local backup directory. Default is '/u03/app/oracle/rman'.
* gcSPath: used to specify a GCS bucket to transfer backup to. User need to ensure proper write access to the bucket from the Oracle Operator. "localPath" will be ignored if this is set.

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
  level: 0
  filesperset: 10
  # Backup Section Size in MB (don't specify the unit, just the integer).
  sectionSize: 100
  # Backup threshold is expressed in minutes (don't specify the unit, just the integer).
  timeLimit: 30
  localPath: "/u03/app/oracle/rman"
```

## Submit the Backup CR

```sh
kubectl apply -f $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_backup_rman3.yaml -n $NAMESPACE
```


## Watch backup status

```sh
kubectl get backups.oracle.db.anthosapis.com  -w -n $NAMESPACE
```

```sh
NAME              INSTANCE NAME   BACKUP TYPE   BACKUP SUBTYPE   DOP   BS/IC   GCS PATH    PHASE        BACKUP ID                        BACKUP TIME
rman3-inst-opts   mydb            Physical      Instance         4                         InProgress   mydb-20210430-phys-826537073     20210420173733
rman3-inst-opts   mydb            Physical      Instance         4                         Succeeded    mydb-20210430-phys-826537073     20210420173733
```

Once the backup phase changed to `Succeeded`, the physical backup creation is complete and ready to use.
