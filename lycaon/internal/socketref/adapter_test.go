package socketref

import "testing"

func TestBindingPreservesSavedInterpretation(t *testing.T) {
	if got := (Binding{}).Mode(); got != implicitBindingMode() {
		t.Fatalf("implicit saved binding changed: %s", got)
	}
	for _, enforcement := range []Enforcement{NoRealization, KernelPathname, RetainedObject} {
		if got := (Binding{Enforcement: enforcement}).Mode(); got != enforcement {
			t.Fatalf("saved %s binding changed to %s", enforcement, got)
		}
	}
	if err := (Binding{Enforcement: NoRealization}).ValidateShape(); err != nil {
		t.Fatalf("unavailable intent shape=%v", err)
	}
	if err := (Binding{Enforcement: NoRealization, ID: "invented"}).ValidateShape(); err == nil {
		t.Fatal("unavailable intent carried invented retained authority")
	}
	if _, err := (Binding{Enforcement: NoRealization}).Acquire(); err == nil {
		t.Fatal("unavailable realization acquired authority")
	}
}
