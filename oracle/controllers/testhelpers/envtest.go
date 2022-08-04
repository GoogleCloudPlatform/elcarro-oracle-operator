// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package testhelpers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	logg "log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/bazelbuild/rules_go/go/tools/bazel"
	snapv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	. "github.com/onsi/ginkgo"
	ginkgoconfig "github.com/onsi/ginkgo/config"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

// Reconciler is the interface to setup a reconciler for testing.
type Reconciler interface {
	SetupWithManager(manager ctrl.Manager) error
}

// Webhook is the interface to setup a webhook for testing.
type Webhook interface {
	SetupWebhookWithManager(mgr ctrl.Manager) error
}

type AdmissionWebhook interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}

// cdToRoot change to the repo root directory.
func CdToRoot(t *testing.T) {
	for {
		if _, err := os.Stat("config/crd/bases/oracle.db.anthosapis.com_instances.yaml"); err == nil {
			break
		}
		if err := os.Chdir(".."); err != nil {
			t.Fatalf("Failed to cd: %v", err)
		}
		if cwd, err := os.Getwd(); err != nil || cwd == "/" {
			t.Fatalf("Failed to find config dir")
		}
	}
}

// RandName generates a name suitable for use as a namespace with a given prefix.
func RandName(base string) string {
	seed := rand.NewSource(time.Now().UnixNano() + int64(1000000*ginkgoconfig.GinkgoConfig.ParallelNode))
	testrand := rand.New(seed)
	buf := make([]byte, 4)
	testrand.Read(buf)
	str := strings.ToLower(base32.StdEncoding.EncodeToString(buf))
	return base + "-" + str[:4]
}

// RunFunctionalTestSuite runs all specs in the current package against a
// specialized testing environment. Before running the suite, this function
// configures the test environment by taking the following actions:
//
// * Starting a control plane consisting of an etcd process and a Kubernetes API
//
//	server process.
//
// * Installing CRDs into the control plane (using provided 'schemeBuilders')
// * Starting an in-process manager in a dedicated goroutine with the given
//
//	reconcilers installed in it.
//
// These components will be torn down after the suite runs.
func RunFunctionalTestSuite(
	t *testing.T,
	k8sClient *client.Client,
	k8sManager *ctrl.Manager,
	schemeBuilders []*runtime.SchemeBuilder,
	description string,
	controllers func() []Reconciler,
	crdPaths []string,
) {
	RunFunctionalTestSuiteWithWebhooks(
		t,
		k8sClient,
		k8sManager,
		schemeBuilders,
		description,
		controllers,
		crdPaths,
		func() []Webhook { return []Webhook{} }, // No webhooks
		func() map[string]AdmissionWebhook { return nil }, // No admission webhook handlers
		[]string{}, // Use default Webhook locations
	)
}

// RunFunctionalTestSuiteWithWebhooks extends RunFunctionalTestSuite
// allowing to set up test webhooks
func RunFunctionalTestSuiteWithWebhooks(
	t *testing.T,
	k8sClient *client.Client,
	k8sManager *ctrl.Manager,
	schemeBuilders []*runtime.SchemeBuilder,
	description string,
	controllers func() []Reconciler,
	crdPaths []string,
	webhooks func() []Webhook,
	admissionWebhookHandlers func() map[string]AdmissionWebhook,
	webhookPaths []string,
) {
	// Define the test environment.
	crdPaths = append(crdPaths, filepath.Join("config", "crd", "bases"), filepath.Join("config", "crd", "testing"))

	testEnv := envtest.Environment{
		CRDDirectoryPaths:        crdPaths,
		ControlPlaneStartTimeout: 60 * time.Second, // Default 20s may not be enough for test pods.
	}
	// Set up webhooks
	if len(webhookPaths) != 0 {
		testEnv.WebhookInstallOptions = envtest.WebhookInstallOptions{
			Paths:                    webhookPaths,
			IgnoreErrorIfPathMissing: true,
		}
	}
	if runfiles, err := bazel.RunfilesPath(); err == nil {
		// Running with bazel test, find binary assets in runfiles.
		testEnv.BinaryAssetsDirectory = filepath.Join(runfiles, "external/kubebuilder_tools/bin")
	}

	BeforeSuite(func(done Done) {
		klog.SetOutput(GinkgoWriter)
		logf.SetLogger(klogr.NewWithOptions(klogr.WithFormat(klogr.FormatKlog)))
		log := logf.FromContext(nil)

		var err error
		var cfg *rest.Config

		var backoff = wait.Backoff{
			Steps:    6,
			Duration: 100 * time.Millisecond,
			Factor:   5.0,
			Jitter:   0.1,
		}
		Expect(retry.OnError(backoff, func(error) bool { return true }, func() error {
			cfg, err = testEnv.Start()
			if err != nil {
				log.Error(err, "Envtest startup failed, retrying")
			}
			return err
		})).Should(Succeed())

		for _, sb := range schemeBuilders {
			utilruntime.Must(sb.AddToScheme(scheme.Scheme))
		}

		if len(webhookPaths) != 0 {
			// start webhook server using Manager
			webhookInstallOptions := &testEnv.WebhookInstallOptions
			*k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
				Scheme:             scheme.Scheme,
				Host:               webhookInstallOptions.LocalServingHost,
				Port:               webhookInstallOptions.LocalServingPort,
				CertDir:            webhookInstallOptions.LocalServingCertDir,
				LeaderElection:     false,
				MetricsBindAddress: "0",
			})
			Expect(err).NotTo(HaveOccurred())
		} else {
			*k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
				Scheme:             scheme.Scheme,
				MetricsBindAddress: "0",
			})
			Expect(err).ToNot(HaveOccurred())
		}

		*k8sClient = (*k8sManager).GetClient()

		// Install controllers into the manager.
		for _, c := range controllers() {
			Expect(c.SetupWithManager(*k8sManager)).To(Succeed())
		}
		// Install webhooks into the manager.
		for _, c := range webhooks() {
			Expect(c.SetupWebhookWithManager(*k8sManager)).To(Succeed())
		}
		// Register admission webhook handlers into the webhook in the manager
		for path, handler := range admissionWebhookHandlers() {
			(*k8sManager).GetWebhookServer().Register(path, handler)
		}

		go func() {
			defer GinkgoRecover()
			err = (*k8sManager).Start(ctrl.SetupSignalHandler())
			Expect(err).ToNot(HaveOccurred())
		}()

		if len(webhookPaths) != 0 {
			// wait for the webhook server to get ready
			webhookInstallOptions := &testEnv.WebhookInstallOptions
			dialer := &net.Dialer{Timeout: 60 * time.Second}
			addrPort := fmt.Sprintf("%s:%d", webhookInstallOptions.LocalServingHost, webhookInstallOptions.LocalServingPort)
			Eventually(func() error {
				conn, err := tls.DialWithDialer(dialer, "tcp", addrPort, &tls.Config{InsecureSkipVerify: true})
				if err != nil {
					return err
				}
				conn.Close()
				return nil
			}, 60*time.Second, 5*time.Second).Should(Succeed())
		}

		close(done)
	}, 600)

	AfterSuite(func() {
		By("Stopping control plane")
		Expect(testEnv.Stop()).To(Succeed())
	})

	RegisterFailHandler(Fail)
	RunSpecsWithDefaultAndCustomReporters(t,
		description,
		[]Reporter{printer.NewlineReporter{}})
}

var (
	// Base image names, to be combined with PROW_IMAGE_{TAG,REPO}.
	dbInitImage          = "oracle.db.anthosapis.com/dbinit"
	loggingSidecarImage  = "oracle.db.anthosapis.com/loggingsidecar"
	monitoringAgentImage = "oracle.db.anthosapis.com/monitoring"
	operatorImage        = "oracle.db.anthosapis.com/operator"
	// Used by pitr test directly.
	PitrAgentImage = "oracle.db.anthosapis.com/pitragent"
)

// Set up kubectl config targeting PROW_PROJECT / PROW_CLUSTER / PROW_CLUSTER_ZONE
// Set envtest environment pointing to that cluster
// Create k8s client
// Install CRDs
// Create the new control plane namespace
func initK8sCluster(namespace *string) (envtest.Environment, context.Context, client.Client) {
	CdToRoot(nil)
	klog.SetOutput(GinkgoWriter)
	logf.SetLogger(klogr.NewWithOptions(klogr.WithFormat(klogr.FormatKlog)))

	log := logf.FromContext(nil)
	// Generate credentials for our test cluster.
	Expect(os.Setenv("KUBECONFIG", fmt.Sprintf("/tmp/.kubectl/config-%v", *namespace))).Should(Succeed())

	// Allow local runs to target their own GKE cluster to prevent collisions with Prow.
	var targetProject, targetCluster, targetZone string
	if targetProject = os.Getenv("PROW_PROJECT"); targetProject == "" {
		Expect(errors.New("PROW_PROJECT envvar was not set. Did you try to test without make?")).NotTo(HaveOccurred())
	}
	if targetCluster = os.Getenv("PROW_CLUSTER"); targetCluster == "" {
		Expect(errors.New("PROW_CLUSTER envar was not set. Did you try to test without make?")).NotTo(HaveOccurred())
	}
	if targetZone = os.Getenv("PROW_CLUSTER_ZONE"); targetZone == "" {
		Expect(errors.New("PROW_CLUSTER_ZONE envar was not set. Did you try to test without make?")).NotTo(HaveOccurred())
	}

	// Set up k8s credentials.
	// This operation might need retrying when executing tests in parallel.
	Expect(retry.OnError(retry.DefaultBackoff, func(error) bool { return true }, func() error {
		cmdGetCreds := exec.Command("gcloud", "container", "clusters", "get-credentials", targetCluster, "--project="+targetProject, "--zone="+targetZone)
		out, err := cmdGetCreds.CombinedOutput()
		log.Info("gcloud get-credentials", "output", string(out))
		return err
	})).Should(Succeed())

	// load the test gcp project config
	cfg, err := config.GetConfig()
	log.Info("Load kubectl config")
	Expect(err).NotTo(HaveOccurred())

	trueValue := true
	env := envtest.Environment{
		UseExistingCluster: &trueValue,
		Config:             cfg,
		CRDDirectoryPaths: []string{
			filepath.Join("config", "crd", "bases"),
		},
		CRDInstallOptions: envtest.CRDInstallOptions{CleanUpAfterUse: false},
	}

	var CRDBackoff = wait.Backoff{
		Steps:    6,
		Duration: 100 * time.Millisecond,
		Factor:   5.0,
		Jitter:   0.1,
	}

	// env.Start() may fail on the same set of CRDs during parallel execution
	// need to retry in that case.
	Expect(retry.OnError(CRDBackoff, func(error) bool { return true }, func() error {
		_, err = env.Start()
		if err != nil {
			log.Error(err, "Envtest startup failed: CRD conflict, retrying")
		}
		return err
	})).Should(Succeed())

	err = v1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = snapv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	k8sClient, err := client.New(cfg, client.Options{})
	Expect(err).NotTo(HaveOccurred())

	nsObj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: *namespace,
			Labels: map[string]string{
				"control-plane": "controller-manager",
			},
		},
	}
	ctx := context.Background()
	Expect(k8sClient.Create(ctx, nsObj)).Should(Succeed())
	return env, ctx, k8sClient
}

// Remove namespace (and all corresponding objects).
// Remove kubectl config.
func cleanupK8Cluster(CPNamespace string, DPNamespace string, k8sClient client.Client) {
	cpnsObj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: CPNamespace,
			Labels: map[string]string{
				"control-plane": "controller-manager",
			},
		},
	}
	dpnsObj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: DPNamespace,
		},
	}
	if k8sClient != nil {
		policy := metav1.DeletePropagationForeground
		k8sClient.Delete(context.Background(), cpnsObj, &client.DeleteOptions{
			PropagationPolicy: &policy,
		})
		k8sClient.Delete(context.Background(), dpnsObj, &client.DeleteOptions{
			PropagationPolicy: &policy,
		})

	}
	os.Remove(fmt.Sprintf("/tmp/.kubectl/config-%v", CPNamespace))
}

// EnableGsmApi ensures the GSM API enabled for PROW_PROJECT.
func EnableGsmApi() {
	// Enable GSM API.
	log := logg.New(GinkgoWriter, "", 0)
	projectId := os.Getenv("PROW_PROJECT")
	Expect(projectId).ToNot(BeEmpty())
	cmd := exec.Command("gcloud", "services", "enable", "secretmanager.googleapis.com", fmt.Sprintf("--project=%s", projectId))
	out, err := cmd.CombinedOutput()
	log.Printf("gcloud services enable secretmanager.googleapis.com output=%s", string(out))
	Expect(err).NotTo(HaveOccurred())
}

// EnableIamApi ensures the IAM API enabled for PROW_PROJECT.
func EnableIamApi() {
	// Enable IAM API.
	log := logg.New(GinkgoWriter, "", 0)
	projectId := os.Getenv("PROW_PROJECT")
	Expect(projectId).ToNot(BeEmpty())
	cmd := exec.Command("gcloud", "services", "enable", "iamcredentials.googleapis.com", fmt.Sprintf("--project=%s", projectId))
	out, err := cmd.CombinedOutput()
	log.Printf("gcloud services enable iamcredentials.googleapis.com output=%s", string(out))
	Expect(err).NotTo(HaveOccurred())
}

// EnableWiWithNodePool ensures workload identity enabled for PROW_CLUSTER.
func EnableWiWithNodePool() {
	log := logg.New(GinkgoWriter, "", 0)

	projectId := os.Getenv("PROW_PROJECT")
	targetCluster := os.Getenv("PROW_CLUSTER")
	targetZone := os.Getenv("PROW_CLUSTER_ZONE")
	Expect(projectId).ToNot(BeEmpty())
	Expect(targetCluster).ToNot(BeEmpty())
	Expect(targetZone).NotTo(BeEmpty())

	// Enable workload identify on existing cluster.
	cmd := exec.Command("gcloud", "container", "clusters", "update", targetCluster, "--workload-pool="+projectId+".svc.id.goog", "--zone="+targetZone, fmt.Sprintf("--project=%s", projectId))
	out, err := cmd.CombinedOutput()
	log.Printf("gcloud container clusters update output=%s", string(out))
	Expect(err).NotTo(HaveOccurred())
	// Migrate applications to Workload Identity with Node pool modification.
	cmd = exec.Command("gcloud", "container", "node-pools", "update", "default-pool", "--cluster="+targetCluster, "--workload-metadata=GKE_METADATA", "--zone="+targetZone, fmt.Sprintf("--project=%s", projectId))
	out, err = cmd.CombinedOutput()
	log.Printf("gcloud container node-pools update output=%s", string(out))
	Expect(err).NotTo(HaveOccurred())
}

// PrintEvents for all namespaces in the cluster.
func PrintEvents() {
	cmd := exec.Command("kubectl", "get", "events", "-A", "-o", "custom-columns=LastSeen:.metadata.creationTimestamp,From:.source.component,Type:.type,Reason:.reason,Message:.message", "--sort-by=.metadata.creationTimestamp")
	out, err := cmd.CombinedOutput()
	if err != nil {
		logf.FromContext(nil).Error(err, "Failed to get events")
		return
	}
	log := logg.New(GinkgoWriter, "", 0)
	log.Println("=============================")
	log.Printf("Last events:\n %s\n", out)
}

// Print pods for all namespaces in the cluster
func PrintPods() {
	cmd := exec.Command("kubectl", "get", "pods", "-A", "-o", "wide")
	out, err := cmd.CombinedOutput()
	if err != nil {
		logf.FromContext(nil).Error(err, "Failed to get pods")
		return
	}
	log := logg.New(GinkgoWriter, "", 0)
	log.Println("=============================")
	log.Printf("Pods:\n %s\n", out)
}

// Print svcs for all namespaces in the cluster
func PrintSVCs() {
	cmd := exec.Command("kubectl", "get", "svc", "-A", "-o", "wide")
	out, err := cmd.CombinedOutput()
	if err != nil {
		logf.FromContext(nil).Error(err, "Failed to get svcs")
		return
	}
	log := logg.New(GinkgoWriter, "", 0)
	log.Println("=============================")
	log.Printf("SVCs:\n %s\n", out)
}

// Print PVCs for all namespaces in the cluster
func PrintPVCs() {
	cmd := exec.Command("kubectl", "get", "pvc", "-A", "-o", "wide")
	out, err := cmd.CombinedOutput()
	if err != nil {
		logf.FromContext(nil).Error(err, "Failed to get pvcs")
		return
	}
	log := logg.New(GinkgoWriter, "", 0)
	log.Println("=============================")
	log.Printf("PVCs:\n %s\n", out)
}

// Print ENV variables
func PrintENV() {
	log := logg.New(GinkgoWriter, "", 0)
	log.Println("=============================")
	log.Println("ENV:")
	for _, e := range os.Environ() {
		log.Println(e)
	}
}

// Prints logs for a typical single-instance test scenario in case of failure:
// Prints logs for 'manager', 'dbdaemon', 'oracledb' containers.
// Prints cluster objects.
// Stores Oracle trace logs to a local dir (or Prow Artifacts).
func PrintSimpleDebugInfo(k8sEnv K8sOperatorEnvironment, instanceName string, CDBName string) {
	PrintLogs(k8sEnv.CPNamespace, k8sEnv.DPNamespace, k8sEnv.Env, []string{"manager", "dbdaemon", "oracledb"}, []string{instanceName})
	PrintClusterObjects()
	var pod = instanceName + "-sts-0"
	if err := StoreOracleLogs(pod, k8sEnv.DPNamespace, instanceName, CDBName); err != nil {
		logf.FromContext(nil).Error(err, "StoreOracleLogs failed")
	}
}

// Print cluster objects - events, pods, pvcs for all namespaces in the cluster
func PrintClusterObjects() {
	PrintENV()
	PrintEvents()
	PrintPods()
	PrintPVCs()
	PrintSVCs()
}

// Print logs from requested containers
func PrintLogs(CPNamespace string, DPNamespace string, env envtest.Environment, dumpLogsFor []string, instances []string) {
	log := logg.New(GinkgoWriter, "", 0)
	for _, c := range dumpLogsFor {
		var logs string
		var err error

		// Make the log start a bit easier to distinguish.
		log.Println("=============================")
		if c == "manager" {
			logs, err = getOperatorLogs(context.Background(), env.Config, CPNamespace)
			if err != nil {
				log.Printf("Failed to get %s logs: %s\n", c, err)
			} else {
				log.Printf("%s logs:\n %s\n", c, logs)
			}
		} else {
			for _, inst := range instances {
				logs, err = getAgentLogs(context.Background(), env.Config, DPNamespace, inst, c)
				if err != nil {
					log.Printf("Failed to get %s %s logs: %s\n", inst, c, err)
				} else {
					log.Printf("%s %s logs:\n %s\n", inst, c, logs)
				}
			}
		}
	}

}

// DeployOperator deploys an operator and returns a cleanup function to delete
// all cluster level objects created outside of the namespace.
func deployOperator(ctx context.Context, k8sClient client.Client, CPNamespace, DPNamespace string) (func() error, error) {
	var agentImageTag, agentImageRepo, agentImageProject string
	if agentImageTag = os.Getenv("PROW_IMAGE_TAG"); agentImageTag == "" {
		return nil, errors.New("PROW_IMAGE_TAG envvar was not set. Did you try to test without make?")
	}
	if agentImageRepo = os.Getenv("PROW_IMAGE_REPO"); agentImageRepo == "" {
		return nil, errors.New("PROW_IMAGE_REPO envar was not set. Did you try to test without make?")
	}
	if agentImageProject = os.Getenv("PROW_PROJECT"); agentImageProject == "" {
		return nil, errors.New("PROW_PROJECT envar was not set. Did you try to test without make?")
	}

	dbInitImage := fmt.Sprintf("%s/%s/%s:%s", agentImageRepo, agentImageProject, dbInitImage, agentImageTag)
	loggingSidecarImage := fmt.Sprintf("%s/%s/%s:%s", agentImageRepo, agentImageProject, loggingSidecarImage, agentImageTag)
	monitoringAgentImage := fmt.Sprintf("%s/%s/%s:%s", agentImageRepo, agentImageProject, monitoringAgentImage, agentImageTag)
	operatorImage := fmt.Sprintf("%s/%s/%s:%s", agentImageRepo, agentImageProject, operatorImage, agentImageTag)
	// Global modified for usage in pitr test.
	PitrAgentImage = fmt.Sprintf("%s/%s/%s:%s", agentImageRepo, agentImageProject, PitrAgentImage, agentImageTag)

	objs, err := readYamls([]string{
		"config/manager/manager.yaml",
		"config/rbac/role.yaml",
		"config/rbac/role_binding.yaml",
	})
	if err != nil {
		return nil, err
	}

	// minimal set of operator.yaml we need to deploy.
	var d *appsv1.Deployment
	var cr *rbacv1.ClusterRole
	var crb *rbacv1.ClusterRoleBinding
	for _, obj := range objs {
		if _, ok := obj.(*appsv1.Deployment); ok {
			d = obj.(*appsv1.Deployment)
		}
		if _, ok := obj.(*rbacv1.ClusterRole); ok {
			if cr != nil {
				return nil, fmt.Errorf("test needs to be updated to handle multiple ClusterRoles")
			}
			cr = obj.(*rbacv1.ClusterRole)
		}
		if _, ok := obj.(*rbacv1.ClusterRoleBinding); ok {
			if crb != nil {
				return nil, fmt.Errorf("test needs to be updated to handle multiple ClusterRoleBindings")
			}
			crb = obj.(*rbacv1.ClusterRoleBinding)
		}
	}

	//Check DPNamespace against CPNamespace. If they're different, create the DPNamespace.
	if CPNamespace != DPNamespace {
		By("Deploying data plane in " + DPNamespace)
		nsObj := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: DPNamespace,
			},
		}
		ctx := context.Background()
		Expect(k8sClient.Create(ctx, nsObj)).Should(Succeed())
	}

	// Add in our overrides.
	cr.ObjectMeta.Name = "manager-role-" + CPNamespace
	crb.ObjectMeta.Name = "manager-rolebinding-" + CPNamespace
	crb.RoleRef.Name = cr.ObjectMeta.Name
	crb.Subjects[0].Namespace = CPNamespace
	d.Namespace = CPNamespace
	d.Spec.Template.Spec.Containers[0].Image = operatorImage
	d.Spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullAlways
	d.Spec.Template.Spec.Containers[0].Args = []string{
		"--logtostderr=true",
		"--enable-leader-election=false",
		"--namespace=" + DPNamespace,
		"--db_init_image_uri=" + dbInitImage,
		"--logging_sidecar_image_uri=" + loggingSidecarImage,
		"--monitoring_agent_image_uri=" + monitoringAgentImage,
	}

	// Ensure account has cluster admin to create ClusterRole/Binding. You
	// can figure out the k8s account name from a GCE service account name
	// using the `uniqueId` property from `gcloud iam service-accounts
	// describe some@service.account`.
	if err := k8sClient.Create(ctx, cr); err != nil {
		return nil, err
	}
	if err := k8sClient.Create(ctx, crb); err != nil {
		k8sClient.Delete(ctx, cr)
		return nil, err
	}
	if err := k8sClient.Create(ctx, d); err != nil {
		k8sClient.Delete(ctx, cr)
		k8sClient.Delete(ctx, crb)
		return nil, err
	}

	// Ensure deployment succeeds.
	instKey := client.ObjectKey{Namespace: CPNamespace, Name: d.Name}
	Eventually(func() int {
		err := k8sClient.Get(ctx, instKey, d)
		if err != nil {
			return 0
		}
		return int(d.Status.ReadyReplicas)
	}, 10*time.Minute, 5*time.Second).Should(Equal(1))

	return func() error {
		if err := k8sClient.Delete(ctx, cr); err != nil {
			return err
		}
		if err := k8sClient.Delete(ctx, crb); err != nil {
			return err
		}
		return nil
	}, nil
}

func readYamls(files []string) ([]runtime.Object, error) {
	var objs []runtime.Object

	decoder := serializer.NewCodecFactory(scheme.Scheme).UniversalDeserializer()
	for _, f := range files {
		data, err := ioutil.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("error reading '%s': %v", f, err)
		}
		parts := bytes.Split(data, []byte("\n---"))

		// role.yaml is generated by kubebuilder with an empty yaml
		// doc, this wont decode so we need to filter it out first.
		for _, part := range parts {
			if cleaned := bytes.TrimSpace(part); len(cleaned) > 0 {
				obj, err := runtime.Decode(decoder, cleaned)
				if err != nil {
					return nil, fmt.Errorf("error decoding '%s': %v", f, err)
				}

				objs = append(objs, obj)
			}
		}
	}

	return objs, nil
}

func getOperatorLogs(ctx context.Context, config *rest.Config, namespace string) (string, error) {
	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", err
	}

	pod, err := FindPodFor(ctx, clientSet, namespace, "control-plane=controller-manager")
	if err != nil {
		return "", err
	}
	return getContainerLogs(ctx, clientSet, namespace, pod.Name, "manager")
}

func getAgentLogs(ctx context.Context, config *rest.Config, namespace, instance, agent string) (string, error) {
	// The label selector to find the target agent container. Different
	// labels are use for the CSA/NCSA agents to associate the deployments
	// with the instance.
	agentToQuery := map[string]string{
		// NCSA Agents
		"oracle-monitoring": "deployment=" + instance + "-agent-deployment",
		// CSA Agents
		"oracledb":             "instance=" + instance,
		"dbdaemon":             "instance=" + instance,
		"alert-log-sidecar":    "instance=" + instance,
		"listener-log-sidecar": "instance=" + instance,
	}

	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", err
	}

	pod, err := FindPodFor(ctx, clientSet, namespace, agentToQuery[agent])
	if err != nil {
		return "", err
	}
	return getContainerLogs(ctx, clientSet, namespace, pod.Name, agent)
}

func getContainerLogs(ctx context.Context, clientSet *kubernetes.Clientset, ns, p, c string) (string, error) {
	logOpts := corev1.PodLogOptions{
		Container: c,
	}
	req := clientSet.CoreV1().Pods(ns).GetLogs(p, &logOpts)
	podLogs, err := req.Stream(context.Background())
	if err != nil {
		return "", err
	}

	sb := strings.Builder{}
	_, err = io.Copy(&sb, podLogs)
	if err != nil {
		return "", err
	}
	return sb.String(), nil
}

func FindPodFor(ctx context.Context, clientSet *kubernetes.Clientset, ns, filter string) (*corev1.Pod, error) {
	listOpts := metav1.ListOptions{
		LabelSelector: filter,
	}
	pods, err := clientSet.CoreV1().Pods(ns).List(ctx, listOpts)
	if err != nil {
		return nil, err
	}
	if len(pods.Items) < 1 {
		return nil, fmt.Errorf("couldnt find Pod in %q matching %q", ns, filter)
	}
	if len(pods.Items) > 1 {
		return nil, fmt.Errorf("found multiple Pods in %q matching %q:\n%+v", ns, filter, pods.Items)
	}
	return &pods.Items[0], nil
}

// GCloudServiceAccount returns the GCloud service account name.
func GCloudServiceAccount() string {
	return fmt.Sprintf(
		"%s@%s.iam.gserviceaccount.com",
		os.Getenv("PROW_INT_TEST_SA"),
		os.Getenv("PROW_PROJECT"))
}

/*
K8sOperatorEnvironment is a helper for integration testing.

Encapsulates all necessary variables to work with the test cluster
Can be created/destroyed multiple times within one test suite
Depends on the Ginkgo asserts
Example usage:

// Global variable, to be accessible by AfterSuite.
var k8sEnv = testhelpers.K8sEnvironment{}
// In case of Ctrl-C, clean up the last valid k8sEnv.
AfterSuite(func() {

	k8sEnv.Close()

})
...
BeforeEach(func() {

	k8sEnv.Init(testhelpers.RandName("k8s-env-stress-test"))

})
AfterEach(func() {

	k8sEnv.Close()

})
*/
type K8sOperatorEnvironment struct {
	Env               envtest.Environment
	CPNamespace       string
	DPNamespace       string
	Ctx               context.Context
	K8sClient         client.Client
	OperCleanup       func() error // Operator deployment cleanup callback.
	TestFailed        bool         // If true then dump container logs.
	K8sServiceAccount string
}

// Init the environment, install CRDs, deploy operator, declare namespaces for control plane and data plane.
func (k8sEnv *K8sOperatorEnvironment) Init(CPNamespace string, DPNamespace string) {
	// K8S Service account
	k8sEnv.K8sServiceAccount = os.Getenv("PROW_PROJECT") + ".svc.id.goog[" + CPNamespace + "/default]"

	By("Starting control plane " + CPNamespace)
	// Init cluster
	k8sEnv.CPNamespace = CPNamespace
	k8sEnv.DPNamespace = DPNamespace
	k8sEnv.Env, k8sEnv.Ctx, k8sEnv.K8sClient = initK8sCluster(&k8sEnv.CPNamespace)
	// Deploy operator
	By("Deploying operator in " + CPNamespace)
	// Deploy Operator, retry if necessary
	Expect(retry.OnError(retry.DefaultBackoff, func(error) bool { return true }, func() error {
		defer GinkgoRecover()
		var err error
		k8sEnv.OperCleanup, err = deployOperator(k8sEnv.Ctx, k8sEnv.K8sClient, k8sEnv.CPNamespace, k8sEnv.DPNamespace)
		if err != nil {
			logf.FromContext(nil).Error(err, "DeployOperator failed, retrying")
		}
		return err
	})).Should(Succeed())
}

// Close cleans cluster objects and uninstalls operator.
func (k8sEnv *K8sOperatorEnvironment) Close() {
	if k8sEnv.CPNamespace == "" && k8sEnv.DPNamespace == "" {
		return
	}
	By("Stopping control plane " + k8sEnv.CPNamespace)
	Expect(k8sEnv.Env.Stop()).To(Succeed())

	if k8sEnv.OperCleanup != nil {
		By("Uninstalling operator " + k8sEnv.CPNamespace)
		k8sEnv.OperCleanup()
	}
	if k8sEnv.K8sClient == nil {
		return
	}

	cleanupK8Cluster(k8sEnv.CPNamespace, k8sEnv.DPNamespace, k8sEnv.K8sClient)
	k8sEnv.CPNamespace = ""
	k8sEnv.DPNamespace = ""
}

// Instance-specific helper functions.

// TestImageForVersion returns service image for integration tests.
// Image paths are predefined in the env variables TEST_IMAGE_ORACLE_*.
func TestImageForVersion(version string, edition string, extra string) string {
	switch edition {
	case "XE":
		{
			switch version {
			case "18c":
				{
					switch extra {
					default:
						return os.Getenv("TEST_IMAGE_ORACLE_18_XE_SEEDED")
					}
				}
			}
		}
	case "EE":
		{
			switch version {
			case "19.3":
				{
					switch extra {
					case "unseeded-32545013":
						return os.Getenv("TEST_IMAGE_ORACLE_19_3_EE_UNSEEDED_32545013")
					case "unseeded":
						return os.Getenv("TEST_IMAGE_ORACLE_19_3_EE_UNSEEDED_32545013")
					case "seeded-buggy":
						return os.Getenv("TEST_IMAGE_ORACLE_19_3_EE_SEEDED_BUGGY")
					case "ocr":
						return os.Getenv("TEST_IMAGE_OCR_ORACLE_19_3_EE_UNSEEDED_29517242")
					default:
						return os.Getenv("TEST_IMAGE_ORACLE_19_3_EE_SEEDED")
					}
				}
			}
		}
	}
	return "INVALID_VERSION"
}

// CreateSimpleInstance creates a basic v1alpha1.Instance object named 'instanceName'.
// 'version' and 'edition' should match rules of TestImageForVersion().
// Depends on the Ginkgo asserts.
func CreateSimpleInstance(k8sEnv K8sOperatorEnvironment, instanceName string, version string, edition string) {
	instance := &v1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName,
			Namespace: k8sEnv.DPNamespace,
		},
		Spec: v1alpha1.InstanceSpec{
			CDBName: "GCLOUD",
			InstanceSpec: commonv1alpha1.InstanceSpec{
				Version: version,
				Disks: []commonv1alpha1.DiskSpec{
					{
						Name: "DataDisk",
						Size: resource.MustParse("45Gi"),
					},
					{
						Name: "LogDisk",
						Size: resource.MustParse("55Gi"),
					},
				},
				DatabaseResources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("9Gi"),
					},
				},
				Images: map[string]string{
					"service": TestImageForVersion(version, edition, ""),
				},
				DBLoadBalancerOptions: &commonv1alpha1.DBLoadBalancerOptions{
					GCP: commonv1alpha1.DBLoadBalancerOptionsGCP{LoadBalancerType: "Internal"},
				},
			},
		},
	}

	K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, instance)
	instKey := client.ObjectKey{Namespace: k8sEnv.DPNamespace, Name: instanceName}

	// Wait until the instance is "Ready" (requires 5+ minutes to download image).
	WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, 20*time.Minute)
}

// CreateSimplePdbWithDbObj creates simple PDB by given database object.
func CreateSimplePdbWithDbObj(k8sEnv K8sOperatorEnvironment, database *v1alpha1.Database) {
	pod := database.Spec.Instance + "-sts-0"
	K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, database)
	// Wait for the PDB to come online (UserReady = "SyncComplete").
	emptyObj := &v1alpha1.Database{}
	objectKey := client.ObjectKey{Namespace: k8sEnv.DPNamespace, Name: database.Name}
	WaitForObjectConditionState(k8sEnv, objectKey, emptyObj, k8s.UserReady, metav1.ConditionTrue, k8s.SyncComplete, 7*time.Minute, k8s.FindConditionOrFailed)

	// Open PDBs.
	out := K8sExecuteSqlOrFail(pod, k8sEnv.DPNamespace, "alter pluggable database all open;")
	Expect(out).To(Equal(""))
}

// CreateSimplePDB creates a simple PDB 'pdb1' inside 'instanceName' Instance.
// Depends on the Ginkgo asserts.
func CreateSimplePDB(k8sEnv K8sOperatorEnvironment, instanceName string) {
	CreateSimplePdbWithDbObj(k8sEnv, &v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: k8sEnv.DPNamespace,
			Name:      "pdb1",
		},
		Spec: v1alpha1.DatabaseSpec{
			DatabaseSpec: commonv1alpha1.DatabaseSpec{
				Name:     "pdb1",
				Instance: instanceName,
			},
			AdminPassword: "123456",
			Users: []v1alpha1.UserSpec{
				{
					UserSpec: commonv1alpha1.UserSpec{
						Name: "scott",
						CredentialSpec: commonv1alpha1.CredentialSpec{
							Password: "tiger",
						},
					},
					Privileges: []v1alpha1.PrivilegeSpec{"connect", "resource", "unlimited tablespace"},
				},
			},
		},
	})
}

// InsertSimpleData creates 'test_table' in pdb1 and inserts a test row.
func InsertSimpleData(k8sEnv K8sOperatorEnvironment) {
	pod := "mydb-sts-0"
	// Insert test data
	sql := `alter session set container=pdb1;
alter session set current_schema=scott;
create table test_table (name varchar(100));
insert into test_table values ('Hello World');
commit;`
	out := K8sExecuteSqlOrFail(pod, k8sEnv.DPNamespace, sql)
	Expect(out).To(Equal(""))
}

// VerifySimpleData checks that the test row in 'pdb1' exists.
func VerifySimpleData(k8sEnv K8sOperatorEnvironment) {
	pod := "mydb-sts-0"
	sql := `alter session set container=pdb1;
alter session set current_schema=scott;
select name from test_table;`
	Expect(K8sExecuteSqlOrFail(pod, k8sEnv.DPNamespace, sql)).To(Equal("Hello World"))
}

// InsertData creates <table> in <pdb> and inserts <value>.
func InsertData(pod, ns, pdb, user, table, value string) {
	// Insert test data
	sql := fmt.Sprintf(`alter session set container=%s;
alter session set current_schema=%s;
create table %s (name varchar(100));
insert into %s values ('%s');
commit;`, pdb, user, table, table, value)
	out := K8sExecuteSqlOrFail(pod, ns, sql)
	Expect(out).To(Equal(""))
}

// VerifyData checks that <value> in <pdb> exists.
func VerifyData(pod, ns, pdb, user, table, value string) {
	sql := fmt.Sprintf(`alter session set container=%s;
alter session set current_schema=%s;
select name from %s;`, pdb, user, table)
	Expect(K8sExecuteSqlOrFail(pod, ns, sql)).To(Equal(value))
}

// WaitForObjectConditionState waits until the k8s object condition object status = targetStatus
// and reason = targetReason.
// Objects supported: v1alpha1. {Instance, Import, Export}
// Depends on the Ginkgo asserts.
func WaitForObjectConditionState(k8sEnv K8sOperatorEnvironment,
	key client.ObjectKey,
	emptyObj client.Object,
	condition string,
	targetStatus metav1.ConditionStatus,
	targetReason string,
	timeout time.Duration,
	findConditionOrFailed func(conditions []metav1.Condition, name string) (bool, *metav1.Condition)) {
	Eventually(func() bool {
		K8sGetWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, key, emptyObj)
		failed := false
		cond := &metav1.Condition{}
		switch emptyObj.(type) {
		case *v1alpha1.Instance:
			failed, cond = findConditionOrFailed(emptyObj.(*v1alpha1.Instance).Status.Conditions, condition)
		case *v1alpha1.Import:
			failed, cond = findConditionOrFailed(emptyObj.(*v1alpha1.Import).Status.Conditions, condition)
		case *v1alpha1.Export:
			failed, cond = findConditionOrFailed(emptyObj.(*v1alpha1.Export).Status.Conditions, condition)
		case *v1alpha1.Database:
			failed, cond = findConditionOrFailed(emptyObj.(*v1alpha1.Database).Status.Conditions, condition)
		}
		if cond != nil {
			logf.FromContext(nil).Info(fmt.Sprintf("Waiting %v, status=%v:%v, expecting=%v:%v", condition, cond.Status, cond.Reason, targetStatus, targetReason))
			done := cond.Status == targetStatus && cond.Reason == targetReason
			if !done && failed { // Allow for expecting a "Failed" condition.
				Fail(fmt.Sprintf("Failed %v, status=%v:%v message=%v, expecting=%v:%v", condition, cond.Status, cond.Reason, cond.Message, targetStatus, targetReason))
			}
			return done

		}
		return false
	}, timeout, 5*time.Second).Should(Equal(true))
}

// WaitForInstanceConditionState waits until the Instance condition object status = targetStatus and reason = targetReason.
// Depends on the Ginkgo asserts.
func WaitForInstanceConditionState(k8sEnv K8sOperatorEnvironment, key client.ObjectKey, condition string, targetStatus metav1.ConditionStatus, targetReason string, timeout time.Duration) {
	instance := &v1alpha1.Instance{}
	WaitForObjectConditionState(k8sEnv, key, instance, condition, targetStatus, targetReason, timeout, k8s.FindConditionOrFailed)
}

// K8sCopyFromPodOrFail copies file/dir in src path of the pod to local dest path.
// Depends on kubectl
// kubectl cp <pod>:<src> dest -n <ns> -c <container>
func K8sCopyFromPodOrFail(pod, ns, container, src, dest string) {
	cmd := exec.Command("kubectl", "cp", fmt.Sprintf("%s:%s", pod, src), dest, "-n", ns, "-c", container)
	output, err := cmd.CombinedOutput()
	logf.FromContext(nil).Info("k8s copy from pod", "cmd", cmd, "output", string(output), "err", err)
	Expect(err).NotTo(HaveOccurred())
}

// UploadFileOrFail uploads an object to GCS, it raises a ginkgo assert on failure.
func UploadFileOrFail(localFile, bucket, object string) {
	Expect(uploadFile(localFile, bucket, object)).NotTo(HaveOccurred())
}

// uploadFile uploads an object to GCS.
func uploadFile(localFile, bucket, object string) error {
	ctx := context.Background()
	c, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("storage.NewClient: %v", err)
	}
	defer c.Close()

	// Open local file.
	f, err := os.Open(localFile)
	if err != nil {
		return fmt.Errorf("os.Open: %v", err)
	}
	defer f.Close()

	ctx, cancel := context.WithTimeout(ctx, time.Minute*5)
	defer cancel()

	// Upload an object with storage.Writer.
	wc := c.Bucket(bucket).Object(object).NewWriter(ctx)
	if _, err = io.Copy(wc, f); err != nil {
		return fmt.Errorf("io.Copy: %v", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("Writer.Close: %v", err)
	}
	return nil
}

// K8sExec execs a command in a pod and returns a string result.
// Depends on the Ginkgo asserts.
// kubectl exec <pod> <cmd> -n <ns> -c <container>
func K8sExec(pod string, ns string, container string, cmd string) (string, error) {
	cfg, err := ctrl.GetConfig()
	Expect(err).NotTo(HaveOccurred())
	clientset, err := kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	var p = controllers.ExecCmdParams{
		Pod: pod,
		Ns:  ns,
		Con: &corev1.Container{
			Name: container,
		},
		Sch:        runtime.NewScheme(),
		RestConfig: cfg,
		Client:     clientset,
	}
	// Execute sh -c <cmd>
	out, err := controllers.ExecCmdFunc(p, cmd)
	// Trim the output.
	out = strings.TrimSpace(out)
	logf.FromContext(nil).Info("Pod exec result", "output", out, "err", err)
	return out, err
}

/*
K8sExecuteSql executes multiple sql statements in an Oracle pod
e.g.
sql := `alter session set container=pdb1;
create table test_table (name varchar(100));
insert into test_table values ('Hello World');
commit;`
out, err = testhelpers.K8sExecuteSql("mydb-sts-0", "db",	sql)
Depends on the Ginkgo asserts.
Please escape any bash special characters.
*/
func K8sExecuteSql(pod string, ns string, sql string) (string, error) {
	cmd := fmt.Sprintf(`source ~/GCLOUD.env && sqlplus -S / as sysdba <<EOF
whenever sqlerror exit sql.sqlcode;
set pagesize 0
set feedback off
set verify off
set heading off
set echo off
%s
EOF
`, sql)
	return K8sExec(
		pod,
		ns,
		"oracledb",
		cmd)
}

// K8sExecuteSqlOrFail is the same as K8sExecuteSql but raises a ginkgo assert on
// failure.
func K8sExecuteSqlOrFail(pod, ns, sql string) string {
	result, err := K8sExecuteSql(pod, ns, sql)
	Expect(err).NotTo(HaveOccurred())
	return result
}

// K8sVerifyUserConnectivity verified user connectivity on "oracledb" container.
// Or raise ginkgo assertion on failure.
// 5 retried in 30 second for each user is performed to workaround potential
// password sync latency between Config Server and Oracle DB.
func K8sVerifyUserConnectivity(pod, ns, pdb string, userCred map[string]string) {
	for user, password := range userCred {
		Eventually(func() bool {
			cmd := fmt.Sprintf(`cd ~ && source ~/GCLOUD.env && sqlplus -S %s/%s@localhost:6021/%s <<EOF
whenever sqlerror exit sql.sqlcode
set pagesize 0
set feedback off
set verify off
set heading off
set echo off
SELECT 1 FROM DUAL;
EOF
`, user, password, pdb)
			out, err := K8sExec(
				pod,
				ns,
				"oracledb",
				cmd)
			if err != nil {
				log := logf.Log
				log.Error(err, "K8sVerifyUserConnectivity sql executed", "output", out)
			}
			return err == nil && out == "1"
		}, time.Second*30, time.Second*5).Should(Equal(true))
	}
}

// Helper functions for functional and integration tests.
// Uses ginkgo asserts.

const RetryTimeout = time.Second * 5
const RetryInterval = time.Second * 1

// K8sCreateWithRetry calls k8s Create() with retry as k8s might require this in some cases (e.g. conflicts).
func K8sCreateWithRetry(k8sClient client.Client, ctx context.Context, obj client.Object) {
	Eventually(
		func() error {
			return k8sClient.Create(ctx, obj)
		}, RetryTimeout, RetryInterval).Should(Succeed())
}

// K8sGetWithRetry calls k8s Get() with retry as k8s might require this in some cases (e.g. conflicts).
func K8sGetWithRetry(k8sClient client.Client, ctx context.Context, objKey client.ObjectKey, obj client.Object) {
	Eventually(
		func() error {
			return k8sClient.Get(ctx, objKey, obj)
		}, RetryTimeout, RetryInterval).Should(Succeed())
}

// K8sDeleteWithRetryNoWait calls k8s Delete() with retry as k8s might require
// this in some cases (e.g. conflicts).
func K8sDeleteWithRetryNoWait(k8sClient client.Client, ctx context.Context, objKey client.ObjectKey, obj client.Object) {
	Eventually(
		func() error {
			return k8sClient.Delete(ctx, obj)
		}, RetryTimeout, RetryInterval).Should(Succeed())
}

// K8sDeleteWithRetry calls k8s Delete() with retry as k8s might require
// this in some cases (e.g. conflicts).
// Waits until the object gets deleted.
// Important: namespace objects never get completely deleted in testenv,
// use K8sDeleteWithRetryNoWait for deleting them
// https://github.com/kubernetes-sigs/controller-runtime/issues/880
func K8sDeleteWithRetry(k8sClient client.Client, ctx context.Context, objKey client.ObjectKey, obj client.Object) {
	Eventually(
		func() error {
			return k8sClient.Delete(ctx, obj)
		}, RetryTimeout, RetryInterval).Should(Succeed())

	Eventually(
		func() error {
			return k8sClient.Get(ctx, objKey, obj)
		}, RetryTimeout, RetryInterval).Should(Not(Succeed()))
}

// Get a fresh version of the object into 'emptyObj' using 'objKey'.
// Apply user-supplied modifyObjectFunc() which should modify the 'emptyObj'.
// Try to update 'emptyObj' in k8s, retry if needed.
// Wait until the object gets updated.
func k8sUpdateWithRetryHelper(k8sClient client.Client,
	ctx context.Context,
	objKey client.ObjectKey,
	emptyObj client.Object,
	modifyObjectFunc func(*client.Object),
	updateStatus bool) {
	originalRV := ""
	Eventually(
		func() error {
			// Get a fresh version of the object
			K8sGetWithRetry(k8sClient, ctx, objKey, emptyObj)
			// Save resource version
			originalRV = emptyObj.GetResourceVersion()
			// Apply modifyObjectFunc()
			modifyObjectFunc(&emptyObj)
			if updateStatus {
				// Try to update status in k8s
				if err := k8sClient.Status().Update(ctx, emptyObj); err != nil {
					logf.FromContext(nil).Error(err, "Failed to update object, retrying")
					return err
				}
			} else {
				// Try to update object in k8s
				if err := k8sClient.Update(ctx, emptyObj); err != nil {
					logf.FromContext(nil).Error(err, "Failed to update object, retrying")
					return err
				}
			}
			return nil
		}, RetryTimeout, RetryInterval).Should(Succeed())

	// Wait until RV has changed
	Eventually(
		func() string {
			// Get a fresh version of the object
			err := k8sClient.Get(ctx, objKey, emptyObj)
			if apierrors.IsNotFound(err) {
				return originalRV + "-deleted"
			}
			Expect(err).ToNot(HaveOccurred())
			return emptyObj.GetResourceVersion()
		}, RetryTimeout, RetryInterval).Should(Not(Equal(originalRV)))
}

// K8sUpdate makes the Get-Modify-Update-Retry cycle easier.
// Get a fresh version of the object into 'emptyObj' using 'objKey'.
// Apply user-supplied modifyObjectFunc() which should modify the 'emptyObj'.
// Try to update 'emptyObj' in k8s, retry if needed.
// Wait until the object gets updated.
func K8sUpdateWithRetry(k8sClient client.Client,
	ctx context.Context,
	objKey client.ObjectKey,
	emptyObj client.Object,
	modifyObjectFunc func(*client.Object)) {
	k8sUpdateWithRetryHelper(k8sClient, ctx, objKey, emptyObj, modifyObjectFunc, false)
}

// K8sUpdateStatus makes the Get-Modify-UpdateStatus-Retry cycle easier
// Get a fresh version of the object into 'emptyObj' using 'objKey'
// Apply user-supplied modifyObjectFunc() which should modify the 'emptyObj'
// Try to update 'emptyObj' status in k8s, retry if needed
// Wait until the object gets updated.
func K8sUpdateStatusWithRetry(k8sClient client.Client,
	ctx context.Context,
	objKey client.ObjectKey,
	emptyObj client.Object,
	modifyObjectFunc func(*client.Object)) {
	k8sUpdateWithRetryHelper(k8sClient, ctx, objKey, emptyObj, modifyObjectFunc, true)
}

// K8sCreateAndGet calls k8s Create() with retry and then wait for the object to be created.
// Updates 'createdObj' with the created object.
func K8sCreateAndGet(k8sClient client.Client, ctx context.Context, objKey client.ObjectKey, obj client.Object, createdObj client.Object) {
	K8sCreateWithRetry(k8sClient, ctx, obj)
	Eventually(
		func() error {
			return k8sClient.Get(ctx, objKey, createdObj)
		}, RetryTimeout, RetryInterval).Should(Succeed())
}

// K8sWaitForUpdate waits until GetResourceVersion changes
// compared to 'originalRV'. Updates 'emptyObj' with the new object.
func K8sWaitForUpdate(k8sClient client.Client,
	ctx context.Context,
	objKey client.ObjectKey,
	emptyObj client.Object,
	originalRV string) {
	Eventually(
		func() string {
			// Get a fresh version of the object
			K8sGetWithRetry(k8sClient, ctx, objKey, emptyObj)
			return emptyObj.GetResourceVersion()
		}, RetryTimeout, RetryInterval).Should(Not(Equal(originalRV)))
}

// SetupServiceAccountBindingBetweenGcpAndK8s creates IAM policy binding between
// k8s service account <projectId>.svc.id.goog[<NAMESPACE>/default]
// and google service account.
func SetupServiceAccountBindingBetweenGcpAndK8s(k8sEnv K8sOperatorEnvironment) {
	Expect(retry.OnError(retry.DefaultBackoff, func(error) bool { return true }, func() error {
		cmd := exec.Command("gcloud", "iam",
			"service-accounts", "add-iam-policy-binding",
			"--role=roles/iam.workloadIdentityUser",
			"--member="+"serviceAccount:"+k8sEnv.K8sServiceAccount,
			GCloudServiceAccount())
		out, err := cmd.CombinedOutput()
		logf.FromContext(nil).Info("gcloud iam service-accounts add-iam-policy-binding", "output", string(out))
		return err
	})).To(Succeed())
	saObj := &corev1.ServiceAccount{}
	K8sUpdateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx,
		client.ObjectKey{Namespace: k8sEnv.CPNamespace, Name: "default"},
		saObj,
		func(obj *client.Object) {
			// Add service account annotation.
			(*obj).(*corev1.ServiceAccount).ObjectMeta.Annotations = map[string]string{
				"iam.gke.io/gcp-service-account": GCloudServiceAccount(),
			}
		})
}

// K8sCopyFromPodOrFail copies file/dir in src path of the pod to local dest path.
// Depends on kubectl
// kubectl cp <pod>:<src> dest -n <ns> -c <container>
func k8sCopyFromPod(pod, ns, container, src, dest string) error {
	cmd := exec.Command("kubectl", "cp", fmt.Sprintf("%s:%s", pod, src), dest, "-n", ns, "-c", container)
	logf.FromContext(nil).Info(cmd.String())
	return cmd.Run()
}

// StoreOracleLogs saves Oracle's trace logs from oracledb pod.
// Stores to $ARTIFACTS in case of a Prow job or in
// a temporary directory if running locally.
func StoreOracleLogs(pod string, ns string, instanceName string, CDBName string) error {
	var storePath string
	artifactsDir := os.Getenv("ARTIFACTS")
	if artifactsDir != "" { // Running in Prow
		storePath = filepath.Join(artifactsDir, ns, instanceName)
		if err := os.MkdirAll(storePath, 0755); err != nil {
			return fmt.Errorf("os.MkdirAll failed: %v", err)
		}
	} else { // Running locally
		tmpDir, err := ioutil.TempDir("", "oracledb")
		if err != nil {
			return fmt.Errorf("TempDir failed: %v", err)
		}
		storePath = tmpDir
	}
	zone := "uscentral1a"
	logf.FromContext(nil).Info("Collecting Oracle logs")
	oracleLogPath := fmt.Sprintf("/u02/app/oracle/diag/rdbms/%s_%s/%s/trace/",
		strings.ToLower(CDBName), zone, CDBName)
	if err := k8sCopyFromPod(pod, ns, "oracledb", oracleLogPath, storePath); err != nil {
		return fmt.Errorf("k8sCopyFromPod failed: %v", err)
	}
	logf.FromContext(nil).Info(fmt.Sprintf("Stored Oracle /trace/ to %s", storePath))
	return nil
}

// Wrapper over exec.Command, prints the command line and the output.
// Raises Ginkgo assert on failure.
func execCommand(name string, arg ...string) {
	out, err := exec.Command(name, arg...).CombinedOutput()
	logf.FromContext(nil).Info(fmt.Sprintf("%s %s", name, strings.Join(arg, " ")), "output", string(out))
	Expect(err).NotTo(HaveOccurred())
}

// Create a new set of agent images from existing adding -v2 to the name
// Throw Ginkgo assert on failure.
func CreateV1V2Images(k8sEnv K8sOperatorEnvironment) (map[string]string, map[string]string) {
	agentImageRepo := os.Getenv("PROW_IMAGE_REPO")
	Expect(agentImageRepo).NotTo(Equal(""))
	agentImageTag := os.Getenv("PROW_IMAGE_TAG")
	Expect(agentImageTag).NotTo(Equal(""))
	tmpContainerName := fmt.Sprintf("temp_container_%s", k8sEnv.CPNamespace)
	agentImageProject := os.Getenv("PROW_PROJECT")
	Expect(agentImageProject).NotTo(Equal(""))

	v1Images := map[string]string{
		"dbinit":          fmt.Sprintf("%s/%s/%s:%s", agentImageRepo, agentImageProject, dbInitImage, agentImageTag),
		"logging_sidecar": fmt.Sprintf("%s/%s/%s:%s", agentImageRepo, agentImageProject, loggingSidecarImage, agentImageTag),
		"monitoring":      fmt.Sprintf("%s/%s/%s:%s", agentImageRepo, agentImageProject, monitoringAgentImage, agentImageTag),
		"operator":        fmt.Sprintf("%s/%s/%s:%s", agentImageRepo, agentImageProject, operatorImage, agentImageTag),
	}

	v2Images := map[string]string{
		"dbinit":          fmt.Sprintf("%s/%s/%s-v2:%s", agentImageRepo, agentImageProject, dbInitImage, agentImageTag),
		"logging_sidecar": fmt.Sprintf("%s/%s/%s-v2:%s", agentImageRepo, agentImageProject, loggingSidecarImage, agentImageTag),
		"monitoring":      fmt.Sprintf("%s/%s/%s-v2:%s", agentImageRepo, agentImageProject, monitoringAgentImage, agentImageTag),
		"operator":        fmt.Sprintf("%s/%s/%s-v2:%s", agentImageRepo, agentImageProject, operatorImage, agentImageTag),
	}

	// Create a new file NEW_VERSION
	f, err := os.Create("NEW_VERSION")
	Expect(err).NotTo(HaveOccurred())
	defer f.Close()

	for k, v := range v1Images {
		// Create a v2 copy of the image
		// gcloud container images add-tag -q $IMAGE_URL_1 $IMAGE_URL_2
		execCommand("gcloud", "container", "images", "add-tag", "-q", v, v2Images[k])

		// Add /NEW_VERSION file to the image, upload it back

		// docker pull $IMAGE_URL
		execCommand("docker", "pull", v2Images[k])
		// docker create --name temp_container $IMAGE_URL noop
		execCommand("docker", "create", "--name", tmpContainerName, v2Images[k], "noop")
		// docker cp NEW_VERSION temp_container:/NEW_VERSION
		execCommand("docker", "cp", "NEW_VERSION", fmt.Sprintf("%s:/NEW_VERSION", tmpContainerName))
		// docker commit temp_container $IMAGE_URL
		execCommand("docker", "commit", tmpContainerName, v2Images[k])
		// docker push $IMAGE_URL
		execCommand("docker", "push", v2Images[k])
		// docker rm temp_container
		execCommand("docker", "rm", tmpContainerName)
	}

	return v1Images, v2Images
}

// Returns true if 'PROW_CANARY_JOB' env is set.
// Canary Job is supposed to host all long-running tests.
func IsCanaryJob() bool {
	return os.Getenv("PROW_CANARY_JOB") != ""
}
