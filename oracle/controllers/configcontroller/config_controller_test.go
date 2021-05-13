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
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
)

var k8sClient client.Client
var k8sManager ctrl.Manager

func TestConfigController(t *testing.T) {
	testhelpers.RunReconcilerTestSuite(t, &k8sClient, &k8sManager, "Config controller", func() []testhelpers.Reconciler {
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
	// Define utility constants for object names and testing timeouts and intervals.
	const (
		Namespace  = "default"
		ConfigName = "test-config"

		timeout  = time.Second * 15
		interval = time.Millisecond * 250
	)

	config := &v1alpha1.Config{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: Namespace,
			Name:      ConfigName,
		},
	}

	var reconciler ConfigReconciler
	BeforeEach(func() {
		reconciler = ConfigReconciler{
			Client: k8sClient,
			Log:    ctrl.Log,
			Scheme: k8sManager.GetScheme(),
			Images: map[string]string{"config": "config_image"},
		}
	})

	objKey := client.ObjectKey{Namespace: Namespace, Name: ConfigName}
	It("Should success when config exists", func() {
		Expect(k8sClient.Create(context.Background(), config)).Should(Succeed())

		createdConfig := &v1alpha1.Config{}
		Eventually(func() bool {
			err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: Namespace, Name: ConfigName}, createdConfig)
			return err == nil
		}, timeout, interval).Should(BeTrue())

		_, err := reconciler.Reconcile(ctrl.Request{NamespacedName: objKey})
		Expect(err).ToNot(HaveOccurred())

		Expect(k8sClient.Delete(context.Background(), config)).Should(Succeed())
	})

	It("Should success when config doesn't exist", func() {
		_, err := reconciler.Reconcile(ctrl.Request{NamespacedName: objKey})
		Expect(err).ToNot(HaveOccurred())
	})
})
