# Monitoring and Dashboards

In this release only basic OS, cluster and Oracle metrics are collected
through Prometheus and can be visualized with Grafana.

## Monitoring Containers Setup

To set up monitoring, run the installation script. This will deploy Prometheus,
alert manager, node-exporter and Grafana on your Kubernetes cluster. The
deployment will be in a separate namespace called "monitoring".

```sh
cd ${PATH_TO_EL_CARRO_RELEASE}
chmod +x ./setup_monitoring.sh
./setup_monitoring.sh install
```

## Deploying Oracle Database Monitoring

Once the installation is done, configure your El Carro instance to start the
Monitoring Service. To do this, edit your Instance manifest. For example:

```sh
vi ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_instance.yaml
```

Include the following line:

```sh
  services:
    Monitoring:true
```

Apply the configuration:

```sh
kubectl apply -f ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_instance.yaml -n $NS
```

## Set up OracleDB As Monitor Target

This step points Prometheus to start scraping the Oracle DB monitoring agent.

```sh
kubectl apply -f ${PATH_TO_EL_CARRO_RELEASE}/db_monitor.yaml
```

## Viewing Monitoring Metrics in Prometheus

To view the monitoring metrics in Prometheus you need to port forward the
prometheus service:

```sh
kubectl port-forward svc/prometheus-k8s 9090 -n monitoring
```

You can now access prometheus on:

```sh
http://localhost:9090
```

## Dashboards

Dashboards are set up in Grafana. To access the dashboard, set up the port
forwarding as follows:

```sh
kubectl port-forward svc/grafana 3000 -n monitoring
```

In your browser navigate to

```sh
http://localhost:3000/
```

You will be prompted for a username and password. Use admin for both values. You
will be prompted immediately to change the admin password.

Once you log into Grafana, choose Dashboards and then Manage.


## Uninstalling Monitoring

To uninstall the monitoring operator and ALL containers, run the
following command:

```sh
${PATH_TO_EL_CARRO_RELEASE}/setup_monitoring.sh uninstall
```
If you see the error
`fatal: destination path 'kube-prometheus' already exists and is not an empty directory.`
then delete `kube-prometheus` directory and rerun the uninstall script.
