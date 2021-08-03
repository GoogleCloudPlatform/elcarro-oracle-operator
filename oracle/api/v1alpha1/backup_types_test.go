package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func TestBackupSpecSectionSize(t *testing.T) {
	quantity, err := resource.ParseQuantity("100M")
	if err != nil {
		t.Error(err)
	}
	b := Backup{}
	b.Spec = BackupSpec{
		SectionSize: quantity,
	}

	expected := int32(100_000_000)
	actual := b.SectionSize()
	if actual != expected {
		t.Error(actual, " != ", expected)
	}
}

func TestBackupSpecSectionSizeFractions(t *testing.T) {
	quantity, err := resource.ParseQuantity("0.5G")
	if err != nil {
		t.Error(err)
	}
	b := Backup{}
	b.Spec = BackupSpec{
		SectionSize: quantity,
	}

	expected := int32(500_000_000)
	actual := b.SectionSize()
	if actual != expected {
		t.Error(actual, " != ", expected)
	}
}
