# El Carro database environment

## Access El Carro database environment

El Carro database processes run in
[kubernetes containers](https://kubernetes.io/docs/concepts/containers/) which
belong to a database [pod](https://kubernetes.io/docs/concepts/workloads/pods/)
, the container base image is docker.io/oraclelinux:7-slim .

### Before you begin

Install [kubectl](https://kubernetes.io/docs/tasks/tools/) to interact with the
kubernetes cluster.

Check the current context to ensure it points to the cluster where an El Carro
instance is running,

```
kubectl config current-context
```

See
[Kubectl context and configuration](https://kubernetes.io/docs/reference/kubectl/cheatsheet/#kubectl-context-and-configuration)
for more instructions.

The following variables will be used:

```
export NS=<El Carro instance namespace>
export INST=<El Carro instance name>
```

### To get a shell to El Carro database container

Get database pod name

```
kubectl get pods -l instance=$INST -n $NS -o=jsonpath='{.items[0].metadata.name}'
```

Get a shell to the oracledb container in the name specified pod.

```
kubectl exec -it <database pod name> -c oracledb -n $NS -- bash
```

### To copy a file/dir to El Carro database environment from local environment

```
kubectl cp <path to a file or dir in local> <database pod name>:<path to a file or dir in container> -c oracledb -n $NS
```

### To copy a file/dir from El Carro database environment to local environment

```
kubectl cp <database pod name>:<path to a file or dir in container> -c oracledb -n $NS <path to a file or dir in local>
```

### Oracle 12.2 env variables for the El Carro database container

```
export ORACLE_HOME=/u01/app/oracle/product/12.2/db
export PATH=/u01/app/oracle/product/12.2/db/bin:/u01/app/oracle/product/12.2/db/OPatch:/usr/local/bin:/usr/local/sbin:/sbin:/bin:/usr/sbin:/usr/bin:/root/bin
export ORACLE_SID=<source database sid>
```

### Oracle 19.3 env variables for the El Carro database container

```
export ORACLE_HOME=/u01/app/oracle/product/19.3/db
export PATH=/u01/app/oracle/product/19.3/db/bin:/u01/app/oracle/product/19.3/db/OPatch:/usr/local/bin:/usr/local/sbin:/sbin:/bin:/usr/sbin:/usr/bin:/root/bin
export ORACLE_SID=<source database sid>
```

## El Carro database filesystem

```
df -ahT
Filesystem     Type     Size  Used Avail Use% Mounted on
...
overlay        overlay   95G   26G   70G  27% /
/dev/sdd       ext4      98G  3.1G   95G   4% /u02
/dev/sdc       ext4     147G   78M  147G   1% /u03
/dev/sdb       ext4      98G   61M   98G   1% /u04
...
```

El Carro optionally provision three
[persistent volumes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/)
based on a disk specification(spec.disks). In a database container, DataDisk
mount point is /u02, LogDisk mount point is /u03, BackupDisk mount point is
/u04. Persistent volumes exist beyond the lifetime of a pod, their data can be
preserved across container restarts. If we want to preserve files on an El Carro
instance, the location should be under /u02, /u03, or /u04. Apart from /u02,
/u03, /u04, Other locations are ephemeral(for example: /u01, /home/oracle, /tmp
and so on). As described in
[k8s doc](https://kubernetes.io/docs/concepts/storage/volumes/), containers
restart/crash will lose states in ephemeral files.

We suggest put data files under `/u02/app/oracle/oradata/`

put archive log files under `/u03/app/oracle/fast_recovery_area/`

put config files(spfile, password) under `/u02/app/oracle/oraconfig/`

For other files used in data migration, we suggest putting them under /u02, /u03
or /u04.

## Configure El Carro to access Google Cloud Platform resources

### Before you begin

The following variables will be used:

```
export PROJECT_ID=<your GCP project id>
export CLUSTER_NAME=<your GKE cluster name, for example: cluster1>
export ZONE=<your GKE cluster zone, for example: us-central1-a>
```

### Configure GCP service account for El Carro

Run [configure-service-account.sh](../../hack/configure-service-account.sh) to configure the GCP service account
for El Carro in GKE.

```
bash configure-service-account.sh --cluster_name $CLUSTER_NAME --gke_zone $ZONE
```

If workload identity is disabled in the GKE cluster, GCP Compute Engine default 
service account will be used.

If workload identity is enabled in the GKE cluster, the script will remind you
to add two more required parameters and help you bind service account with 
El Carro components:

1.  k8s namespace, either operator namespace or instance namespace

    Operator namespace(`operator-system` default value in El Carro configuration), where the El Carro operator will be deployed. 

    Instance namespace(`db`, default value in this instruction), where the El Carro instance CR will be deployed.

    ```
    export NS=<El Carro operator/instance namespace>
    ```

2.  An existing GCP service account to bind with El Carro k8s service account

    You can either create a new service account

    ```
    export SA=<A new GCP service account name, for example: el_carro_sa>
    gcloud iam service-accounts create "${SA}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --description="El Carro pods service account" \
    --display-name="El Carro pods service account" \
    --project=$PROJECT_ID
    ```

    or list existing service accounts and choose one from them.

    ```
    gcloud iam service-accounts list --project=$PROJECT_ID
    ```

    See
    [Google Cloud service accounts](https://cloud.google.com/iam/docs/creating-managing-service-accounts)
    for more instructions

    Then you can run

    ```
    export NS=<El Carro operator/instance namespace>
    export SA=<An existing GCP service account>
    bash configure-service-account.sh --cluster_name $CLUSTER_NAME --gke_zone $ZONE --namespace $NS --service_account $SA
    ```

The script output will show the GCP service account, which will be used by an El
Carro instance.

