## Dev Guide for El Carro

Thanks for your interest in El Carro! This guide details all the steps you need
to follow to set up a development environment to modify, build, test and deploy 
El Carro. The instructions below assume you have a Debian based Linux environment
such as Ubuntu. The instructions below should be completed in the order presented.

### A - Prerequisites
Before you begin, confirm that you have the following set up on your machine.

- [Google Cloud CLI for Linux](https://cloud.google.com/sdk/docs/install-sdk#linux).
Remember to authenticate yourself using `gcloud init` and select a GCP project to work with.
- [kubectl for Linux](https://kubernetes.io/docs/tasks/tools/install-kubectl-linux/)
- [Golang for Linux](https://go.dev/doc/install) 
- [Docker Engine (Server) for Linux](https://docs.docker.com/engine/install/#server)
- [Enable management of Docker for non-root users](https://docs.docker.com/engine/install/linux-postinstall/)
- [Kubebuilder](https://book.kubebuilder.io/quick-start.html#installation)
- [Buildah](https://github.com/containers/buildah/blob/main/install.md)
- Install runc and crun
  ```sh
  $ sudo apt install runc crun
  ```
- Authenticate Docker with gcloud
  ```sh
  $ gcloud auth configure-docker
  ```
- [Bazel](https://bazel.build/install)
- [Install SQL*Plus](https://www.oracle.com/ca-en/database/technologies/instant-client/linux-x86-64-downloads.html#ic_x64_inst)

###  B - Clone the El Carro repo
1.  Create a fork of the El Carro repo

    -   Visit https://github.com/GoogleCloudPlatform/elcarro-oracle-operator
    -   Click the Fork button (top right) to create a fork hosted on GitHub

2.  Clone your fork to your machine

    -   Authenticate yourself to GitHub. We recommend using SSH as described
        [here](https://docs.github.com/en/github/authenticating-to-github/connecting-to-github-with-ssh).
    
    -   Define a local working directory. For example:

        ```sh
        $ export WORKING_DIR="$(go env GOPATH)/src/elcarro.anthosapis.com"
        ```

    -   Set your GitHub user:

        ```sh
        $ export GITHUB_USER=<GitHub username>
        ```

    -   Clone your fork

        ```sh
        $ mkdir -p $WORKING_DIR
        $ cd $WORKING_DIR
        $ git clone git@github.com:$GITHUB_USER/elcarro-oracle-operator.git

        $ cd $WORKING_DIR/elcarro-oracle-operator
        $ git remote add upstream git@github.com:GoogleCloudPlatform/elcarro-oracle-operator.git

        # Disable pushing to upstream main branch
        $ git remote set-url --push upstream no_push

        # Verify remotes:
        $ git remote -v
        origin	git@github.com:<GitHub username>/elcarro-oracle-operator.git (fetch)
        origin	git@github.com:<GitHub username>/elcarro-oracle-operator.git (push)
        upstream	git@github.com:GoogleCloudPlatform/elcarro-oracle-operator.git (fetch)
        upstream	no_push (push)
        ```
        
### C - Build and Deploy El Carro
To build and deploy El Carro, please do the following:

1. Create a GKE cluster if you don't already have one by following [this guide](https://cloud.google.com/kubernetes-engine/docs/how-to/creating-a-zonal-cluster).
2. [Configure kubectl to access your cluster](https://cloud.google.com/kubernetes-engine/docs/how-to/cluster-access-for-kubectl)
3. Determine the fully qualified name of your cluster
   ```sh
   $ export PROJECT_ID=<Name of the project where you created your cluster>
   $ export GCP_ZONE=<Name of the zone where you created your cluster. i.e. us-central1-a>
   $ export CLUSTER_NAME=<The Name you gave to your cluster when you created it>
   $ export FULL_CLUSTER_NAME=gke_${PROJECT_ID}_${GCP_ZONE}_${CLUSTER_NAME}
   ```
4. cd into `el-carro/oracle`
   ```sh
   $ cd $WORKING_DIR/elcarro-oracle-operator/oracle
   ```
5. Set some environment variables for the Makefile to use:
   ```sh
   $ export PROW_PROJECT=$PROJECT_ID
   $ export PROW_IMAGE_TAG=dev
   $ export PROW_CLUSTER_ZONE=$GCP_ZONE
   $ export PROW_CLUSTER=$CLUSTER_NAME
   ```
6. Generate Kubernetes objects for El Carro such as CRDs, etc. 
   ```sh
   $ make generate-config
   ```
   You may need to install gcc by running:
   ```sh
   $ sudo apt install build-essential
   ```
7. Build and push El Carro containers. This step will take a while the first time
but take considerably less time on subsequent runs.
   ```sh
   $ make buildah-push-all-containerized -j8
   ``` 
8. Give your default Service Account permission to access your freshly built images
   ```sh
   $ gsutil iam ch serviceAccount:$(gcloud projects describe ${PROJECT_ID} --format="value(projectNumber)")-compute@developer.gserviceaccount.com:objectViewer gs://artifacts.${PROJECT_ID}.appspot.com
   ``` 
9. Deploy the El Carro Operator 
   ```sh
   $ scripts/redeploy.sh $FULL_CLUSTER_NAME
   ``` 
   Verify deployment of the Operator by running:
   ```sh
   $ kubectl get pods -n operator-system
     NAME                                          READY   STATUS    RESTARTS   AGE
     operator-controller-manager-cb8856847-pjmfz   2/2     Running   0          2m21s
   ``` 
10. sss


### D - Create a database Image
Before you can create a database instance, you will need to create a database image as follows:

1. Enable the necessary APIs on your GCP project
   ```sh
   $ gcloud services enable container.googleapis.com anthos.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com --project $PROJECT_ID
   ``` 
2. Give your default service account permissions to access the container registry
    ```sh
    $ export PROJECT_NUMBER=$(gcloud projects describe $PROJECT_ID --format="value(projectNumber)")
    $ gcloud projects add-iam-policy-binding $PROJECT_ID --member=serviceAccount:service-${PROJECT_NUMBER}@containerregistry.iam.gserviceaccount.com --role=roles/containerregistry.ServiceAgent
    ```
3. Trigger the Google Cloud Build pipeline. 
   When using Oracle 18c XE, you can only create seeded (containing a CDB)
   images. It's critical that you use an uppercase DBNAME (i.e. GCLOUD).
   To create a seeded image, run the following:

    ```sh
    $ cd $WORKING_DIR/elcarro-oracle-operator/oracle/build/dbimage
    $ chmod +x ./image_build.sh
    $ export DBNAME=<a NAME for your database, must be all UPPERCASE, i.e. GCLOUD>
    $ ./image_build.sh --db_version=18c --create_cdb=true --cdb_name=$DBNAME --no_dry_run --project_id=$PROJECT_ID
    Executing the following command:
    [...]
    ```
4. Verify that your containerized database image was successfully created.

    Cloud Build should take around 45 minutes to build the image. To verify the
    image that was created, run:

     ```sh
     $ gcloud container images list --project $PROJECT_ID --repository gcr.io/$PROJECT_ID/oracle-database-images
    
     NAME
     gcr.io/$PROJECT_ID/oracle-database-images/oracle-18c-xe-seeded-$DBNAME
    
     $ gcloud container images describe gcr.io/$PROJECT_ID/oracle-database-images/oracle-18c-xe-seeded-$DBNAME
    
     image_summary:
       digest: sha256:ce9b44ccab513101f51516aafea782dc86749a08d02a20232f78156fd4f8a52c
       fully_qualified_digest: gcr.io/$PROJECT_ID/oracle-database-images/oracle-18c-seeded-$DBNAME@sha256:ce9b44ccab513101f51516aafea782dc86749a08d02a20232f78156fd4f8a52c
       registry: gcr.io
       repository: $PROJECT_ID/oracle-database-images/oracle-18c-xe-seeded-$DBNAME
     ```

### E - Create an El Carro Database Instance
1. Create a namespace to host your instance by running:
    ```sh
    $ kubectl create namespace db
    ```
2. Prepare an instance k8s manifest (YAML file)
   ```sh
   $ cd $WORKING_DIR/elcarro-oracle-operator/docs/contributors
   $ sed -i "s/gcr.io\/PROJECT_ID/gcr.io\/${PROJECT_ID}/g" dev-db-instance.yaml
   $ sed -i "s/seeded-DBNAME/seeded-$(echo "${DBNAME}" | tr '[:upper:]' '[:lower:]')/g" dev-db-instance.yaml
   $ sed -i "s/cdbName: DBNAME/cdbName: ${DBNAME}/g" dev-db-instance.yaml
   ``` 
3. Create an instance by applying the manifest
   ```sh
   $ kubectl apply -f dev-db-instance.yaml -n db
   ``` 
4. Monitor creation of your instance by running:
    ```sh
    $ kubectl get -w instances.oracle.db.anthosapis.com -n db
    NAME   DB ENGINE   VERSION   EDITION      ENDPOINT      URL                DB NAMES   BACKUP ID   READYSTATUS   READYREASON        DBREADYSTATUS   DBREADYREASON
    mydb   Oracle      18c       Express      mydb-svc.db   34.71.69.25:6021                          False         CreateInProgress
    ```

    Once your instance is ready, the **READYSTATUS** and **DBREADYSTATUS** will both flip to **TRUE**.

    Tip: You can monitor the logs from the El Carro operator by running:
    ```sh
    $ kubectl logs -l control-plane=controller-manager -n operator-system -c manager -f
    ```

5. Create a PDB (Database)

    To store and query data, create a PDB and attach it to the instance you created
    in the previous step by running:
    ```sh
    $ kubectl apply -f $WORKING_DIR/elcarro-oracle-operator/oracle/config/samples/v1alpha1_database_pdb1.yaml -n db
    ```

    Monitor creation of your PDB by running:
    ```sh
    $ kubectl get -w databases.oracle.db.anthosapis.com -n db
    NAME   INSTANCE   USERS                                 PHASE   DATABASEREADYSTATUS   DATABASEREADYREASON   USERREADYSTATUS   USERREADYREASON
    pdb1   mydb       ["superuser","scott","proberuser"]    Ready   True                  CreateComplete        True              SyncComplete
    ```

    Once your PDB is ready, the **DATABASEREADYSTATUS** and **USERREADYSTATUS** will
    both flip to **TRUE**.

   You can access your PDB externally by using
   [sqlplus](https://docs.oracle.com/en/database/oracle/oracle-database/18/sqpug/index.html):
    ```sh
    $ sqlplus scott/tiger@$INSTANCE_URL/pdb1.gke
    ```
    Replace $INSTANCE_URL with the URL that was assigned to your instance.