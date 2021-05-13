# Client Side Connectivity

Once a database/PDB is created, database connectivity can be tested with
any of the client side tools. For examnple, using SQL\*Plus:

```sh
sqlplus <Database.Spec.Users.Name>/<Database.Spec.Users.Password>@<Instance.Status.URL>/<Database.Spec.Name>.<Instance.Spec.DbDomain>
```

In practice, this could look like the following:

```sh
sqlplus scott/tiger@ip-address:6021/pdb1.gke
```

We currently don't allow changing a listener port. Use port-forwarding if you
need to use a port other than the default. For example:

```sh
kubectl port-forward svc/graydb-svc 1521:6021 -n db
Forwarding from 127.0.0.1:1521 -> 6021
Forwarding from [::1]:1521 -> 6021
Handling connection for 1521
```

You can then connect to port 1521 (or any port of a customer choice)
using localhost:

```bash
sqlplus scott/tiger@localhost:1521/pdb1.gke
```
