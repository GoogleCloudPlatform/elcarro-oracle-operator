# Data Pump: Export data from a PDB

The following variables used in the examples below:

```sh
export NAMESPACE=<kubernetes namespace where the instance was created>
export PATH_TO_EL_CARRO_RELEASE=<the complete path to the downloaded release directory>
```


Data Pump export uses the Oracle `expdp` utility for exporting data and metadata
into a set of operating system files called a dump file set. El Carro allows you
to declaratively initiate a Data Pump export. To do so:

1.  Prepare and apply an Export CR (Custom Resource):
    ```sh
    cat $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_export_dmp1.yaml
    ```

    ```yaml
    apiVersion: oracle.db.anthosapis.com/v1alpha1
    kind: Export
    metadata:
     name: export-dmp1
    spec:
     instance: mydb
     databaseName: pdb1
     type: DataPump
     exportObjectType: Schemas # 'Schemas' or 'Tables'
     exportObjects:
     -SCOTT
     # Uncomment flashbackTime to enable flashback time feature
     # Time is in RFC3339 for datetime format,
     # for example 1985-04-12T23:20:50.52Z represents 20 minutes and 50.52 seconds after the 23rd hour of April 12th, 1985 in UTC.
     # before enabling make sure undo_retention settings are consistent with set time
     # flashbackTime: "2021-01-05T15:00:00Z"  #optional

     # Service account should have write access to the destination bucket,
     # sample command to grant access (replace with actual SA email):
     # > gsutil iam ch serviceaccount:SA@PROJECT.iam.gserviceaccount.com:objectCreator gs://example-bucket
     #  Add .gz as GCS object file extension to enable compression.
     gcsPath: "gs://example-bucket/elcarro/export/pdb1/exportSchema.dmp"
     gcsLogPath: "gs://example-bucket/elcarro/export/pdb1/exportSchema.log" #optional
    ```

2.  Submit the Export CR

    ```sh
    kubectl apply -f $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_export_dmp1.yaml -n $NAMESPACE
    ```

### ExportObjectType and ExportObjects fields

El Carro currently supports Data Pump export in Schema (default export mode) and
Table mode. Export mode can be set in the `exportObjectType` field of the export
CR. A list of objects to be exported, schemas or tables, should be specified in
the `exportObjects` field. For example, to export tables instead of schemas,
change `exportObjectType` and `exportObjects` fields:

```sh
exportObjectType: Tables
exportObjects:
- SCOTT.t1
- SCOTT.t2
```

### FlashbackTime field

<code>[FlashbackTime](https://docs.oracle.com/cd/B28359_01/server.111/b28319/dp_export.htm#i1007150)</code>
field in the export CR is an optional timestamp in
[RFC3339](https://tools.ietf.org/html/rfc3339) format. When specified, the
system change number (SCN) that most closely matches the time is found, and this
SCN is used to enable the Flashback utility. If consistency up to a certain SCN
is desired, specify the <code>FlashbackTime</code> field.

### gcsPath field

`gcsPath` field is a full path to a GCS bucket to which the export dmp file
should be uploaded to. You must ensure that the service account used by the El
Carro operator has adequate write permissions to the GCS bucket by running the
following command:

```sh
gsutil iam ch serviceaccount:$gke_cluster_service_account_email:objectCreator gs://example-bucket
```

### gcsLogPath field

`gcsLogPath` is an optional parameter. When specified, export logs files will be
uploaded to it. Similar to `gcsPath`, write access to the GCS bucket to upload
export log files should be granted to the service account used by the El Carro
operator.

Note: For `gcsPath` and `gcsLogPath`, the .gz suffix can be added to dmp and log
files to enable compression when uploading to the GCS bucket. For example:

```sh
#  Add .gz as Google Cloud Storage object file extension to enable compression.
gcsPath: "gs://example-bucket/elcarro/export/pdb1/exportSchema.dmp.gz"
gcsLogPath: "gs://example-bucket/elcarro/export/pdb1/exportSchema.log.gz" # optional
```

```sh
#  Add .gz as GCS object file extension to enable compression.
gcsPath: "gs://example-bucket/elcarro/export/pdb1/exportSchema.dmp.gz"
gcsLogPath: "gs://example-bucket/elcarro/export/pdb1/exportSchema.log.gz" # optional
```
