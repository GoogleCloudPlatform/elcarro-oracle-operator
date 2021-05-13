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

package main

import (
	"context"
	"flag"
	"os"

	snapv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/backupcontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/backupschedulecontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/configcontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/cronanythingcontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/databasecontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/exportcontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/importcontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/instancecontroller"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	dbInitImage          = flag.String("db_init_image_uri", "gcr.io/elcarro/oracle.db.anthosapis.com/dbinit:latest", "DB POD init binary image URI")
	serviceImage         = flag.String("service_image_uri", "", "GCR service URI")
	configAgentImage     = flag.String("config_image_uri", "gcr.io/elcarro/oracle.db.anthosapis.com/configagent:latest", "Config Agent image URI")
	loggingSidecarImage  = flag.String("logging_sidecar_image_uri", "gcr.io/elcarro/oracle.db.anthosapis.com/loggingsidecar:latest", "Logging Sidecar image URI")
	monitoringAgentImage = flag.String("monitoring_agent_image_uri", "gcr.io/elcarro/oracle.db.anthosapis.com/monitoring:latest", "Monitoring Agent image URI")

	namespace = flag.String("namespace", "", "TESTING ONLY: Limits controller to watching resources in this namespace only")
)

func init() {
	_ = snapv1.AddToScheme(scheme)

	_ = clientgoscheme.AddToScheme(scheme)

	_ = v1alpha1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=releases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oracle.db.anthosapis.com,resources=releases/status,verbs=get;update;patch

func main() {
	klog.InitFlags(nil)

	var metricsAddr string
	var enableLeaderElection bool
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	flag.Parse()

	ctrl.SetLogger(klogr.New())

	images := make(map[string]string)
	images["dbinit"] = *dbInitImage
	images["service"] = *serviceImage
	images["config"] = *configAgentImage
	images["logging_sidecar"] = *loggingSidecarImage
	images["monitoring"] = *monitoringAgentImage

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		LeaderElection:     enableLeaderElection,
		LeaderElectionID:   "controller-leader-election-helper",
		Port:               9443,
		Namespace:          *namespace,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&instancecontroller.InstanceReconciler{
		Client:        mgr.GetClient(),
		Log:           ctrl.Log.WithName("controllers").WithName("Instance"),
		Scheme:        mgr.GetScheme(),
		Images:        images,
		ClientFactory: &controllers.GrpcConfigAgentClientFactory{},
		Recorder:      mgr.GetEventRecorderFor("instance-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Instance")
		os.Exit(1)
	}
	if err = (&databasecontroller.DatabaseReconciler{
		Client:        mgr.GetClient(),
		Log:           ctrl.Log.WithName("controllers").WithName("Database"),
		Scheme:        mgr.GetScheme(),
		ClientFactory: &controllers.GrpcConfigAgentClientFactory{},
		Recorder:      mgr.GetEventRecorderFor("database-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Database")
		os.Exit(1)
	}
	if err = (&backupcontroller.BackupReconciler{
		Client:        mgr.GetClient(),
		Log:           ctrl.Log.WithName("controllers").WithName("Backup"),
		Scheme:        mgr.GetScheme(),
		ClientFactory: &controllers.GrpcConfigAgentClientFactory{},
		Recorder:      mgr.GetEventRecorderFor("backup-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Backup")
		os.Exit(1)
	}
	if err = (&configcontroller.ConfigReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controllers").WithName("Config"),
		Scheme:   mgr.GetScheme(),
		Images:   images,
		Recorder: mgr.GetEventRecorderFor("config-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Config")
		os.Exit(1)
	}
	if err = (&exportcontroller.ExportReconciler{
		Client:        mgr.GetClient(),
		Log:           ctrl.Log.WithName("controllers").WithName("Export"),
		Scheme:        mgr.GetScheme(),
		ClientFactory: &controllers.GrpcConfigAgentClientFactory{},
		Recorder:      mgr.GetEventRecorderFor("export-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Export")
		os.Exit(1)
	}
	if err = (&importcontroller.ImportReconciler{
		Client:        mgr.GetClient(),
		Log:           ctrl.Log.WithName("controllers").WithName("Import"),
		Scheme:        mgr.GetScheme(),
		ClientFactory: &controllers.GrpcConfigAgentClientFactory{},
		Recorder:      mgr.GetEventRecorderFor("import-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Import")
		os.Exit(1)
	}

	if err = backupschedulecontroller.NewBackupScheduleReconciler(mgr).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "BackupSchedule")
		os.Exit(1)
	}

	cronAnythingReconciler, err := cronanythingcontroller.NewCronAnythingReconciler(mgr)
	if err != nil {
		setupLog.Error(err, "unable to build controller", "controller", "CronAnything")
		os.Exit(1)
	}

	if err := cronAnythingReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to Add controller", "controller", "CronAnything")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	// Use the testing namespace if supplied, otherwise deploy to the same namespace as the operator.
	operatorNS := "operator-system"
	if *namespace != "" {
		operatorNS = *namespace
	}

	c := mgr.GetClient()

	ctx := context.Background()
	release := &v1alpha1.Release{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "oracle.db.anthosapis.com/v1alpha1",
			Kind:       "Release",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "release",
			Namespace: operatorNS,
		},
		Spec: v1alpha1.ReleaseSpec{
			Version: version,
		},
	}

	err = c.Create(ctx, release)

	if apierrors.IsAlreadyExists(err) {
		if err := c.Patch(ctx, release, client.Apply, client.ForceOwnership, client.FieldOwner("release-controller")); err != nil {
			setupLog.Error(err, "failed to patch release CRD")
		}
	} else if err != nil {
		setupLog.Error(err, "failed to install release CRD")
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
