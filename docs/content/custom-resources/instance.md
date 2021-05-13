# Appendix A: Create an El Carro Instance: Advanced  {: #appendix-a}

The `samples` directory provided with the El Carro release contains a set of
useful manifests to get you started. As you start rolling out El Carro services
to many databases, it may become tedious to keep track and maintain consistency
across manifests even with the rigorous version control practices. This is in
part because the sample manifests lack a common origin, the "root of
manifests".

One way to start on the path of creating declarative workflows is to parametrize
the template YAMLs, keep them DRY and hydrate per application. The `workflows`
directory can help you to get started. Here's how an Instance template manifest
can be hydrated:

```sh
kpt cfg create-setter ${PATH_TO_EL_CARRO_RELEASE}/workflows namespace "<your-ns>"
<br>
kpt cfg create-setter ${PATH_TO_EL_CARRO_RELEASE}/workflows services --type array --field spec.services
<br>
kpt cfg create-setter ${PATH_TO_EL_CARRO_RELEASE}/workflows dbimage "<db-GCR-location>"
```

The result of running this command is a fully hydrated and ready to apply
Instance manifest:

```sh
apiVersion: oracle.db.anthosapis.com/v1alpha1
kind: Instance
metadata:
  name: mydb
  namespace: "<your-ns>" # {"$kpt-set":"namespace"}
spec:
  type: Oracle
  version: "12.2"
  edition: Enterprise
  DBDomain: "gke"
  disks:
  - name: DataDisk
    size: 45Gi
    type: pd-standard
  - name: LogDisk
    size: 55Gi
    type: pd-standard
  services: # {"$kpt-set":"services"}
  - "<your-services>" # {"$kpt-set":"services"}
  images:
    service: "<your-db-GCR-location>" # {"$kpt-set":"dbimage"}
  sourceCidrRanges: [0.0.0.0/0]
  minMemoryForDBContainer: 4.0Gi
  maintenanceWindow:
    timeRanges:
    - start: "2121-04-20T15:45:30Z"
      duration: "168h"

  #  parameters:
  #    parallel_servers_target: "15"
  #    disk_asynch_io: "true"

# Uncomment this section to trigger a restore.
#  restore:
#    backupType: "Snapshot" #(or "Physical")
#    backupId: "mydb-20200705-snap-996678001"
#    force: True
#    # once applied, new requests with same or older time will be ignored,
#    # current time can be generated using the command: date -u '+%Y-%m-%dT%H:%M:%SZ'
#    requestTime: "2000-01-19T01:23:45Z"
#    # Physical backup specific attributes:
#    dop: 2
#    # The unit for time limit is minutes (but specify just an integer).
#    timeLimitMinutes: 180
```

Given that this manifest is the same as the one provided in the `samples`
directory (but hydrated dynamically by way of the safe variable substitution),
the process
[described earlier](#submit-cr)
of applying this manifest fully applies.
