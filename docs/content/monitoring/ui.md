# User Interface

## Installation

Run the following command to install El Carro UI.

```sh
kubectl apply -f ${PATH_TO_EL_CARRO_RELEASE}/ui.yaml
```

The output of the previous command looks like the following:

```sh
clusterrole.rbac.authorization.k8s.io/ui created
clusterrolebinding.rbac.authorization.k8s.io/ui created
deployment.apps/ui created
service/ui created
```

## Visit the Web UI

Forward the port of Web UI to [http://localhost:8080](http://localhost:8080).

```sh
kubectl port-forward -n ui svc/ui 8080:80
```

Then you can visit the Web UI at [http://localhost:8080](http://localhost:8080).
To create a new Instance, a new Database hosted on that Instance or to take a
Backup, click on the side menu on the left.

## GCP

There's no special El Carro UI available in this release other than the
Google Cloud Console to view and manage a GKE cluster with the El Carro Operator
and database in it.
