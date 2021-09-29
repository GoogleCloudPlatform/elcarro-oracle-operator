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

package instance_lock_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/instancecontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	k8sClient  client.Client
	k8sManager ctrl.Manager
	ctx        context.Context
)

const (
	Namespace     = "default"
	InstanceName  = "myinstance"
	retryTimeout  = time.Millisecond * 10000
	retryInterval = time.Millisecond * 100
)

func TestInstanceLockBasic(t *testing.T) {
	ctx = context.Background()
	testhelpers.RunReconcilerTestSuite(t, &k8sClient, &k8sManager, "Instance controller", func() []testhelpers.Reconciler {
		return []testhelpers.Reconciler{}
	})
}

// Simple worker performing read-write operations with 'testmap' in a loop
// acquiring/releasing instance lock twice
func updateMapWorker(id int, testmap map[string]int, waitgroup *sync.WaitGroup) {
	defer GinkgoRecover()
	logf.FromContext(nil).Info(fmt.Sprintf("Worker %d started", id))

	objKey := client.ObjectKey{Namespace: Namespace, Name: InstanceName}
	ownerName := fmt.Sprintf("owner_%d", id)
	inst := &v1alpha1.Instance{}

	for i := 0; i < 50; i++ {
		// Acquire the lock twice
		// First call should eventually succeed
		Eventually(
			func() error {
				if err := k8sClient.Get(ctx, objKey, inst); err != nil {
					return err
				}
				return instancecontroller.AcquireInstanceMaintenanceLock(ctx, k8sClient, inst, ownerName)
			}, retryTimeout, retryInterval).Should(Succeed())
		// Second call should succeed or return a k8s-retry
		Expect(instancecontroller.AcquireInstanceMaintenanceLock(ctx, k8sClient, inst, ownerName)).
			Should(Or(Succeed(), MatchError("failed to update the instance status")))
		// Perform simple r/w operations with the counter to ensure there are no conflicts.
		testmap["COUNTER"] += i
		testmap["COUNTER"] -= i

		// Release the lock twice
		Eventually(
			func() error {
				if err := k8sClient.Get(ctx, objKey, inst); err != nil {
					return err
				}
				return instancecontroller.ReleaseInstanceMaintenanceLock(ctx, k8sClient, inst, ownerName)
			}, retryTimeout, retryInterval).Should(Succeed())
		Eventually(
			func() error {
				if err := k8sClient.Get(ctx, objKey, inst); err != nil {
					return err
				}
				return instancecontroller.ReleaseInstanceMaintenanceLock(ctx, k8sClient, inst, ownerName)
			}, retryTimeout, retryInterval).Should(Succeed())

	}
	waitgroup.Done()
}

var _ = Describe("Instance Lock Test", func() {
	BeforeEach(func() {
		instance := &v1alpha1.Instance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      InstanceName,
				Namespace: Namespace,
			},
			Spec: v1alpha1.InstanceSpec{
				CDBName: "GCLOUD",
			},
		}
		objKey := client.ObjectKey{Namespace: Namespace, Name: instance.Name}
		createdInstance := &v1alpha1.Instance{}
		testhelpers.K8sCreateAndGet(k8sClient, ctx, objKey, instance, createdInstance)
	})
	AfterEach(func() {
		objKey := client.ObjectKey{Namespace: Namespace, Name: InstanceName}
		createdInstance := &v1alpha1.Instance{}
		testhelpers.K8sGetWithRetry(k8sClient, ctx, objKey, createdInstance)
		testhelpers.K8sDeleteWithRetry(k8sClient, ctx, objKey, createdInstance)
	})

	// Check that basic operations work
	It("Should work", func() {
		objKey := client.ObjectKey{Namespace: Namespace, Name: InstanceName}
		inst := &v1alpha1.Instance{}
		// Lock A should work (may need k8s retries)
		Eventually(
			func() error {
				if err := k8sClient.Get(ctx, objKey, inst); err != nil {
					return err
				}
				return instancecontroller.AcquireInstanceMaintenanceLock(ctx, k8sClient, inst, "A")
			}, retryTimeout, retryInterval).Should(Succeed())

		// Second call should succeed or return a k8s-retry
		Expect(instancecontroller.AcquireInstanceMaintenanceLock(ctx, k8sClient, inst, "A")).
			Should(Or(Succeed(), MatchError("failed to update the instance status")))

		// Lock B should fail
		Expect(instancecontroller.AcquireInstanceMaintenanceLock(ctx, k8sClient, inst, "B")).ShouldNot(Succeed())

		// Unlock A should work (may need k8s retries)
		Eventually(
			func() error {
				if err := k8sClient.Get(ctx, objKey, inst); err != nil {
					return err
				}
				return instancecontroller.ReleaseInstanceMaintenanceLock(ctx, k8sClient, inst, "A")
			}, retryTimeout, retryInterval).Should(Succeed())

		// Lock B should work (may need k8s retries)
		Eventually(
			func() error {
				if err := k8sClient.Get(ctx, objKey, inst); err != nil {
					return err
				}
				return instancecontroller.AcquireInstanceMaintenanceLock(ctx, k8sClient, inst, "B")
			}, retryTimeout, retryInterval).Should(Succeed())

		// Unlock B should work (may need k8s retries)
		Eventually(
			func() error {
				if err := k8sClient.Get(ctx, objKey, inst); err != nil {
					return err
				}
				return instancecontroller.ReleaseInstanceMaintenanceLock(ctx, k8sClient, inst, "B")
			}, retryTimeout, retryInterval).Should(Succeed())
	})

	// Spawn multiple worker threads and make them read and
	// write a map object in parallel using instance lock as protection mechanism.
	// Test idempotency by locking and unlocking
	// the instance twice every time.
	// The workers should not raise a concurrent access panic, and the counter
	// is expected to remain 0
	It("Parallel access and idempotent locking", func() {
		testMap := map[string]int{"COUNTER": 0}
		var wg sync.WaitGroup
		for i := 1; i <= 5; i++ {
			wg.Add(1)
			go updateMapWorker(i, testMap, &wg)
		}
		wg.Wait()
		Expect(testMap["COUNTER"]).To(Equal(0))
	})
})
