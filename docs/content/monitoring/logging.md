# Logging

## Before you begin

The following variables will be used in this guide:

```sh
export INSTANCE_NAME=<the name of the instance for which you want to inspect logs>
export NS=<namespace in which your El Carro instance is deployed>
export PATH_TO_EL_CARRO_RELEASE=<the complete path to the downloaded release directory>
```

You should set these variables in your environment.

## Viewing logs via kubectl

You can retrieve El Carro logs from your Kubernetes cluster via the kubectl
command, which will display the stdout and stderr streams of the specified
container (alert-log-sidecar in this example) by running:

```sh
kubectl logs -f $(kubectl get pod -n $NS -l instance=$INSTANCE_NAME -o jsonpath="{.items[0].metadata.name}") -c alert-log-sidecar -n $INSTANCE_NAME
```

This will display all the stdout/stderr output of the specified container. Aside
from the regular agents, we have provided two sidecar containers that tail the
listener log and alert log file, named _listener-log-sidecar_ and
_alert-log-sidecar_ respectively.

## Viewing logs via Cloud Console

You can also retrieve El Carro logs using the Google Cloud Logs Explorer. This
feature allows you to view logs of containers for up to 30 days. The Logs
explorer allows searching for particular messages, as well as viewing logs for
containers that have been shut down or restarted. For more information on Logs
Explorer, see the information at the
[Logs Explorer help page](https://cloud.google.com/logging/docs/view/logs-viewer-interface).

Since it may take a few minutes for the database instance to get fully
provisioned (largely depending on whether or not the database container has been
locally cached or not), the alert log won't show up in Stackdriver until
instance provisioning is complete. Once it does (see alert-log-sidecar
container's "View Logs" link), there's an option to time lock, jump to now or
stream the logs to get the latest updates automatically.

## Changing Log Verbosity Levels

The El Carro operator and its agents use the
[klog logger](https://github.com/kubernetes/klog), which is a fork of glog, and
has the ability to dynamically set the verbosity level of the logs. The default
level of log verbosity is set to 0, but it can be increased to any positive
integer value. Any log messages less than or equal to the current verbosity
level will be printed out, for example if the verbosity level is set to 2 then
log messages at level 0, 1, or 2 will be printed out, but messages at level 3 or
greater will not be printed out.

You're free to change the verbosity level of the operators or config-agent by
setting the value of the log levels in the config spec (see the
v1alpha1_config_gcp1.yaml file for an example), and then applying it with the
command below.

```sh
kubectl apply -f $PATH_TO_EL_CARRO_RELEASE/samples/v1alpha1_config_gcp1.yaml -n $NS
```
