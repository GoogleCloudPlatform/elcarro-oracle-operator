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

package releasetest

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	// Enable GCP auth for k8s client
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
)

// Made global to be accessible by AfterSuite
var k8sEnv = testhelpers.K8sOperatorEnvironment{}

// In case of Ctrl-C clean up the last valid k8sEnv.
var _ = AfterSuite(func() {
	k8sEnv.Close()
})

var _ = Describe("New deployment", func() {
	var namespace string

	BeforeEach(func() {
		defer GinkgoRecover()
		namespace = testhelpers.RandName("release-crd-test")
		k8sEnv.Init(namespace, namespace)
	})

	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			testhelpers.PrintLogs(k8sEnv.CPNamespace, k8sEnv.DPNamespace, k8sEnv.Env, []string{}, []string{})
			testhelpers.PrintClusterObjects()
		}
		k8sEnv.Close()
	})

	It("Should create s release object", func() {
		Eventually(func() string {
			rKey := client.ObjectKey{Namespace: namespace, Name: "release"}
			release := &v1alpha1.Release{}
			if err := k8sEnv.K8sClient.Get(k8sEnv.Ctx, rKey, release); err != nil {
				return ""
			}
			return release.Spec.Version
		}, 1*time.Minute, 5*time.Second).Should(Not(BeEmpty()))
	})
})

func TestRelease(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecsWithDefaultAndCustomReporters(t,
		t.Name(),
		[]Reporter{printer.NewlineReporter{}})
}
