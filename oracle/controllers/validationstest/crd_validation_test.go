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

package validationstest

import (
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/types"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
)

var (
	k8sClient  client.Client
	k8sManager ctrl.Manager
)

func TestValidations(t *testing.T) {
	testhelpers.CdToRoot(t)
	testhelpers.RunFunctionalTestSuite(t, &k8sClient, &k8sManager,
		[]*runtime.SchemeBuilder{&v1alpha1.SchemeBuilder.SchemeBuilder},
		"Validations test",
		func() []testhelpers.Reconciler {
			return []testhelpers.Reconciler{}
		},
		[]string{}, // Use default CRD locations
	)
}

var _ = Describe("Instance CRD Validation rules", func() {
	instanceMeta := metav1.ObjectMeta{
		Name:      "test-instance",
		Namespace: "default",
	}
	ctx := context.Background()

	Context("Memory percent attribute", func() {
		It("Is validated", func() {
			tests := []struct {
				memPercent int
				valid      bool
			}{
				{-1, false},
				{0, true},
				{42, true},
				{100, true},
				{101, false},
			}

			for _, tc := range tests {
				By(fmt.Sprintf("Creating an Instance with MemoryPercent=%d", tc.memPercent))

				instance := &v1alpha1.Instance{
					ObjectMeta: instanceMeta,
					Spec: v1alpha1.InstanceSpec{
						MemoryPercent: tc.memPercent,
					},
				}

				haveExpectedOutcome := Succeed()
				if !tc.valid {
					haveExpectedOutcome = validationErrorOccurred()
				}

				createRequest := k8sClient.Create(ctx, instance)
				_ = k8sClient.Delete(ctx, instance)

				Expect(createRequest).To(haveExpectedOutcome)
			}
		})
	})

	Context("Database engine attribute", func() {
		It("Is validated", func() {
			tests := []struct {
				dbType string
				valid  bool
			}{
				{"Oracle", true},
				{"MySQL", false},
			}

			for _, tc := range tests {
				By(fmt.Sprintf("Creating an Instance with Type=%s", tc.dbType))

				instance := &v1alpha1.Instance{
					ObjectMeta: instanceMeta,
					Spec: v1alpha1.InstanceSpec{
						InstanceSpec: commonv1alpha1.InstanceSpec{
							Type: tc.dbType,
						},
					},
				}

				haveExpectedOutcome := Succeed()
				if !tc.valid {
					haveExpectedOutcome = validationErrorOccurred()
				}

				createRequest := k8sClient.Create(ctx, instance)
				_ = k8sClient.Delete(ctx, instance)

				Expect(createRequest).To(haveExpectedOutcome)
			}
		})
	})

	Context("Restore.DOP attribute", func() {
		It("Is validated", func() {
			tests := []struct {
				dop   int32
				valid bool
			}{
				{-1, false},
				{0, true},
				{1, true},
				{100, true},
				{101, false},
			}

			for _, tc := range tests {
				By(fmt.Sprintf("Creating an Instance with Restore.DOP=%d", tc.dop))

				instance := &v1alpha1.Instance{
					ObjectMeta: instanceMeta,
					Spec: v1alpha1.InstanceSpec{
						Restore: &v1alpha1.RestoreSpec{
							Dop:         tc.dop,
							RequestTime: metav1.Now(),
						},
					},
				}

				haveExpectedOutcome := Succeed()
				if !tc.valid {
					haveExpectedOutcome = validationErrorOccurred()
				}

				createRequest := k8sClient.Create(ctx, instance)
				_ = k8sClient.Delete(ctx, instance)

				Expect(createRequest).To(haveExpectedOutcome)
			}
		})
	})

	Context("Restore.TimeLimitMinutes attribute", func() {
		It("Is validated", func() {
			tests := []struct {
				timeLimitMinutes int32
				valid            bool
			}{
				{-1, false},
				{0, true},
				{1, true},
				{101, true},
			}

			for _, tc := range tests {
				By(fmt.Sprintf("Creating an Instance with Restore.TimeLimitMinutes=%d", tc.timeLimitMinutes))

				instance := &v1alpha1.Instance{
					ObjectMeta: instanceMeta,
					Spec: v1alpha1.InstanceSpec{
						Restore: &v1alpha1.RestoreSpec{
							TimeLimitMinutes: tc.timeLimitMinutes,
							RequestTime:      metav1.Now(),
						},
					},
				}

				haveExpectedOutcome := Succeed()
				if !tc.valid {
					haveExpectedOutcome = validationErrorOccurred()
				}

				createRequest := k8sClient.Create(ctx, instance)
				_ = k8sClient.Delete(ctx, instance)

				Expect(createRequest).To(haveExpectedOutcome)
			}
		})
	})

	Context("Disk.Name attribute", func() {
		It("Is validated", func() {
			tests := []struct {
				disks []commonv1alpha1.DiskSpec
				valid bool
			}{
				{
					disks: []commonv1alpha1.DiskSpec{
						{Name: "DataDisk"},
						{Name: "LogDisk"},
					},
					valid: true,
				},
				{
					disks: []commonv1alpha1.DiskSpec{
						{Name: "DataDisk"},
					},
					valid: true,
				},
				{
					disks: []commonv1alpha1.DiskSpec{
						{Name: "FrisbeeDisk"},
					},
					valid: false,
				},
				{
					disks: []commonv1alpha1.DiskSpec{
						{Name: "SystemDisk"},
						{Name: "DataDisk"},
					},
					valid: false,
				},
			}

			for _, tc := range tests {
				By(fmt.Sprintf("Creating an Instance with Disks=%v", tc.disks))

				instance := &v1alpha1.Instance{
					ObjectMeta: instanceMeta,
					Spec: v1alpha1.InstanceSpec{
						InstanceSpec: commonv1alpha1.InstanceSpec{
							Disks: tc.disks,
						},
					},
				}

				haveExpectedOutcome := Succeed()
				if !tc.valid {
					haveExpectedOutcome = validationErrorOccurred()
				}

				createRequest := k8sClient.Create(ctx, instance)
				_ = k8sClient.Delete(ctx, instance)

				Expect(createRequest).To(haveExpectedOutcome)
			}
		})
	})
})

var _ = Describe("Database CRD Validation rules", func() {
	instanceMeta := metav1.ObjectMeta{
		Name:      "test-database",
		Namespace: "default",
	}
	ctx := context.Background()
	Context("User name attribute", func() {
		It("Is validated", func() {
			tests := []struct {
				user  string
				valid bool
			}{
				{user: "scott", valid: true},
				{user: "superuser", valid: true},
			}

			for _, tc := range tests {
				By(fmt.Sprintf("Creating Database User with name =%v", tc.user))

				database := &v1alpha1.Database{
					ObjectMeta: instanceMeta,
					Spec: v1alpha1.DatabaseSpec{
						DatabaseSpec: commonv1alpha1.DatabaseSpec{
							Name:     "pdb1",
							Instance: "mydb",
						},
						AdminPassword: "google",

						Users: []v1alpha1.UserSpec{
							{
								UserSpec: commonv1alpha1.UserSpec{
									Name: tc.user,
									CredentialSpec: commonv1alpha1.CredentialSpec{
										Password: "123456",
									},
								},
								Privileges: []v1alpha1.PrivilegeSpec{},
							},
						},
					},
				}
				haveExpectedOutcome := Succeed()
				if !tc.valid {
					haveExpectedOutcome = validationErrorOccurred()
				}

				createRequest := k8sClient.Create(ctx, database)
				_ = k8sClient.Delete(ctx, database)

				Expect(createRequest).To(haveExpectedOutcome)
			}
		})
	})
})

// validationErrorMatcher is a matcher for CRD validation errors.
type validationErrorMatcher struct{}

func validationErrorOccurred() types.GomegaMatcher {
	return &validationErrorMatcher{}
}

func (matcher *validationErrorMatcher) Match(actual interface{}) (bool, error) {
	if actual == nil {
		return false, fmt.Errorf("expected an error, got nil")
	}

	err, ok := actual.(error)
	if !ok {
		return false, fmt.Errorf("%s is not an error", format.Object(actual, 1))
	}

	if !errors.IsInvalid(err) {
		return false, fmt.Errorf("%s is not an error indicating an invalid resource", format.Object(err, 1))
	}

	return true, nil
}

func (matcher *validationErrorMatcher) FailureMessage(actual interface{}) (message string) {
	return format.Message(actual, "to be a validation error")
}

func (matcher *validationErrorMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	return format.Message(actual, "not to be a validation error")
}
