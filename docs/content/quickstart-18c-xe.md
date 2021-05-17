# El Carro Operator installation guide

El Carro is a new tool that allows users to keep full control of their database
environment (root on a machine, sysdba in Oracle), while helping users automate
several aspects of managing their database services.

El Carro helps you with the deployment and management of a database software
(like Oracle database) on Kubernetes. You must have appropriate licensing rights
to that database software to allow you to use it with El Carro (BYOL).

This quickstart aims to help get your licensed Oracle database up and running on
Kubernetes. This guide is only intended for Oracle 18c XE which is free to use.
If you prefer to use an Enterprise Edition of Oracle with El Carro and have a
valid Oracle license, check out the
[main quickstart guide](quickstart.md) instead.

## Before you begin

The following variables will be used in this quickstart:

```sh
export DBNAME=<Must consist of only uppercase letters, and digits. For example: MYDB>
export PROJECT_ID=<your GCP project id>
export SERVICE_ACCOUNT_ID=<The ID for the service account to be used by El Carro>
export SERVICE_ACCOUNT=<fully qualified name of the compute service account to be used by El Carro (i.e. SERVICE_ACCOUNT_ID@PROJECT_ID.iam.gserviceaccount.com)>
export PATH_TO_EL_CARRO_RELEASE=<the complete path to the downloaded release directory>
export ZONE=<for example: us-central1-a>
export CLUSTER_NAME=<for example: cluster1>
```

Download El Carro software to your workstation as follows:

1) Option 1: You can download it from [El Carro GitHub repo](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/releases).
Choose one of the release versions, preferably the latest release. The release
artifacts exist as *release-artifacts.tar.gz*.

2) Option 2: You can choose one of the release versions, preferably the latest
release, from this [GCS bucket](https://console.cloud.google.com/storage/browser/elcarro)
using [gsutil](https://cloud.google.com/storage/docs/gsutil).

```sh
gsutil -m cp -r gs://elcarro/latest $PATH_TO_EL_CARRO_RELEASE
```

[Create a new GCP project](https://cloud.google.com/resource-manager/docs/creating-managing-projects)
or reuse an existing one to install El Carro. GCP provides [free tier products](https://cloud.google.com/free).
So if you are not already a GCP user, you can sign up for a free trial. :)

```sh
gcloud projects create $PROJECT_ID [--folder [...]]
```

Set gcloud config project to $PROJECT_ID
```sh
gcloud config set project $PROJECT_ID
```

Check gcloud config project
```sh
gcloud config get-value project
```

Create a new service account or reuse an existing one in your GCP project to
install El Carro. Check out
[Creating and Managing Service Accounts](https://cloud.google.com/iam/docs/creating-managing-service-accounts)
if you need help creating or locating an existing service account.

To get El Carro up and running, you need to do one the following:

**Express install**

Run the express install script:

```sh
cd $PATH_TO_EL_CARRO_RELEASE/deploy
chmod +x ./install-18c-xe.sh

./install-18c-xe.sh --service_account $SERVICE_ACCOUNT
```

Optionally set CDB name, GKE cluster name, GKE zone

```sh
./install-18c-xe.sh --service_account $SERVICE_ACCOUNT --cdb_name $DBNAME --cluster_name $CLUSTER_NAME --gke_zone $ZONE
```

OR

**Perform the manual install steps:**

*   Check downloaded El Carro software
*   Create a containerized database image
*   Provision a kubernetes cluster. We recommend a cluster running
    Kubernetes/GKE version 1.17 or above.
*   Deploy the El Carro Operator to your Kubernetes cluster
*   Create an Instance (CDB) and Database (PDB) via the El Carro Operator

## Check downloaded El Carro software

*   The `operator.yaml` is a collection of manifests that is used to deploy the
    El Carro Operator:
*   The `ui.yaml` is a collection of manifests that is used to deploy the El
    Carro UI.
*   The `dbimage` directory contains a set of files for building a
    containerized database image described in the next section.
*   The `samples` directory contains the manifests for creating Custom
    Resources (CRs) mentioned in this user guide.
*   The `workflows` directory is similar to samples, but the manifests there
    are the DRY templates that can be hydrated with
    [kpt](https://googlecontainertools.github.io/kpt/) to create/manage the same
    Custom Resources (CRs).

We recommend starting with the samples first, but as you become more familiar
with El Carro, consider the more advanced use of declarative workflows that can
be achieved with the parameterized templates in the workflows directory.

The `db_monitor.yaml` and `setup_monitoring.sh` files are useful to deploy the
El Carro monitoring and viewing metrics.

## Creating a containerized Oracle database image

There are two options to build the actual container database image: Using Google
Cloud Build or locally using Docker.

### Using Google Cloud Build to create a containerized Oracle database image (Recommended)

1.  [Create a new GCP project](https://cloud.google.com/resource-manager/docs/creating-managing-projects)
    or reuse an existing one with the following settings:

    ```sh
    gcloud projects create $PROJECT_ID [--folder [...]]
    gcloud services enable container.googleapis.com anthos.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com --project $PROJECT_ID
    ```

    Though the default compute service account can be used with El Carro, we
    recommend creating a dedicated one as follows:

    ```sh
    gcloud iam service-accounts create $SERVICE_ACCOUNT_ID --project $PROJECT_ID
    export PROJECT_NUMBER=$(gcloud projects describe $PROJECT_ID --format="value(projectNumber)")
    gcloud projects add-iam-policy-binding $PROJECT_ID --member=serviceAccount:service-${PROJECT_NUMBER}@containerregistry.iam.gserviceaccount.com --role=roles/containerregistry.ServiceAgent
    ```

2.  Trigger the Google Cloud Build pipeline

    When using Oracle 18c XE, you can only create seeded (containing a CDB)
    images. To create a seeded image, run the following:

    ```sh
    cd $PATH_TO_EL_CARRO_RELEASE/dbimage
    chmod +x ./image_build.sh

    ./image_build.sh --db_version=18c --create_cdb=true --cdb_name=$DBNAME --no_dry_run --project_id=$PROJECT_ID

    Executing the following command:
    [...]
    ```

3.  Verify that your containerized database image was successfully created.

    Cloud Build should take around 45 minutes to build the image. To verify the
    image that was created, run:

    ```sh
    gcloud container images list --project $PROJECT_ID --repository gcr.io/$PROJECT_ID/oracle-database-images

    NAME
    gcr.io/$PROJECT_ID/oracle-database-images/oracle-18c-xe-seeded-$DBNAME

    gcloud container images describe gcr.io/$PROJECT_ID/oracle-database-images/oracle-18c-xe-seeded-$DBNAME

    image_summary:
      digest: sha256:ce9b44ccab513101f51516aafea782dc86749a08d02a20232f78156fd4f8a52c
      fully_qualified_digest: gcr.io/$PROJECT_ID/oracle-database-images/oracle-18c-seeded-$DBNAME@sha256:ce9b44ccab513101f51516aafea782dc86749a08d02a20232f78156fd4f8a52c
      registry: gcr.io
      repository: $PROJECT_ID/oracle-database-images/oracle-18c-xe-seeded-$DBNAME
    ```

### Building a containerized Oracle database image locally using Docker

If you're not able or prefer not to use Google cloud build to create a
containerized database image, you can build an image locally using
[Docker](https://www.docker.com). You need to push your locally built image to a
registry that your Kubernetes cluster can pull images from. You must have Docker
installed before proceeding with a local containerized database image build.

Note that in the current release, local build only works with Linux systems.
Additional support for other operating systems like Windows, Mac OS, etc. will
be added in future releases.

1.  Trigger the image creation script

    When using Oracle 18c XE, you can only create seeded (containing a CDB)
    images. To create a seeded image, run the following:

    ```sh
    cd $PATH_TO_EL_CARRO_RELEASE/dbimage
    chmod +x ./image_build.sh

    ./image_build.sh --local_build=true --db_version=18c --create_cdb=true --cdb_name=$DBNAME --no_dry_run --project_id=local-build
    ```

2.  Verify that your containerized database image was successfully created.

    Docker should take around 20-30 minutes to build the image. To verify that
    your image was successfully created, run the following command:

    ```sh
    docker images
    REPOSITORY                                                                               TAG       IMAGE ID       CREATED       SIZE
    gcr.io/local-build/oracle-database-images/oracle-18c-xe-seeded-$DBNAME         latest    c766d980c9a0   2 hours ago   11.8GB
    ```

3.  Retag your locally built image if necessary and push it to a registry that
    your Kubernetes cluster can pull images from.

## Provisioning a Kubernetes cluster on GKE to run El Carro

To provision a Kubernetes cluster on Google Kubernetes Engine (GKE), run the
following command:

```sh
gcloud container clusters create $CLUSTER_NAME --release-channel rapid --machine-type=n1-standard-4 --num-nodes 2 --zone $ZONE --project $PROJECT_ID --scopes gke-default,compute-rw,cloud-platform,https://www.googleapis.com/auth/dataaccessauditlogging --service-account $SERVICE_ACCOUNT
```

To get the cluster ready for El Carro, create a k8s storage class and a volume
snapshot class as follows:

```sh
kubectl create -f $PATH_TO_EL_CARRO_RELEASE/deploy/csi/gce_pd_storage_class.yaml
kubectl create -f $PATH_TO_EL_CARRO_RELEASE/deploy/csi/gce_pd_volume_snapshot_class.yaml
```

Verify that both resources have been created properly by running:

```sh
kubectl get storageclasses
NAME                 PROVISIONER             RECLAIMPOLICY   VOLUMEBINDINGMODE      ALLOWVOLUMEEXPANSION   AGE
csi-gce-pd           pd.csi.storage.gke.io   Delete          WaitForFirstConsumer   false                  30d
standard (default)   kubernetes.io/gce-pd    Delete          Immediate              true                   30d

kubectl get volumesnapshotclass
NAME                        AGE
csi-gce-pd-snapshot-class   78s
```

## Deploying the El Carro Operator to a Kubernetes cluster

You can use `kubectl` to deploy the El Carro Operator to your cluster by
running:

```sh
kubectl apply -f $PATH_TO_EL_CARRO_RELEASE/operator.yaml
namespace/operator-system created
[...]
```

## Creating an El Carro Instance

An instance consists of Oracle software running in a container and a CDB. To
create an instance:

-   Create a namespace to host your instance by running:
    ```sh
    kubectl create namespace db
    ```

-   Modify $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_instance_18c_XE.yaml to
    include the link to the database service image you built earlier.

-   Apply the modified yaml file by running:
    ```sh
    kubectl apply -f $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_instance_18c_XE.yaml -n db
    ```

Monitor creation of your instance by running:
```sh
kubectl get -w instances.oracle.db.anthosapis.com -n db
NAME   DB ENGINE   VERSION   EDITION      ENDPOINT      URL                DB NAMES   BACKUP ID   READYSTATUS   READYREASON        DBREADYSTATUS   DBREADYREASON
mydb   Oracle      18c       Express      mydb-svc.db   34.71.69.25:6021                          False         CreateInProgress
```

Once your instance is ready, the **READYSTATUS** and **DBREADYSTATUS** will both
flip to **TRUE**.

Tip: You can monitor the logs from the El Carro operator by running:
```sh
kubectl logs -l control-plane=controller-manager -n operator-system -c manager -f
```

## Creating a PDB (Database)

To store and query data, create a PDB and attach it to the instance you created
in the previous step by running:
```sh
kubectl apply -f $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_database_pdb1.yaml -n db
```

Monitor creation of your PDB by running:
```sh
kubectl get -w databases.oracle.db.anthosapis.com -n db
NAME   INSTANCE   USERS                                 PHASE   DATABASEREADYSTATUS   DATABASEREADYREASON   USERREADYSTATUS   USERREADYREASON
pdb1   mydb       ["superuser","scott","proberuser"]    Ready   True                  CreateComplete        True              SyncComplete
```

Once your PDB is ready, the **DATABASEREADYSTATUS** and **USERREADYSTATUS** will
both flip to **TRUE**.

You can access your PDB externally by using
[sqlplus](https://docs.oracle.com/en/database/oracle/oracle-database/18/sqpug/index.html):
```sh
sqlplus scott/tiger@$INSTANCE_URL/pdb1.gke
```
Replace $INSTANCE_URL with the URL that was assigned to your instance.

## ORACLE 18c XE Limitations

Oracle 18c XE has the following resource limitations:

-   Up to 12 GB of user data
-   Up to 2 GB of database RAM
-   Up to 2 CPU threads
-   Up to 3 Pluggable Databases

More details on what's included in Oracle 18c XE can be found on
[Oracle's website](https://www.oracle.com/database/technologies/appdev/xe.html).
