package inference

import (
	"errors"
	"testing"
)

func TestSelectDeviceUsesStablePhysicalFacts(t *testing.T) {
	devices := []DeviceFacts{
		{PhysicalDevice: PhysicalDevice{Kind: "pci", Address: "0000:18:00.0"}, UnifiedMemory: true, RecommendedBytes: 32 << 30},
		{PhysicalDevice: PhysicalDevice{Kind: "pci", Address: "0000:03:00.0"}, RecommendedBytes: 8 << 30},
		{PhysicalDevice: PhysicalDevice{Kind: "pci", Address: "0000:04:00.0"}, RecommendedBytes: 16 << 30},
	}
	for _, order := range [][]DeviceFacts{devices, {devices[2], devices[0], devices[1]}} {
		chosen, err := SelectDevice(Inventory{Devices: order}, nil)
		if err != nil || chosen.PhysicalDevice != devices[2].PhysicalDevice {
			t.Fatalf("automatic selection: %+v, %v", chosen, err)
		}
	}
	chosen, err := SelectDevice(Inventory{Devices: devices}, &devices[0].PhysicalDevice)
	if err != nil || chosen.PhysicalDevice != devices[0].PhysicalDevice {
		t.Fatalf("explicit selection: %+v, %v", chosen, err)
	}
	devices[1].RecommendedBytes = devices[2].RecommendedBytes
	chosen, err = SelectDevice(Inventory{Devices: devices}, nil)
	if err != nil || chosen.PhysicalDevice != devices[1].PhysicalDevice {
		t.Fatalf("equal-capacity selection: %+v, %v", chosen, err)
	}
	for _, requested := range []*PhysicalDevice{nil, {Kind: "pci", Address: "0000:05:00.0"}, {Kind: "ordinal", Address: "0"}} {
		if _, err := SelectDevice(Inventory{}, requested); !errors.Is(err, ErrIdentity) {
			t.Fatalf("unavailable selection: %v", err)
		}
	}
}
