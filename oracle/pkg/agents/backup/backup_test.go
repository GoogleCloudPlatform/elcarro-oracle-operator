package backup

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestSectionSize_Zero(t *testing.T) {
	zero := resource.NewQuantity(0, resource.DecimalSI)

	expected := ""
	got := sectionSize(*zero)

	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("Diff: \n%v\n", diff)
	}
}

func TestSectionSize_Bytes(t *testing.T) {
	size := resource.NewQuantity(12, resource.DecimalSI)

	expected := "section size 12"
	got := sectionSize(*size)

	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("Diff: \n%v\n", diff)
	}
}

func TestSectionSize_KBytes(t *testing.T) {
	size := resource.NewQuantity(23_456, resource.DecimalSI)

	expected := "section size 23K"
	got := sectionSize(*size)

	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("Diff: \n%v\n", diff)
	}
}

func TestSectionSize_MBytes(t *testing.T) {
	size := resource.NewQuantity(34_567_890, resource.DecimalSI)

	expected := "section size 34M"
	got := sectionSize(*size)

	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("Diff: \n%v\n", diff)
	}
}

func TestSectionSize_GBytes(t *testing.T) {
	size := resource.NewQuantity(45_678_901_234, resource.DecimalSI)

	expected := "section size 45G"
	got := sectionSize(*size)

	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("Diff: \n%v\n", diff)
	}
}
