# Preparation

The preparation steps consist of the following:

1.  [Set up a GCP project](https://cloud.google.com/resource-manager/docs/creating-managing-projects).
1.  Download the El Carro software.
1.  [Create a Kubernetes cluster](https://kubernetes.io/docs/setup/).
1.  Deploy the El Carro Operator.

## Set up a GCP Project

Either create a new project or use an existing one with the following settings:

```bash
gcloud projects create $PROJECT_ID [--folder [...]

gcloud services enable \
container.googleapis.com \
anthos.googleapis.com \
cloudbuild.googleapis.com \
artifactregistry.googleapis.com \
--project $PROJECT_ID
```

While the default compute service account can be used with El Carro, we
recommend creating a dedicated one as follows:

```bash
gcloud iam service-accounts create $SERVICE_ACCOUNT --project $PROJECT_ID
PROJECT_NUMBER=$(gcloud projects describe $PROJECT_ID --format="value(projectNumber)")

gcloud projects add-iam-policy-binding $PROJECT_ID --member=serviceAccount:service-${PROJECT_NUMBER}@containerregistry.iam.gserviceaccount.com --role=roles/containerregistry.ServiceAgent
```

Use this service account when you create a GKE cluster (for GCP deployment route).

### Download El Carro Software

The preparation steps differ depending on the deployment platform.

#### GCP

On GCP you need to download and to install the El Carro software yourself. The
El Carro manifests are available in both GitHub release and Google Cloud Storage.

Download El Carro software to your workstation as follows:

1) Option 1: You can download it from [El Carro GitHub repo](https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/releases).
Choose one of the release versions, preferably the latest release. The release
artifacts exist as *release-artifacts.tar.gz*.

2) Option 2: You can choose one of the release versions, preferably the latest
release, from this [GCS bucket](https://console.cloud.google.com/storage/browser/elcarro)
using [gsutil](https://cloud.google.com/storage/docs/gsutil).

```sh
gsutil -m cp -r gs://elcarro/latest .
Copying gs://elcarro/...
...


tree latest
latest
├── dashboards
│           ├── db-dashboard.json
│           ├── install-dashboards.jsonnet
│           └── README.md
├── dbimage
│           ├── cloudbuild-18c-xe.yaml
│           ├── cloudbuild.yaml
│           ├── Dockerfile
│           ├── image_build.sh
│           ├── install-oracle-18c-xe.sh
│           ├── install-oracle.sh
│           ├── ora12-config.sh
│           ├── ora19-config.sh
│           └── README.md
├── db_monitor.yaml
├── deploy
│           ├── csi
│           │           ├── gce_pd_storage_class.yaml
│           │           └── gce_pd_volume_snapshot_class.yaml
│           ├── install-18c-xe.sh
│           └── install.sh
├── get_all_logs.sh
├── operator.yaml
├── samples
│           ├── v1alpha1_backup_rman1.yaml
│           ├── v1alpha1_backup_rman2.yaml
│           ├── v1alpha1_backup_rman3.yaml
│           ├── v1alpha1_backup_rman4.yaml
│           ├── v1alpha1_backupschedule.yaml
│           ├── v1alpha1_backup_snap1.yaml
│           ├── v1alpha1_backup_snap2.yaml
│           ├── v1alpha1_backup_snap_minikube.yaml
│           ├── v1alpha1_config_bm1.yaml
│           ├── v1alpha1_config_bm2.yaml
│           ├── v1alpha1_config_gcp1.yaml
│           ├── v1alpha1_config_gcp2.yaml
│           ├── v1alpha1_config_gcp3.yaml
│           ├── v1alpha1_config_minikube.yaml
│           ├── v1alpha1_cronanything.yaml
│           ├── v1alpha1_database_pdb1_express.yaml
│           ├── v1alpha1_database_pdb1_gsm.yaml
│           ├── v1alpha1_database_pdb1_unseeded.yaml
│           ├── v1alpha1_database_pdb1.yaml
│           ├── v1alpha1_database_pdb2.yaml
│           ├── v1alpha1_database_pdb3.yaml
│           ├── v1alpha1_database_pdb4.yaml
│           ├── v1alpha1_export_dmp1.yaml
│           ├── v1alpha1_export_dmp2.yaml
│           ├── v1alpha1_import_pdb1.yaml
│           ├── v1alpha1_instance_18c_XE_express.yaml
│           ├── v1alpha1_instance_18c_XE.yaml
│           ├── v1alpha1_instance_custom_seeded.yaml
│           ├── v1alpha1_instance_express.yaml
│           ├── v1alpha1_instance_gcp_ilb.yaml
│           ├── v1alpha1_instance_minikube.yaml
│           ├── v1alpha1_instance_standby.yaml
│           ├── v1alpha1_instance_unseeded.yaml
│           ├── v1alpha1_instance_with_backup_disk.yaml
│           └── v1alpha1_instance.yaml
├── setup_monitoring.sh
├── ui.yaml
└── workflows
            ├── Kptfile
            ├── README.md
            ├── v1alpha1_database_pdb1.yaml
            └── v1alpha1_instance.yaml

6 directories, 60 files
```


The top level files and directories are:

* The `operator.yaml` is a collection of manifests that is used to deploy the El Carro Operator.
* The `ui.yaml` is a collection of manifests that is used to deploy the El
  Carro UI.
* The `dbimage` directory contains a set of files for building a containerized
  database image described in [this guide](provision/image.md).
* The `samples` directory contains the manifests for creating Custom Resources
  (CRs) mentioned in the user guide.
* The `workflows` directory is similar to samples, but the manifests there are the
  DRY templates that can be hydrated with
  [kpt](https://googlecontainertools.github.io/kpt/) to create/manage the same
  Custom Resources (CRs).

We recommend starting with the samples first, but as you become more familiar
with El Carro, consider the more advanced use of declarative workflows that can
be achieved with the parameterized templates in the workflows directory.

The `db_monitor.yaml` and `setup_monitoring.sh` files are useful to
deploy the El Carro monitoring and viewing metrics.

### Create a Cluster

#### GCP

On GCP you can create a GKE-on-GCP cluster on your own at will. GKE provides a
fully managed K8s cluster, which can be provisioned with a single command:

```sh
export ZONE=<for example: us-central1-a>
export CLUSTER_NAME=<for example: cluster1>
export SERVICE_ACCOUNT=<service account created earlier for the GKE cluster>

gcloud beta container clusters create ${CLUSTER_NAME} --release-channel rapid --machine-type=n1-standard-4 --num-nodes 2 --zone ${ZONE} --project ${PROJECT_ID} --scopes gke-default,compute-rw,cloud-platform,https://www.googleapis.com/auth/dataaccessauditlogging --service-account ${SERVICE_ACCOUNT}
```

If backups using the storage snapshots are required (El Carro recommended),
additional steps are required for setting up a CSI driver (and its corresponding
storage class). GKE comes with the on-board CSI driver, which is not suitable
for El Carro storage-based backups.

The general installation process for the gce-pd-csi driver is described
[here](https://github.com/kubernetes-sigs/gcp-compute-persistent-disk-csi-driver/blob/master/docs/kubernetes/user-guides/driver-install.md).

See an example run and the end result below:

```sh
$ cd $PATH_TO_EL_CARRO_RELEASE/deploy/csi

$ kubectl create -f gce_pd_storage_class.yaml

$ kubectl create -f gce_pd_volume_snapshot_class.yaml

// Confirm that both resources have been created properly:

$ kubectl get storageclasses
NAME                 PROVISIONER             RECLAIMPOLICY   VOLUMEBINDINGMODE      ALLOWVOLUMEEXPANSION   AGE
csi-gce-pd           pd.csi.storage.gke.io   Delete          WaitForFirstConsumer   false                  30d
standard (default)   kubernetes.io/gce-pd    Delete          Immediate              true                   30d

$ kubectl get volumesnapshotclass
NAME                        AGE
csi-gce-pd-snapshot-class   32d
```

### Deploy El Carro Operator

See the standard [K8s documentation](https://kubernetes.io/docs/tasks/access-application-cluster/configure-access-multiple-clusters/)
on how to point kubectl to a particular cluster.

#### GCP

Given that you control how GKE clusters are created, the deployment of the
El Carro Operator on your GKE-on-GCP cluster is also left to you as a user,
but it is a one step process:

```sh
$ kubectl apply -f ${PATH_TO_EL_CARRO_RELEASE}/operator.yaml
namespace/operator-system created
```

### Create a namespace

You're free to deploy El Carro in a namespace of your choice, but that
namespace should be created prior to creating an El Carro Instance as follows:

```sh
$ export NS=<namespace of user choice, for example: "db">
$ kubectl create namespace $NS
```

## What's Next
Check out [Create a Containerized Database Image](provision/image.md) to
start provisioning Instances and Databases.

You can optionally create a default Config to set namespace-wide defaults for
configuring your databases, following
[this guide](provision/config.md).
