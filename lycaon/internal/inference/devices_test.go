package inference

import (
	"errors"
	"testing"
)

func TestPhysicalDevicesShareAdmissionAcrossAdapters(t *testing.T) {
	devices, err := NewDevices(4)
	if err != nil {
		t.Fatalf("new device registry: %v", err)
	}
	defer devices.Close()
	vulkan, err := devices.Bind(PhysicalDevice{Kind: "pci", Address: "0000:0A:00.0"})
	if err != nil {
		t.Fatalf("bind Vulkan: %v", err)
	}
	other, err := devices.Bind(PhysicalDevice{Kind: "pci", Address: "00000000:0a:00.0"})
	if err != nil || other != vulkan {
		t.Fatalf("adapters did not share admission: %v", err)
	}
	cpu, err := devices.Bind(PhysicalDevice{Kind: "cpu"})
	if err != nil || cpu == vulkan {
		t.Fatalf("CPU shared a discrete compute queue: %v", err)
	}
	active := submit(t, vulkan, "generation", Foreground)
	decision := submit(t, other, "decision", Decision)
	if got := other.Snapshot(); got.Queued != 1 || got.Active.Owner != "generation" {
		t.Fatalf("physical GPU admitted overlapping work: %+v", got)
	}
	devices.Close()
	<-decision.ready
	if !errors.Is(decision.err, ErrClosed) || vulkan.Snapshot().Active == nil {
		t.Fatalf("registry close lost active work: %v", decision.err)
	}
	active.permit.Complete()
	if _, err := devices.Bind(PhysicalDevice{Kind: "cpu"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed registry: %v", err)
	}
}

func TestPhysicalDeviceIdentityRequiresStructuredFacts(t *testing.T) {
	for _, identity := range []PhysicalDevice{
		{}, {Kind: "unknown"}, {Kind: "mlx"}, {Kind: "vulkan:0"},
		{Kind: "cpu", Address: "0"}, {Kind: "apple_silicon", Address: "M4"},
		{Kind: "pci"}, {Kind: "pci", Address: "0000:03:20.0"},
		{Kind: "pci", Address: "0000:03:00.8"}, {Kind: "pci", Address: " 0000:03:00.0"},
	} {
		if _, err := identity.key(); !errors.Is(err, ErrIdentity) {
			t.Fatalf("accepted invalid identity %+v: %v", identity, err)
		}
	}
	for _, identity := range []PhysicalDevice{
		{Kind: "cpu"}, {Kind: "apple_silicon"}, {Kind: "pci", Address: "ffffffff:ff:1f.7"},
	} {
		if _, err := identity.key(); err != nil {
			t.Fatalf("valid identity %+v: %v", identity, err)
		}
	}
	if _, err := NewDevices(0); err == nil {
		t.Fatal("accepted zero queue capacity")
	}
}
