# Running El Carro Operator on Amazon cloud (AWS)

This guide helps you run El Carro Operator in [Amazon Elastic Kubernetes Service (EKS)](https://aws.amazon.com/eks/).

If you prefer to use GKE (Google Kubernetes Engine) to deploy the El
Carro Operator, stop here and refer to the [Quickstart Guide](quickstart.md) or
[Quickstart Guide for Oracle 18c XE](quickstart-18c-xe.md).

If you prefer to use a local cluster on your personal computer instead of AWS EKS, refer to either the [Minikube Guide](minikube.md) or [Kind Guide](kind.md).

## Before you begin

We used a t2.micro EC2 instance to run this test, as of today the [AWS CloudShell doesn’t support Docker containers](https://docs.aws.amazon.com/cloudshell/latest/userguide/vm-specs.html).
All steps were executed as the ec2-user from within the EC2 instance:

## Install docker, kubectl and eksctl 

* Install Docker - needed to check images in repository or build images locally.

    ```shell script
    sudo amazon-linux-extras install docker
    sudo yum update -y
    sudo service docker start
    sudo usermod -a -G docker ec2-user
    reboot
    ```

* Install [kubectl](https://docs.aws.amazon.com/eks/latest/userguide/install-kubectl.html) to interact with Kubernetes clusters.

    ```shell script
    curl -o kubectl https://amazon-eks.s3.us-west-2.amazonaws.com/1.20.4/2021-04-12/bin/linux/amd64/kubectl
    chmod +x ./kubectl
    mkdir -p $HOME/bin && mv ./kubectl $HOME/bin/kubectl && export PATH=$PATH:$HOME/bin
    echo 'export PATH=$PATH:$HOME/bin' >> ~/.bashrc
    kubectl version --short --client
    Client Version: v1.20.4-eks-6b7464
    ```

*  Install [eksctl](https://docs.aws.amazon.com/eks/latest/userguide/eksctl.html) to interact with the EKS cluster, which automates many individual tasks.

    ```shell script
    curl --silent --location "https://github.com/weaveworks/eksctl/releases/latest/download/eksctl_$(uname -s)_amd64.tar.gz" | tar xz -C /tmp
    sudo mv /tmp/eksctl /usr/local/bin
    eksctl version
     0.55.0
    ```

*   Make sure you have access to the latests El Carro source code from Github 

    ```shell script
    wget https://github.com/GoogleCloudPlatform/elcarro-oracle-operator/releases/download/v0.0.0-alpha/release-artifacts.tar.gz
    tar -xzvf release-artifacts.tar.gz
    ```

## Configure the AWS client

```shell script
aws configure
```

Enter the following data from your account, which is available in your [AWS console](https://docs.aws.amazon.com/general/latest/gr/aws-sec-cred-types.html):

```
AWS Access Key ID [None]: <your AK id>
AWS Secret Access Key [None]: <your SAK>
Default region name [None]: <your region>
```

## Set environment variables

```sh
export PATH_TO_EL_CARRO_REPO=<the complete path to the directory that contains the cloned El Carro repository>
export DBNAME=<Database name>
export CRD_NS=<Namespace where you will deploy your El Carro instance, for example "db">
export CLUSTER_NAME=<EKS cluster name>
export ZONE=<AWS region you are connected and creating resources>
export ACC_ID=<AWS account id>
```
    
Example of values used in this guide:
    
```sh
export PATH_TO_EL_CARRO_REPO=/home/ec2-user/v0.0.0-alpha
export DBNAME=GCLOUD
export CRD_NS=db
export CLUSTER_NAME=gkecluster
```

You can easily get your AWS account ID and ZONE using below commands from the EC2 instance you are connected to

```sh
export ZONE=$(curl -s http://169.254.169.254/latest/meta-data/placement/availability-zone | sed 's/\(.*\)[a-z]/\1/') 
export ACC_ID=$(aws sts get-caller-identity --query "Account" | sed 's/"//g')
```


## Creating a Containerized Oracle Database Image

The following steps create a docker container image and pushes it into an AWS ECR repository, automating the process using AWS CodeBuild.
You could create a local image instead, following similar steps as described in the [GCP quickstart guide](quickstart-18c-xe.md), changing only the URL to the docker repository for one similar to the one used below.

1. Create a service role, needed for CodeBuild as described [here](https://docs.aws.amazon.com/codebuild/latest/userguide/setting-up.html#setting-up-service-role):

    ```sh
    aws iam create-role --role-name CodeBuildServiceRole --assume-role-policy-document file://${PATH_TO_EL_CARRO_REPO}/dbimage/aws/create-service-role.json

    aws iam put-role-policy --role-name CodeBuildServiceRole --policy-name CodeBuildServiceRolePolicy --policy-document file://${PATH_TO_EL_CARRO_REPO}/dbimage/aws/put-role-policy.json
    ```

2. Create an ECR repository. We are naming it elcarro:

    ```sh
    aws ecr create-repository \
    >     --repository-name elcarro \
    >     --image-scanning-configuration scanOnPush=true \
    >     --region $ZONE
    ```
    
3. Create a zip file with the build instructions. 
   This will create the Oracle Database 18c XE container image and push it into the registry. 
   If you want to install a different Oracle version, adjust buildspec.yml file and include the proper install scripts in this zip.
  
    ```sh
    cd ${PATH_TO_EL_CARRO_REPO}/dbimage
    zip ../elCarroImage18cXe.zip buildspec.yml Dockerfile install-oracle-18c-xe.sh
    ```
    
4. Create an S3 bucket and copy above zip file
    
    ```sh
    aws s3 mb s3://codebuild-${ZONE}-${ACC_ID}-input-bucket --region $ZONE
    aws s3 --region $ZONE cp ../elCarroImage18cXe.zip s3://codebuild-${ZONE}-${ACC_ID}-input-bucket --acl public-read
    ```

5. Prepare a build JSON file for this project. 
   We have a template file, *elcarro-ECR-template.json*, to generate the build file *elcarro-ECR.json* by adding your ZONE and ACC_ID values.

    ```sh
    sed "s/<ZONE>/${ZONE}/g; s/<ACC_ID>/${ACC_ID}/g" aws/elcarro-ECR-template.json > elcarro-ECR.json
    ```
   
6. Create the build project

    ```sh
    aws codebuild create-project --cli-input-json file://elcarro-ECR.json
    ```

7. Start the build

    ```sh
    aws codebuild start-build --project-name elCarro
    ```

    To check progress from the CLI:

    ```sh
    aws codebuild list-builds
    {
       "ids": [
           "elCarro:59ef7722-06d6-4a1d-b2d5-14d967e9e4df"
       ]
    }
    ```

    We can see build details using the latest ID from the above output:

    ```sh
    aws codebuild batch-get-builds --ids elCarro:59ef7722-06d6-4a1d-b2d5-14d967e9e4df
    ```

    The build takes around 20 minutes. Once completed successfully, we can see the image in our ECR repo:

    ```sh
    aws ecr list-images --repository-name elcarro
    {
        "imageIds": [
            {
                "imageTag": "latest",
                "imageDigest": "sha256:7a2bd504abdf7c959332601b1deef98dda19418a252b510b604779a6143ec809"
            }
        ]
    }
    ```

## Create a Kubernetes cluster on EKS

We need to use a ssh key pair for this test. In this example, we are creating a new one called *myekskeys*. 
This key is stored in your account, so there’s no need to create it again if you repeat this test in the future:

```sh
aws ec2 create-key-pair --region $ZONE --key-name myekskeys
```

Next, we’ll create a local file with the content of the private key, using output from the previous command:

```sh
vi myekskeys.priv
```

We are ready to create the cluster. It will take about 25 minutes to complete:

```sh
eksctl create cluster \
    --name $CLUSTER_NAME \
    --region $ZONE \
    --with-oidc \
    --ssh-access \
    --ssh-public-key myekskeys \
    --managed
```

Below are basic checks to monitor the progress and resources as they are created:

```sh
aws eks list-clusters
aws eks describe-cluster --name $CLUSTER_NAME
eksctl get addons --cluster $CLUSTER_NAME
kubectl get nodes --show-labels --all-namespaces
kubectl get pods -o wide --all-namespaces --show-labels
kubectl get svc --all-namespaces
kubectl describe pod --all-namespaces
kubectl get events --sort-by=.metadata.creationTimestamp
```

### Add the EBS CSI driver

We need to use the [AWS-EBS CSI driver](https://github.com/kubernetes-sigs/aws-ebs-csi-driver). Below steps are from that guide.

The policy below is required only one time in your account, so there’s no need to re-execute this if you’re repeating cluster creation:

```sh
wget https://raw.githubusercontent.com/kubernetes-sigs/aws-ebs-csi-driver/master/docs/example-iam-policy.json

aws iam create-policy \
    --policy-name AmazonEKS_EBS_CSI_Driver_Policy \
    --policy-document file://example-iam-policy.json
```

The following steps are mandatory every time you need to configure the EBS CSI:

```sh
eksctl create iamserviceaccount \
    --name ebs-csi-controller-sa \
    --namespace kube-system \
    --cluster $CLUSTER_NAME \
    --attach-policy-arn arn:aws:iam::${ACC_ID}:policy/AmazonEKS_EBS_CSI_Driver_Policy \
    --approve \
    --override-existing-serviceaccounts

kubectl apply -k "github.com/kubernetes-sigs/aws-ebs-csi-driver/deploy/kubernetes/overlays/stable/?ref=master"
```

We are ready to add our storage class into the cluster. Will create the configuration YAML based on the one for GCP, adjusting a few values:

```sh
sed -e 's/provisioner: pd.csi.storage.gke.io/provisioner: ebs.csi.aws.com/; /^  type: pd-standard/d; /parameters:/d' ${PATH_TO_EL_CARRO_REPO}/deploy/csi/gce_pd_storage_class.yaml > /tmp/aws_pd_storage_class.yaml

kubectl create -f /tmp//aws_pd_storage_class.yaml
```
    
Now we’re completing the storage configuration and installing the volume snapshot class:

```sh
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/master/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/master/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/master/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/master/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/master/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml
```

And now we add the storage class into the cluster, again by adjusting the GCP configuration file to AWS services:

```sh
sed -e 's/driver: pd.csi.storage.gke.io/driver: ebs.csi.aws.com/; s#apiVersion: snapshot.storage.k8s.io/v1beta1#apiVersion: snapshot.storage.k8s.io/v1#' ${PATH_TO_EL_CARRO_REPO}/deploy/csi/gce_pd_volume_snapshot_class.yaml > /tmp/aws_pd_volume_snapshot_class.yaml

kubectl create -f /tmp/aws_pd_volume_snapshot_class.yaml
```

At this point, the cluster is ready to deploy the operator. 

A few basic sanity checks of our k8s cluster can be performed to compare later in case of any problem appears:

```sh
kubectl get storageclass --all-namespaces
kubectl describe storageclass
kubectl get pods -o wide --all-namespaces
kubectl get events --sort-by=.metadata.creationTimestamp
```


## Deploy El Carro operator and create the DB

We use the code provided by the GCP project without any change:

```sh
kubectl apply -f ${PATH_TO_EL_CARRO_REPO}/operator.yaml
```

Now we are ready to create an Oracle 18c XE database. The only change required to the YAML file used in GCP for this step is the URL for the DB image—we need the one we created a few steps ago. There’s no need to adjust other parameters as the storageClass name, as we deployed to the cluster with the same name used by the GCP example but pointing to the correct AWS driver. 

This command will create the YAML with the changes we need, based on the a sample file included in the project:

```sh
cd ${PATH_TO_EL_CARRO_REPO}/samples
sed "s#service: \"gcr.io.*#service: \"${ACC_ID}.dkr.ecr.${ZONE}.amazonaws.com/elcarro:latest\"#g; s#\${DB}#${DBNAME}#g" v1alpha1_instance_18c_XE_express.yaml > /tmp/v1alpha1_instance_18c_XE_express_AWS.yaml
```

We can continue with the steps described in the GCP guide to deploy the 18c XE database:

```sh
kubectl create ns $CRD_NS
kubectl get ns $CRD_NS

kubectl apply -f /tmp/v1alpha1_instance_18c_XE_express_AWS.yaml -n $CRD_NS
```

To monitor the progress of the deployment, we should check every component, but the most relevant information is on the mydb-sts-0 pod. Below are some commands to be used for that:

```sh
kubectl get instances -n $CRD_NS
kubectl get events --sort-by=.metadata.creationTimestamp
kubectl get nodes
kubectl get pods -n $CRD_NS
kubectl get pvc -n $CRD_NS
kubectl get svc -n $CRD_NS
kubectl logs mydb-agent-deployment-<id> -c config-agent -n $CRD_NS
kubectl logs mydb-agent-deployment-<id> -c oracle-monitoring -n $CRD_NS
kubectl logs mydb-sts-0 -c oracledb -n $CRD_NS
kubectl logs mydb-sts-0 -c dbinit -n $CRD_NS
kubectl logs mydb-sts-0 -c dbdaemon -n $CRD_NS
kubectl logs mydb-sts-0 -c alert-log-sidecar -n $CRD_NS
kubectl logs mydb-sts-0 -c listener-log-sidecar -n $CRD_NS
kubectl describe pods mydb-sts-0 -n $CRD_NS
kubectl describe pods mydb-agent-deployment-<id> -n $CRD_NS
kubectl describe node ip-<our-ip>.compute.internal
```

After a few minutes, the database instance (CDB) is created:

```sh
$ kubectl get pods -n $CRD_NS
NAME                                     READY   STATUS    RESTARTS   AGE
mydb-agent-deployment-6f7748f88b-2wlp9   1/1     Running   0          11m
mydb-sts-0                               4/4     Running   0          11m

$ kubectl get instances -n $CRD_NS
NAME   DB ENGINE   VERSION   EDITION   ENDPOINT      URL                                                                           DB NAMES   BACKUP ID   READYSTATUS   READYREASON      DBREADYSTATUS   DBREADYREASON
mydb   Oracle      18c       Express   mydb-svc.db   af8b37a22dc304391b51335848c9f7ff-678637856.us-east-2.elb.amazonaws.com:6021                          True          CreateComplete   True            CreateComplete
```

The last step is to deploy the database (PDB), which doesn't need any change from the sample file provided for GCP:

```sh
kubectl apply -f ${PATH_TO_EL_CARRO_REPO}/samples/v1alpha1_database_pdb1_express.yaml -n $CRD_NS
database.oracle.db.anthosapis.com/pdb1 created
```

After this completes, we can see instance status now includes the PDB:

```sh
$ kubectl get instances -n $CRD_NS
NAME   DB ENGINE   VERSION   EDITION   ENDPOINT      URL                                                                           DB NAMES   BACKUP ID   READYSTATUS   READYREASON      DBREADYSTATUS   DBREADYREASON
mydb   Oracle      18c       Express   mydb-svc.db   af8b37a22dc304391b51335848c9f7ff-678637856.us-east-2.elb.amazonaws.com:6021   ["pdb1"]               True          CreateComplete   True            CreateComplete
```

To connect directly to the container running the database:

```sh
kubectl  exec -it -n db mydb-sts-0 -c oracledb -- bash -i
```

Follow the [instance provisioning user guide](provision/instance.md) to learn
how to provision more complex types of El Carro instances.


## Delete resources after testing

After your tests are finished, the cluster should be deleted to avoid charges if you don’t plan to continue using it:

```sh
kubectl get svc --all-namespaces
kubectl delete svc mydb-svc -n $CRD_NS
eksctl delete cluster --name $CLUSTER_NAME
```

Even if the output from the last command shows it completed successfully, check if that is true by inspecting the detailed output using the command below. Also double-check the objects created in your AWS console, as it is known to leave resources created. Specially **Internet Gateway, Subnets, VPC, EBS volumes** and **EC2 instances**.

```sh
eksctl utils describe-stacks --region=$ZONE --cluster=$CLUSTER_NAME
```

