package socketref

import "testing"

func TestBindingDefaultUsesActualAdapterRealization(t *testing.T) {
	if got, want := (Binding{}).Mode(), Platform().Command; got != want {
		t.Fatalf("default binding=%s, adapter=%s", got, want)
	}
	for _, command := range []Enforcement{NoRealization, KernelPathname, RetainedObject} {
		adapter := Adapter{Command: command}
		if got := adapter.BindingMode(Binding{}); got != command {
			t.Fatalf("adapter %s defaulted to %s", command, got)
		}
		explicit := Binding{Enforcement: RetainedObject, ID: "saved-intent"}
		if got := adapter.BindingMode(explicit); got != RetainedObject {
			t.Fatalf("adapter rewrote explicit intent to %s", got)
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
