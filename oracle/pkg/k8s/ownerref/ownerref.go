package ownerref

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Add adds owner to the object.
func Add(obj, owner client.Object, controller bool, block bool) {
	if obj.GetNamespace() != owner.GetNamespace() {
		panic("Object owner must be in the same namespace.")
	}
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == owner.GetUID() {
			return
		}
	}
	obj.SetOwnerReferences(append(obj.GetOwnerReferences(), New(owner, controller, block)))
}

// New constructs a OwnerReference to an Object.
func New(owner client.Object, controller bool, block bool) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         owner.GetObjectKind().GroupVersionKind().GroupVersion().String(),
		Kind:               owner.GetObjectKind().GroupVersionKind().Kind,
		Name:               owner.GetName(),
		UID:                owner.GetUID(),
		Controller:         pointer.BoolPtr(controller),
		BlockOwnerDeletion: pointer.BoolPtr(block),
	}
}
