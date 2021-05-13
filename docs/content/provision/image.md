# Create a Containerized Database Image

This guide is only valid for Oracle Database 12c. To build an Oracle Database
18c XE image, refer to the [18c XE quickstart guide](../quickstart-18c-xe.md).

## Before you begin

The following variables will be used in this guide:

```sh
export DBNAME=<Must consist of only uppercase letters, and digits. For example: MYDB>
export PROJECT_ID=<your GCP project id>
export SERVICE_ACCOUNT=<fully qualified name of the compute service account to be used by El Carro (i.e. SERVICE_ACCOUNT@PROJECT_NAME.iam.gserviceaccount.com)>
export PATH_TO_EL_CARRO_RELEASE=<the complete path to directory that contains the downloaded El Carro release>
export GCS_BUCKET=<your globally unique Google Cloud Storage bucket name>
```

You should set these variables in your environment.

**There are two options to build the actual container database image: Using
Google Cloud Build or building the image locally using Docker.**

### Using Google Cloud Build to create a containerized Oracle database image (Recommended)

1.  Download Oracle software to Google Cloud Storage.

    El Carro has only been tested with
    [Oracle 12c R2 (12.2)](https://docs.oracle.com/en/database/oracle/oracle-database/12.2/index.html)
    and
    [Oracle 18c XE](https://www.oracle.com/database/technologies/appdev/xe.html).
    The recommended place to obtain the database software is the official Oracle
    eDelivery Cloud. Patches can be downloaded from the Oracle support website.
    As an El Carro user, you're advised to consult your licensing agreement with
    Oracle Corp to decide where to get the software from. To create an Oracle
    database image, you will need to download three pieces of software from
    Oracle's website:

    -   Oracle Database 12c Release 2 (12.2.0.1.0) for Linux x86-64 (Enterprise
        Edition), which can be downloaded from the
        [Oracle eDelivery Cloud](https://edelivery.oracle.com).
    -   A recent PSU. The Jan 2021 PSU can be downloaded
        [here](https://support.oracle.com/epmos/faces/PatchDetail?_adf.ctrl-state=bsblgctta_4&patch_name=32228578&releaseId=600000000018520&patchId=32228578&languageId=0&platformId=226&_afrLoop=314820757336783).
    -   Latest available OPatch that can be downloaded from here. We recommend
        choosing the following download parameters:
        -   Release: OPatch 20.0.0.0.0
        -   Platform: Linux x86_64

    The result of downloading the three Oracle artifacts should look like the
    following:

    ```sh
    ls -l ~/Downloads
    -rw-r--r--@   1 primaryuser  primarygroup    856130787 Oct 13 13:24 p32228578_122010_Linux-x86-64.zip
    -rw-r--r--@   1 primaryuser  primarygroup    119259475 Oct 13 12:45 p6880880_200000_Linux-x86-64.zip
    -rw-r--r--@   1 primaryuser  primarygroup   3453696911 Oct 13 12:33 linuxx64_12201_database.zip
    ```

    Next, transfer these three files to a Google Cloud Storage bucket. If
    needed, a new Google Cloud Storage bucket can be created as follows:

    ```sh
    gsutil mb gs://${GCS_BUCKET}
    gsutil cp ~/Downloads/linuxx64_12201_database.zip gs://${GCS_BUCKET}/install/
    gsutil cp ~/Downloads/p6880880_200000_LINUX.zip  gs://${GCS_BUCKET}/install/
    gsutil cp ~/Downloads/p32228578_122010_Linux-x86-64.zip  gs://${GCS_BUCKET}/install/
    ```

    This is an example of how the three files in a Google Cloud Storage bucket
    could look like on the command line and in the Google Cloud Console:

    ```sh
    gsutil ls -l gs://${GCS_BUCKET}/install
             0  2020-10-13T19:24:05Z  gs://${GCS_BUCKET}/install/
    3453696911  2020-10-13T19:24:24Z  gs://${GCS_BUCKET}/install/linuxx64_12201_database.zip
     856130787  2020-10-13T19:37:29Z  gs://${GCS_BUCKET}/install/p32228578_122010_Linux-x86-64.zip
     119259475  2020-10-13T19:26:33Z  gs://${GCS_BUCKET}/install/p6880880_200000_LINUX.zip
    ```

    Once the bucket is ready, grant the IAM read privilege
    (roles/storage.objectViewer) to the Google Cloud Build service account. Use
    your project name as a PROJECT_ID below and your globally unique Google
    Cloud Storage bucket name in the snippet below:

    ```sh
    export PROJECT_NUMBER=$(gcloud projects describe ${PROJECT_ID} --format="value(projectNumber)")
    gsutil iam ch serviceAccount:${PROJECT_NUMBER}@cloudbuild.gserviceaccount.com:roles/storage.objectViewer gs://${GCS_BUCKET}
    ```

2.  Trigger the Google Cloud Build (GCB) pipeline.

    You have a choice of creating a container image with just the Oracle RDBMS
    software in it (based on what you downloaded and placed in a Google Cloud
    Storage bucket) or, optionally, create a seed database at the same time and
    host it in the same image. El Carro recommends you to consider the following
    before making this decision:

    *   A database container image with the seed database in it is "heavier" and
        takes longer to download at runtime, however the overall El Carro
        Instance provisioning with the seed image is still faster compared to
        creating a database instance from scratch at runtime.

    *   The downside of including a seed database in the container image are
        related to image maintenance and flexibility:

        *   If all/most of your databases use the same character set and
            same/similar database options (Oracle Text, APEX, etc.), having a
            seed database in the image may be the most cost effective way for
            you to proceed.

        *   If on the other hand, your databases are very different in terms of
            database options, init parameters, character sets, it may be easier
            for you to not include a seed database in the image, which makes the
            provisioning time longer, but relieves you from maintaining multiple
            container images.

    To proceed with creating a seed database as part of the container image
    build, add --create_cdb true and optionally specify a seed database name,
    e.g. --cdb_name ORCL (Note that if a seed database name is not provided,
    image_build.sh defaults it to GCLOUD. This value can be changed at runtime)

    Set PATH_TO_EL_CARRO_RELEASE to the directory where the El Carro release was
    downloaded to.

    ```sh
    cd ${PATH_TO_EL_CARRO_RELEASE}/dbimage
    chmod +x ./image_build.sh
    ./image_build.sh --install_path=gs://${GCS_BUCKET}/install --db_version=12.2 --create_cdb=true --cdb_name=${DBNAME} --mem_pct=45 --no_dry_run --project_id=${PROJECT_ID}
    ```

    If you prefer to create a database container image without a seed database,
    set --create_cdb false.

    ```sh
    cd ${PATH_TO_EL_CARRO_RELEASE}/dbimage
    chmod +x ./image_build.sh
    ./image_build.sh --install_path=gs://${GCS_BUCKET}/install --db_version=12.2 --create_cdb=false --mem_pct=45 --no_dry_run --project_id=${PROJECT_ID}
    ```

    Note that depending on the options, creating a containerized image may take
    ~40+ minutes.

    If AccessDeniedException is raised against the above command that likely
    means that the previous gsutil iam ch command didn't succeed. Once fixed,
    rerun the above image build script.

3.  Verify that your containerized database image was successfully created.

    Creating a containerized image can take ~40+ minutes and the progress is
    trackable from both the command line (from the previous command) and the
    Google Cloud Console UI.

    ```sh
    gcloud container images list --project ${PROJECT_ID} --repository gcr.io/${PROJECT_ID}/oracle-database-images --filter=oracle-12.2-ee-seeded-$(echo "${DBNAME}" | tr '[:upper:]' '[:lower:]')

    NAME
    gcr.io/${PROJECT_ID}/oracle-database-images/oracle-12.2-ee-seeded-${DBNAME}

    gcloud container images describe gcr.io/${PROJECT_ID}/oracle-database-images/oracle-12.2-ee-seeded-$(echo "${DBNAME}" | tr '[:upper:]' '[:lower:]') --project ${PROJECT_ID}
    image_summary:
      digest: sha256:ce9b44ccab513101f51516aafea782dc86749a08d02a20232f78156fd4f8a52c
      fully_qualified_digest: gcr.io/${PROJECT_ID}/oracle-database-images/oracle-12.2-ee-seeded-${DBNAME}@sha256:ce9b44ccab513101f51516aafea782dc86749a08d02a20232f78156fd4f8a52c
      registry: gcr.io
      repository: ${PROJECT_ID}/oracle-database-images/oracle-12.2-ee-seeded-${DBNAME}
    ```

### Building a containerized Oracle database image locally using Docker

If you're not able or prefer not to use Google cloud build to create a
containerized database image, you can build an image locally using
[Docker](https://www.docker.com). You need to push your locally built image to a
registry that your Kubernetes cluster can pull images from. You must have Docker
installed before proceeding with a local containerized database image build.

1.  Copy the Oracle binaries you downloaded earlier to
    ${PATH_TO_EL_CARRO_RELEASE}/dbimage

    Your ${PATH_TO_EL_CARRO_RELEASE} directory should look something
    like.

    ```sh
    ls -1X
    Dockerfile
    README.md
    image_build.sh
    install-oracle-18c-xe.sh
    install-oracle.sh
    ora12-config.sh
    ora19-config.sh
    cloudbuild.yaml
    p32228578_122010_Linux-x86-64.zip
    p6880880_200000_Linux-x86-64.zip
    V839960-01.zip
    ```

2.  Trigger the image creation script.

    You have the choice of creating a container image with just the Oracle RDBMS
    software in it or, optionally, create a seed database (CDB) at the same time
    and host it in the same container image. Seeded images are larger in size
    but may save you time during provisioning. For a quick start, we suggest you
    create a seeded container image.

    -   To create a seeded image, run the following:

    ```sh
    cd ${PATH_TO_EL_CARRO_RELEASE}/dbimage
    chmod +x ./image_build.sh

    ./image_build.sh --local_build=true --db_version=12.2 --patch_version=32228578 --create_cdb=true --cdb_name=${DBNAME} --mem_pct=45 --no_dry_run --project_id=local-build

    Executing the following command:
    [...]
    ```

    -   To create an unseeded image (one without a CDB), run the same steps as
        for the seeded case but set the `--create_cdb` flag to `false` and omit
        the `--cdb_name` parameter.

3.  Verify that your containerized database image was successfully created.

    Depending on the options you choose when you run the image creation script,
    creating a containerized image may take ~40+ minutes. To verify that your
    image was successfully created, run the following command:

    ```sh
    docker images
    REPOSITORY                                                                               TAG       IMAGE ID       CREATED       SIZE
    gcr.io/local-build/oracle-database-images/oracle-12.2-ee-seeded-${DBNAME}        latest    c766d980c9a0   2 hours ago   17.4GB
    ```

4.  Retag your locally built image if necessary and push it to a registry that
    your Kubernetes cluster can pull images from.

## What's Next

Check out the [instance provisioning guide](instance.md) to learn how to deploy
your newly built image to a Kubernetes cluster using El Carro.
