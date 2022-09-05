## Managing database user credentials using Google Secret Manager

When creating Databases (PDBs) in El Carro, it's a good idea to keep user
passwords out of Kubernetes objects (e.g: YAML files). This guide shows you how
to securely manage user credentials for El Carro databases using
[Google Secret Manager](https://cloud.google.com/secret-manager).

### A - Prerequisites

-   Save the name of your GCP project to a variable:

    ```sh
    $ export PROJECT_ID=<Name of the project where El Carro is installed>
    ```

-   Configure gcloud to point to your project

    ```sh
    $ gcloud config set project $PROJECT_ID
    ```

-   You must have an El Carro instance fully provisioned. Check out the
    [quickstart guide](../quickstart.md) if you don't already have an instance.
    You can verify that you have an instance running by executing:

    ```sh
    $ kubectl get instances.oracle.db.anthosapis.com -A
    ```

-   Enable the Google Cloud IAM Credentials API

    ```sh
    $ gcloud services enable iamcredentials.googleapis.com
    ```

-   Enable the Secret Manager API

    ```sh
    $ gcloud services enable secretmanager.googleapis.com
    ```

### B - Create a Service Account (SA)

Your service account must be created in the same Google Cloud project as your
El Carro installation.

- Choose a name for your Service Account:

    ```sh
    $ export GSM_SA=<i.e gsm-sa>
    ```

- Create the Service Account:

    ```sh
    $ gcloud iam service-accounts create $GSM_SA
    ```

### C - Create database user secrets with GSM

Create secret id/password pairs: GPDB_ADMIN/google, superuser/superpassword,
scott/tiger, proberuser/proberpassword by running:

```sh
$ echo -n "google" | gcloud secrets create GPDB_ADMIN --replication-policy="automatic" --data-file=-
$ echo -n "superpassword" | gcloud secrets create superuser --replication-policy="automatic" --data-file=-
$ echo -n "tiger" | gcloud secrets create scott --replication-policy="automatic" --data-file=-
$ echo -n "proberpassword" | gcloud secrets create proberuser --replication-policy="automatic" --data-file=-
```

> **WARNING:** Creating/Updating secret versions using plain text like in the
> example above is discouraged because the secrets will appear in your shell
> history. The recommended approach for creating secrets is to use the
> **--data-file** to specify the path to the contents of your secrets. Visit
> [Creating and accessing secrets](https://cloud.google.com/secret-manager/docs/creating-and-accessing-secrets#secretmanager-add-secret-version-cli)
> for more information.

### D - Grant your Service Account access to your secrets

- Grant your Service Account access to your secrets:

    ```sh
    $ gcloud secrets add-iam-policy-binding GPDB_ADMIN --role=roles/secretmanager.secretAccessor --member=serviceAccount:${GSM_SA}@${PROJECT_ID}.iam.gserviceaccount.com
    $ gcloud secrets add-iam-policy-binding superuser --role=roles/secretmanager.secretAccessor --member=serviceAccount:${GSM_SA}@${PROJECT_ID}.iam.gserviceaccount.com
    $ gcloud secrets add-iam-policy-binding scott --role=roles/secretmanager.secretAccessor --member=serviceAccount:${GSM_SA}@${PROJECT_ID}.iam.gserviceaccount.com
    $ gcloud secrets add-iam-policy-binding proberuser --role=roles/secretmanager.secretAccessor --member=serviceAccount:${GSM_SA}@${PROJECT_ID}.iam.gserviceaccount.com
    ```

### E - Create a binding between the default k8s Service Account and your GSM Service Account

- Create the binding on the Google Cloud Service Account:

    ```sh
    $ gcloud iam service-accounts add-iam-policy-binding \
      --role roles/iam.workloadIdentityUser \
      --member "serviceAccount:${PROJECT_ID}.svc.id.goog[operator-system/default]" \
      ${GSM_SA}@${PROJECT_ID}.iam.gserviceaccount.com
    ```

- Add an annotation to the default k8s Service Account:

    ```sh
    $ kubectl annotate serviceaccount \
      --namespace operator-system \
      default \
      iam.gke.io/gcp-service-account=${GSM_SA}@${PROJECT_ID}.iam.gserviceaccount.com
    ```

### F - Create a Database (PDB) using your GSM secrets

- Navigate to the directory where your El Carro release is located:

    ```sh
    $ cd $PATH_TO_EL_CARRO_RELEASE
    ```

- Update the sample Database manifest (YAML) with your PROJECT_ID:

    ```sh
    $ sed -i "s|\${PROJECT_ID}|${PROJECT_ID}|g" samples/v1alpha1_database_pdb1_gsm.yaml
    ```

The resulting YAML file should look like:

```yaml
cat samples/v1alpha1_database_pdb1_gsm.yaml
apiVersion: oracle.db.anthosapis.com/v1alpha1
kind: Database
metadata:
  name: pdb1
spec:
  name: pdb1
  instance: mydb
  adminPasswordGsmSecretRef:
    projectId: my-project-id
    secretId: GPDB_ADMIN
    version: "1"
  users:
    - name: superuser
      gsmSecretRef:
        projectId: my-project-id
        secretId: superuser
        version: "1"
      privileges:
        - dba
    - name: scott
      gsmSecretRef:
        projectId: my-project-id
        secretId: scott
        version: "1"
      privileges:
        - connect
        - resource
        - unlimited tablespace
    - name: proberuser
      gsmSecretRef:
        projectId: my-project-id
        secretId: proberuser
        version: "1"
      privileges:
        - create session
```

- Save the name of the namespace where you created your El Carro Instance.

    ```sh
    $ export DB_NAMESPACE=<i.e db>
    ```

- Create the database (PDB):

  ```sh
  kubectl apply -f samples/v1alpha1_database_pdb1_gsm.yaml -n $DB_NAMESPACE
  ```

- Monitor creation of your PDB by running:

    ```sh
    $ kubectl get -w databases.oracle.db.anthosapis.com -n $DB_NAMESPACE
    NAME   INSTANCE   USERS                                 PHASE   DATABASEREADYSTATUS   DATABASEREADYREASON   USERREADYSTATUS   USERREADYREASON
    pdb1   mydb       ["superuser","scott","proberuser"]    Ready   True                  CreateComplete        True              SyncComplete
    ```

  Once your PDB is ready, the **DATABASEREADYSTATUS** and **USERREADYSTATUS**
  will both flip to **TRUE**.


- You can access your PDB externally by using
  [sqlplus](https://www.oracle.com/database/technologies/instant-client/downloads.html):

  ```sh
  $ sqlplus scott/tiger@$INSTANCE_URL/pdb1.gke
  ```
  Replace $INSTANCE_URL with the URL that was assigned to your instance.
