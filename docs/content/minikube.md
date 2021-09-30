# Running El Carro Operator on local clusters with minikube

This guide helps you run El Carro Operator locally on minikube on your personal
computer. If you prefer to use GKE (Google Kubernetes Engine) to deploy the El
Carro Operator, stop here and refer to our [Quickstart Guide](quickstart.md) or
[Quickstart Guide for Oracle 18c XE](quickstart-18c-xe.md).

## Before you begin

The following variables will be used in this guide:

```sh
export PATH_TO_EL_CARRO_REPO=<the complete path to the directory that contains the cloned El Carro repository>
export NS=<Namespace where you will deploy your El Carro instance, for example "db".>
```

You should set these variables in your environment.

## Install Minikube, Docker and kubectl

*   Install minikube by following the official minikube
    [Get Started guide](https://minikube.sigs.k8s.io/docs/start/).
*   Install [kubectl](https://kubernetes.io/docs/tasks/tools/) to access the
    kubernetes cluster inside minikube
*   Install Docker to build the oracle image locally
*   Make sure you have access to El Carro source code either through Github
    because we will build container images locally and push to the local
    minikube registry.

## Prepare a minikube cluster

1.  Create a minikube cluster by running:

    ```sh
    minikube start
    ```

2.  Verify that your minikube cluster was created and set as the current
    context:

    ```sh
    kubectl config current-context
    ```

    This should print:
    ```sh
    minikube
    ```

3.  Enable the following two addons to get minikube ready for El Carro:

    ```sh
    minikube addons enable csi-hostpath-driver
    minikube addons enable volumesnapshots
    ```

4.  Enable the registry addon to allow Docker to push images to minikube's registry:
    ```sh
    minikube addons enable registry
    ```

5.  In a separate terminal, redirect port 5000 from Docker to port 5000 on
    your host by following this
    [guide](https://minikube.sigs.k8s.io/docs/handbook/registry/) or running:

    ```sh
    docker run --rm -d --network=host --name=registry-port-forwarder alpine ash -c "apk add socat && socat TCP-LISTEN:5000,reuseaddr,fork TCP:$(minikube ip):5000"
    ```

6.  Verify that you are able to access the minikube registry by running:

    ```sh
    curl http://localhost:5000/v2/_catalog
    ```

7.  After completing the steps above, running:

    ```sh
    minikube addons list
    ```

    should print:

    ```sh
    |-----------------------------|----------|--------------|
    |         ADDON NAME          | PROFILE  |    STATUS    |
    |-----------------------------|----------|--------------|
    | ambassador                  | minikube | disabled     |
    | auto-pause                  | minikube | disabled     |
    | csi-hostpath-driver         | minikube | enabled ✅   |
    | dashboard                   | minikube | disabled     |
    | default-storageclass        | minikube | enabled ✅   |
    | efk                         | minikube | disabled     |
    | freshpod                    | minikube | disabled     |
    | gcp-auth                    | minikube | disabled     |
    | gvisor                      | minikube | disabled     |
    | helm-tiller                 | minikube | disabled     |
    | ingress                     | minikube | disabled     |
    | ingress-dns                 | minikube | disabled     |
    | istio                       | minikube | disabled     |
    | istio-provisioner           | minikube | disabled     |
    | kubevirt                    | minikube | disabled     |
    | logviewer                   | minikube | disabled     |
    | metallb                     | minikube | disabled     |
    | metrics-server              | minikube | disabled     |
    | nvidia-driver-installer     | minikube | disabled     |
    | nvidia-gpu-device-plugin    | minikube | disabled     |
    | olm                         | minikube | disabled     |
    | pod-security-policy         | minikube | disabled     |
    | registry                    | minikube | enabled ✅   |
    | registry-aliases            | minikube | disabled     |
    | registry-creds              | minikube | disabled     |
    | storage-provisioner         | minikube | enabled ✅   |
    | storage-provisioner-gluster | minikube | disabled     |
    | volumesnapshots             | minikube | enabled ✅   |
    |-----------------------------|----------|--------------|
    ```

## Connect to the minikube LoadBalancer service

In order to connect to El Carro later, you need to
[connect to LoadBalancer services](https://minikube.sigs.k8s.io/docs/commands/tunnel/)
by running the following command in **a separate terminal session**:

```sh
minikube tunnel
```

## Build El Carro images locally

### Oracle database image

Follow the [Quickstart Guide](quickstart.md) to build an oracle database image
locally, then tag and push the image to the local registry:

```sh
docker tag gcr.io/local-build/oracle-database-images/oracle-12.2-ee-seeded-mydb:latest localhost:5000/oracle-12.2-ee-seeded-mydb:latest
docker push localhost:5000/oracle-12.2-ee-seeded-mydb:latest
```

### Build and push the El Carro Operator and Agent images

Configure your environment for your local registry by running:

```sh
cd $PATH_TO_EL_CARRO_REPO
export PROW_IMAGE_REPO="localhost:5000"
export PROW_IMAGE_TAG="latest"
export PROW_PROJECT="local"
```

To deploy the El Carro operator to the current kubectl context using your
locally built image, run the following:

```sh
make -C oracle deploy
```

Verify that your images were successfully pushed to your local repository by running:

```sh
curl http://localhost:5000/v2/_catalog
```

You should see an output similar to this:
```sh
{"repositories":["oracle-12.2-ee-seeded-mydb","local/oracle.db.anthosapis.com/configagent","local/oracle.db.anthosapis.com/dbinit","local/oracle.db.anthosapis.com/loggingsidecar","local/oracle.db.anthosapis.com/monitoring","local/oracle.db.anthosapis.com/operator"]}
```

### Setup a namespace:

Setup a namespace where you will apply your custom resources (El carro instance,
database, etc). For the linked user guides referencing a namespace, you should
use the namespace you created in this step.

```sh
kubectl create namespace $NS
```

## Creating a minikube config CR:

To override the default csi driver and image settings used for GKE, apply the
minikube specific config CR by running:

```sh
kubectl apply -f $PATH_TO_EL_CARRO_REPO/oracle/config/samples/v1alpha1_config_minikube.yaml -n $NS
```

You must apply the config CR before you create El Carro instances so minikube
specific configurations can be picked up by El Carro.

### Creating an El Carro instance:

```sh
kubectl apply -f $PATH_TO_EL_CARRO_REPO/oracle/config/samples/v1alpha1_instance_local.yaml -n $NS
```

Follow the [instance provisioning user guide](provision/instance.md) to learn
how to provision more complex types of El Carro instances.

## [optional] Minikube dashboard

Enable the minikube dashboard addon to see information about Kubernetes
resources in your minikube cluster:

```sh
minikube addons enable dashboard
    ▪ Using image kubernetesui/metrics-scraper:v1.0.4
    ▪ Using image kubernetesui/dashboard:v2.1.0
💡  Some dashboard features require the metrics-server addon. To enable all features please run:
    minikube addons enable metrics-server
🌟  The 'dashboard' addon is enabled
minikube dashboard
🤔  Verifying dashboard health ...
🚀  Launching proxy ...
🤔  Verifying proxy health ...
🎉  Opening http://127.0.0.1:44273/api/v1/namespaces/kubernetes-dashboard/services/http:kubernetes-dashboard:/proxy/ in your default browser...
Opening in existing browser session.
```

## Stop the minikube cluster:

```sh
minikube stop
✋  Stopping node "minikube"  ...
🛑  Powering off "minikube" via SSH ...
🛑  1 nodes stopped.
```

Using `minikube stop` will not delete the resources you've provisioned in your
cluster. You can start minikube again with `minikube start` and all the
resources you have created in the minikube cluster will be available.

## Delete the minikube cluster:

```sh
minikube delete
🔥  Deleting "minikube" in docker ...
🔥  Deleting container "minikube" ...
🔥  Removing /usr/local/google/home/${USER}/.minikube/machines/minikube ...
💀  Removed all traces of the "minikube" cluster.
```
