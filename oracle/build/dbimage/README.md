# Cloud Build for DB Image
This tooling picks up the software from GCS bucket and then creates a container
image with the RDBMS software preinstalled. At present, it supports Oracle 19c
and Oracle 12.2. The docker container does not contain the database, and needs
to be created separately.

The base container OS is Oracle Enterprise Linux (OEL7-slim).

## How to run

Oracle 19c EE

```shell
$ GCS_PATH=<gcs_path_storing_software>
$ gcloud builds submit --config=cloudbuild.yaml  --substitutions=_INSTALL_PATH=$GCS_PATH,_DB_VERSION=19c
```

Oracle 12.2 EE

```shell
$ GCS_PATH=<gcs_path_storing_software>
$ gcloud builds submit --config=cloudbuild.yaml  --substitutions=_INSTALL_PATH=$GCS_PATH,_DB_VERSION=12.2
```

Oracle 18c XE

```shell
$ GCS_PATH=<gcs_path_storing_software>
$ gcloud builds submit --config=cloudbuild.yaml  --substitutions=_INSTALL_PATH=$GCS_PATH,_DB_VERSION=18c,_CREATE_CDB=true,_CDB_NAME=MYDB
```

## Access

When running the above command, you might see failures if the cloudbuilder
service account does not have 'Storage Viewer' access to the GCS bucket that
stores the software.

```shell
export PROJECT_NUMBER=<gcloud_project_number>
export BUCKET_NAME=<gcs_bucket_storing_software>
gsutil iam ch serviceAccount:$PROJECT_NUMBER@cloudbuild.gserviceaccount.com:roles/storage.objectViewer gs://$BUCKET_NAME
```
