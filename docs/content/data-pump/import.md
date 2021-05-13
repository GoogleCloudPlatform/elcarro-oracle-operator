# DataPump: Import data from one PDB into another one

Running Oracle Data Pump import utility - `impdp` - is supported via a
declarative Kubernetes-based API. An import operation takes a dump file at a
[Google Cloud Storage](https://cloud.google.com/storage/docs) location and
applies it to an existing PDB. A dump file contains db data and metadata in an
Oracle proprietary binary format which is produced by the Oracle Data Pump
export utility - `expdp`.

Note: the **PDB name** the data was exported from, and the destination **PDB
name** the dump file is imported to, must match.

1.  Prepare a dump file and set permissions

    Upload a dump file to a
    [Google Cloud Storage](https://cloud.google.com/storage/docs) location, for
    example: `gs://example-bucket/imports/pdb1/tables.dmp` Make sure the El
    Carro operator has read access to the bucket containing the dump file:

    a. If you're not using
    <a href="/kubernetes-engine/docs/how-to/workload-identity#alternatives">workload
    identity</a>, use the Compute Engine default service account for GCS access.

    b. If you have enabled workload identity on GKE, you can
    <a href="/kubernetes-engine/docs/how-to/workload-identity#concepts">configure
    the Kubernetes service account to act as a Google service account</a>.

    You can run the following command to see whether workload identity is
    enabled for the GKE cluster or not:

     <pre class="prettyprint lang-sh">
     gcloud container clusters describe <var>CLUSTER</var> --zone=<var>ZONE</var>  --project=<var>PROJECT</var> | grep workload

     workloadMetadataConfig:
       workloadMetadataConfig:
     workloadIdentityCon fig:
        workloadPool: <var>PROJECT</var>.svc.id.goog
     </pre>

    Grant permissions using the appropriate service account:

    <pre class="prettyprint lang-sh">
     gsutil iam ch serviceAccount:<var>service_account_email</var>:objectViewer gs://example-bucket
    </pre>

    **Optionally**

    There is an optional parameter you can pass to an import with the GCS
    location of the import operation log file that you can inspect later, and
    which is produced by the `impdp`utility with the import process details. If
    you request an import log, make sure the operator can write to the
    destination GCS bucket

    <pre class="prettyprint lang-sh">
    gsutil iam ch serviceAccount:<var>gke_cluster_service_account_email</var>:objectCreator gs://example-log-bucket
    </pre>

1.  Create and apply Import CR

    Edit the `gcsPath` and optionally the `gcsLogPath` attributes in the sample
    import resource:

    <pre class="prettyprint lang-sh">
    cat <var>PATH_TO_EL_CARRO_RELEASE</var>/samples/v1alpha1_import_pdb1.yaml
    </pre>

    <pre class="prettyprint yaml">
    apiVersion: oracle.db.anthosapis.com/v1alpha1
    kind: Import
    metadata:
      name: import-pdb1
    spec:
      instance: mydb
      databaseName: pdb1
      type: DataPump
      # Service account should have read access to the destination bucket,
      # sample command to grant read access (replace with actual SA email):
      # > gsutil iam ch serviceaccount:SA@PROJECT.iam.gserviceaccount.com:objectViewer gs://ex-bucket
      gcsPath: "gs://example-bucket/import/pdb1/import.dmp"
      # Uncomment to enable import log upload to Google Cloud Storage.
      # Service account should have write access to the destination bucket,
      # sample command to grant access (replace with actual SA email):
      # > gsutil iam ch serviceaccount:SA@PROJECT.iam.gserviceaccount.com:objectCreator gs://ex-bucket
      #  Add .gz as Google Cloud Storage object file extension to enable compression.
      gcsLogPath: "gs://example-log-bucket/import/pdb1.log"
    </pre>

    `instance` and `databaseName` must refer to existing `Instance` and
    `Database` custom resources names in the namespace the `Import` resource
    will be created in.

    After the manifest is ready, submit it to the cluster as follows:

    <pre class="prettyprint lang-sh">
    kubectl apply -f <var>PATH_TO_EL_CARRO_RELEASE</var>/samples/v1alpha1_import_pdb1 -n <var>NAMESPACE</var>
    </pre>

1.  (Optional) Inspect the result of creating an Import resource

    Check the Import custom resource status:

    <pre class="prettyprint lang-sh">
    kubectl get imports.oracle.db.anthosapis.com -n <var>NAMESPACE</var>
    </pre>
