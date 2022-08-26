package ownerref

import (
	"testing"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
)

func TestAdd(t *testing.T) {
	testCases := []struct {
		controlled     *corev1.Pod
		owner          *appsv1.Deployment
		expected       *corev1.Pod
		inputNamespace string
		inputName      string
	}{
		// Add new ownerreference.
		{
			controlled: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test1-p",
					Namespace: "db",
				},
			},
			owner: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test1",
					Namespace: "db",
					UID:       "1",
				},
			},
			expected: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test1-p",
					Namespace: "db",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "",
							Kind:               "",
							Name:               "test1",
							UID:                "1",
							Controller:         pointer.BoolPtr(true),
							BlockOwnerDeletion: pointer.BoolPtr(true),
						},
					},
				},
			},
			inputNamespace: "db",
			inputName:      "test1-p",
		},
		// Add existing ownerreference.
		{
			controlled: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test1-p",
					Namespace: "db",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "",
							Kind:               "",
							Name:               "test1",
							UID:                "1",
							Controller:         pointer.BoolPtr(true),
							BlockOwnerDeletion: pointer.BoolPtr(true),
						},
					},
				},
			},
			owner: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test1",
					Namespace: "db",
					UID:       "1",
				},
			},
			expected: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test1-p",
					Namespace: "db",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "",
							Kind:               "",
							Name:               "test1",
							UID:                "1",
							Controller:         pointer.BoolPtr(true),
							BlockOwnerDeletion: pointer.BoolPtr(true),
						},
					},
				},
			},
			inputNamespace: "db",
			inputName:      "test1-p",
		},
	}

	g := NewWithT(t)

	for _, test := range testCases {
		Add(test.controlled, test.owner, true, true)
		g.Expect(test.controlled).To(Equal(test.expected))
	}
}
