# Running El Carro Operator on local clusters with kind

This guide helps you run El Carro Operator in a [kind](https://github.com/kubernetes-sigs/kind) cluster on your personal
computer.

If you prefer to use GKE (Google Kubernetes Engine) to deploy the El
Carro Operator, stop here and refer to our [Quickstart Guide](quickstart.md) or
[Quickstart Guide for Oracle 18c XE](quickstart-18c-xe.md).

If you prefer to use minikube instead of kind as a local cluster, refer to our [Minikube Guide](minikube.md).

## Before you begin

The following variables will be used in this guide:

```sh
export PATH_TO_EL_CARRO_REPO=<the complete path to the directory that contains the cloned El Carro repository>
export NS=<Namespace where you will deploy your El Carro instance, for example "db">
```

You should set these variables in your environment.

## Install kind, Docker and kubectl

*   Install kind by following the official kind [Installation Guide](https://kind.sigs.k8s.io/docs/user/quick-start/#installation).
*   Install [kubectl](https://kubernetes.io/docs/tasks/tools/) to interact with the kind cluster.
*   Install Docker to build images locally.
*   Make sure you have access to El Carro source code through Github because we will build container images locally and load them into the kind cluster.

## Prepare a kind cluster

1.  Create a kind cluster by running:

    ```sh
    kind create cluster
    ```
    
    By default, the cluster will be given the name "kind".
    
2.  Verify that your kind cluster was created and set as the current
    context:

    ```sh
    kubectl config current-context
    ```

    This should print:
    ```sh
    kind-kind
    ```

3. Install a recent version of [CSI snapshotter](https://github.com/kubernetes-csi/external-snapshotter) to kind cluster
    ```sh
    SNAPSHOTTER_VERSION=v4.1.0
   
    # Install VolumeSnapshot CRDs
    kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml
    kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml
    kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml
   
    # Create Snapshot controller
    kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml
    kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml
    ```

4.  Install the [CSI Hostpath Driver](https://github.com/kubernetes-csi/csi-driver-host-path) to kind cluster
    ```sh
    git clone https://github.com/kubernetes-csi/csi-driver-host-path.git
    
    cd csi-driver-host-path
    ./deploy/kubernetes-1.18/deploy.sh
    
    kubectl apply -f ./examples/csi-storageclass.yaml
    ```

5.  Install and setup [MetalLB](https://github.com/metallb/metallb) in kind cluster, so that "LoadBalancer" type service can work
    
    a. First enable strict ARP mode by editing kube-proxy config and set "strictARP: true"
    ```sh
    kubectl edit configmap -n kube-system kube-proxy
    ```
    
    b. Install MetalLB using default manifests and wait for metallb pods to reach running status
    ```shell script
    kubectl apply -f https://raw.githubusercontent.com/metallb/metallb/master/manifests/namespace.yaml
    kubectl create secret generic -n metallb-system memberlist --from-literal=secretkey="$(openssl rand -base64 128)" 
    kubectl apply -f https://raw.githubusercontent.com/metallb/metallb/master/manifests/metallb.yaml
    
    kubectl get pods -n metallb-system --watch
    ```
    
    c. Find kind docker container network cidr range and convert it to IP addresses range using this [tool](https://www.ipaddressguide.com/cidr)
    ```shell script
    docker network inspect -f '{{.IPAM.Config}}' kind
    ```
    
    d. Change the following script to configure MetalLB based on IP addresses range from previous step
    ```shell script
    cat > metallb-configmap.yaml <<'EOF'
    apiVersion: v1
    kind: ConfigMap
    metadata:
      namespace: metallb-system
      name: config
    data:
      config: |
        address-pools:
        - name: default
          protocol: layer2
          addresses:
          # REPLACE FOLLOWING VALUE WITH YOURS
          - 192.168.11.0-192.168.11.255
    EOF

    kubectl apply -f metallb-configmap.yaml
    ```

    e. In a separate terminal, launch a local docker repository to recieve the
    El Carro images.
    ```sh
    docker run --rm -d -p 5000:5000 --restart=always --name registry registry:2
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
{"repositories":["oracle-12.2-ee-seeded-mydb","local/oracle.db.anthosapis.com/dbinit","local/oracle.db.anthosapis.com/loggingsidecar","local/oracle.db.anthosapis.com/monitoring","local/oracle.db.anthosapis.com/operator"]}
```

### Setup a namespace

Setup a namespace where you will apply your custom resources (El carro instance,
database, etc). For the linked user guides referencing a namespace, you should
use the namespace you created in this step.

```sh
kubectl create namespace $NS
```

## Creating a kind config CR

To override the default csi driver and image settings used for GKE, apply the
kind specific config CR by running:

```sh
kubectl apply -f $PATH_TO_EL_CARRO_REPO/oracle/config/samples/v1alpha1_config_kind.yaml -n $NS
```

You must apply the config CR before you create El Carro instances so kind specific configurations can be picked up by El Carro.

### Creating an El Carro instance

```sh
kubectl apply -f $PATH_TO_EL_CARRO_REPO/oracle/config/samples/v1alpha1_instance_local.yaml -n $NS
```

Follow the [instance provisioning user guide](provision/instance.md) to learn
how to provision more complex types of El Carro instances.

## Delete the kind cluster

Kind cluster can be deleted using the following command:
```sh
kind delete cluster
```
