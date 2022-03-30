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

package configcontroller

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
)

var k8sClient client.Client
var k8sManager ctrl.Manager

// Define utility constants for object names and testing timeouts and intervals.
const (
	Namespace  = "default"
	ConfigName = "test-config"

	timeout  = time.Second * 15
	interval = time.Millisecond * 250

	instanceCount = 3
)

func TestConfigController(t *testing.T) {
	testhelpers.CdToRoot(t)
	testhelpers.RunFunctionalTestSuite(t, &k8sClient, &k8sManager,
		[]*runtime.SchemeBuilder{&v1alpha1.SchemeBuilder.SchemeBuilder},
		"Config controller",
		func() []testhelpers.Reconciler {
			return []testhelpers.Reconciler{
				&ConfigReconciler{
					Client: k8sManager.GetClient(),
					Log:    ctrl.Log.WithName("controllers").WithName("Config"),
					Scheme: k8sManager.GetScheme(),
					Images: map[string]string{"config": "config_image"},
				},
			}
		})
}

var _ = Describe("Config controller", func() {
	var patchedDeploymentCount uint32
	config := &v1alpha1.Config{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: Namespace,
			Name:      ConfigName,
		},
	}
	configObjectKey := client.ObjectKey{Namespace: Namespace, Name: ConfigName}

	BeforeEach(func() {
		Patch = func(reconciler *ConfigReconciler, ctx context.Context, object client.Object, patch client.Patch, option ...client.PatchOption) error {
			atomic.AddUint32(&patchedDeploymentCount, 1)
			return nil
		}
	})

	It("Should succeed when config exists", func() {
		createInstances()
		Expect(k8sClient.Create(context.Background(), config)).Should(Succeed())

		createdConfig := &v1alpha1.Config{}
		Eventually(func() bool {
			err := k8sClient.Get(context.Background(), configObjectKey, createdConfig)
			return err == nil
		}, timeout, interval).Should(BeTrue())

		Expect(k8sClient.Delete(context.Background(), config)).Should(Succeed())
	})

	It("Should succeed when config doesn't exist", func() {
		reconciler := ConfigReconciler{
			Client: k8sClient,
			Log:    ctrl.Log,
			Scheme: k8sManager.GetScheme(),
			Images: map[string]string{"config": "config_image"},
		}
		// Force the reconciler to run since there's no state change in this test spec that would cause it to run.
		_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: configObjectKey})
		Expect(err).ToNot(HaveOccurred())
	})
})

func createInstances() {
	for i := 0; i < instanceCount; i++ {
		instance := &v1alpha1.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("myinstance-%d", i),
				Namespace: Namespace,
			},
			Spec: v1alpha1.InstanceSpec{
				CDBName: fmt.Sprintf("MYDB-%d", i),
			},
		}
		Expect(k8sClient.Create(context.Background(), instance)).Should(Succeed())
	}
}
