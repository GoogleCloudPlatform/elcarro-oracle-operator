# Create El Carro Database(s)

This step depends on the previous step of successfully created Instance CR.
The preflight checks ensure that the Database Controller doesn't reconcile until
the Instance is found in the Ready state and the database instance accepts
connections.

Confirm that the Instance CR has been created successfully and features
ReadyStatus and DBReadyStatus of "True":

```sh
export NS=<namespace of user choice, for example: "db">
kubectl get instances.oracle.db.anthosapis.com -n $NS

NAME     DB ENGINE   VERSION   EDITION      ENDPOINT        URL                  READYSTATUS   DBREADYSTATUS   DB NAMES   BACKUP ID
mydb     Oracle      12.2      Enterprise   mydb-svc.db     34.122.76.205:6021   True          True
```

1. Prepare a Database CR Manifest

   Please note that the user / schema credentials in the manifest below appear
   in clear text and may need to be secured.

   ```sh
   cat ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_database_pdb1.yaml
   apiVersion: oracle.db.anthosapis.com/v1alpha1
   kind: Database
   metadata:
    name: pdb1
   spec:
    name: pdb1
    instance: mydb
    admin_password: google
    users:
      - name: superuser
        password: superpassword
        privileges:
          - dba
      - name: scott
        password: tiger
        privileges:
          - connect
          - resource
          - unlimited tablespace
      - name: proberuser
        password: proberpassword
        privileges:
          - create session
   ```

1. Submit the Database CR

   After completing the Database manifest, submit it to the local cluster as
   follows:

   ```sh
   kubectl apply -f ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_database_pdb1.yaml -n $NS
   ```

1. Review the Database CR

   An easy way to monitor the state of the Database CR is by running the
   following command:

   ```sh
   kubectl get databases.oracle.db.anthosapis.com -n $NS -w
   NAMESPACE   NAME   INSTANCE        USERS       STATUS
   db          pdb1   mydb            scott       Ready
   ```

   Once the Database CR status turns to Ready, the PDB database is ready to use.
   In addition to having a separate entry for each PDB in the Database CR, a
   list of PDBs is also propagated up to an Instance CR and looks like the
   following:

   ```sh
   kubectl get instances.oracle.db.anthosapis.com -n $NS
   NAME     DB ENGINE   VERSION   EDITION      ENDPOINT        URL                  READYSTATUS   DBREADYSTATUS   DB NAMES    BACKUP ID
   mydb     Oracle      12.2      Enterprise   mydb-svc.db     34.122.76.205:6021   True          True            [pdb1]
   ```

   The above steps can be repeated to create additional Databases (in particular,
   the samples supplied with El Carro release includes v1alpha1_database_pdb2.yaml).


1. Connect to a Database

   At this point a Database (PDB) should be fully functional and accessible from
   outside the cluster via an external load balancer on a public IP address and
   port 6021 (to be made configurable in future releases). The IP and the port
   are combined together in the Instance.Status.URL attribute:

   ```sh
   kubectl get instances.oracle.db.anthosapis.com -n $NS
   NAME     DB ENGINE   VERSION   EDITION      ENDPOINT        URL                  READYSTATUS   DBREADYSTATUS   DB NAMES   BACKUP ID
   mydb     Oracle      12.2      Enterprise   mydb-svc.db     34.122.76.205:6021   True          True
   ```

   As long as there's connectivity to the cluster, you should be able to
   establish a connection to a database as follows:

   ```sh
   nc -vz 34.122.76.205 6021
   Connection to 34.122.76.205 6021 port [tcp/*] succeeded!

   sqlplus scott/tiger@34.122.76.205:6021/pdb1.gke

   SQL> show user con_id con_name
   USER is "SCOTT"

   CON_ID
   ------------------------------
   3

   CON_NAME
   ------------------------------
   PDB1
   ```

   Similar to SQL*Plus, other client side database tools can be used to access
   an El Carro database.

   Once a Database CR (and the corresponding PDB) is created, it can be modified
   as described in the [appendix B](../custom-resources/database.md).
