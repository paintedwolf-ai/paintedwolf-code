package inference

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestInventoryPreservesPhysicalMemoryDomains(t *testing.T) {
	raw := []byte(`{"protocol":1,"backend":"vulkan","devices":[{"physical_device":{"kind":"pci","address":"0000:03:00.0"},"name":"Discrete GPU","unified_memory":false,"total_bytes":8589934592,"recommended_bytes":8589934592,"available_bytes":0},{"physical_device":{"kind":"pci","address":"0000:18:00.0"},"name":"Integrated GPU","unified_memory":true,"total_bytes":17179869184,"recommended_bytes":17179869184,"available_bytes":null}]}`)
	inventory, err := DecodeInventory(raw)
	if err != nil {
		t.Fatalf("decode inventory: %v", err)
	}
	if inventory.Devices[0].UnifiedMemory || !inventory.Devices[1].UnifiedMemory || inventory.Devices[0].AvailableBytes == nil || *inventory.Devices[0].AvailableBytes != 0 || inventory.Devices[1].AvailableBytes != nil {
		t.Fatal("lost memory-domain or availability facts")
	}
}

func TestInventoryRefusesAmbiguousOrInconsistentFacts(t *testing.T) {
	base := Inventory{Protocol: 1, Backend: "mlx", Devices: []DeviceFacts{{PhysicalDevice: PhysicalDevice{Kind: "apple_silicon"}, Name: "Apple GPU", UnifiedMemory: true, TotalBytes: 8 << 30, RecommendedBytes: 6 << 30}}}
	for name, mutate := range map[string]func(*Inventory){
		"version":       func(v *Inventory) { v.Protocol = 2 },
		"backend":       func(v *Inventory) { v.Backend = "cpu" },
		"identity":      func(v *Inventory) { v.Devices[0].PhysicalDevice.Kind = "cpu" },
		"memory domain": func(v *Inventory) { v.Devices[0].UnifiedMemory = false },
		"memory limit":  func(v *Inventory) { v.Devices[0].RecommendedBytes = 9 << 30 },
		"availability":  func(v *Inventory) { n := uint64(9 << 30); v.Devices[0].AvailableBytes = &n },
		"duplicate":     func(v *Inventory) { v.Devices = append(v.Devices, v.Devices[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			value.Devices = append([]DeviceFacts(nil), base.Devices...)
			mutate(&value)
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("encode fixture: %v", err)
			}
			if _, err := DecodeInventory(raw); !errors.Is(err, ErrInventory) {
				t.Fatalf("accepted inconsistent inventory: %v", err)
			}
		})
	}
	valid, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("encode valid inventory: %v", err)
	}
	missingDomain := strings.Replace(string(valid), `"unified_memory":true,`, "", 1)
	for _, raw := range [][]byte{[]byte(missingDomain), []byte(`{"protocol":1,"backend":"mlx"}`), nil, make([]byte, MaxInventoryBytes+1), []byte(`{"protocol":1,"backend":"mlx","devices":[],"extra":true}`), []byte(`{"protocol":1,"backend":"mlx","devices":[]} {}`)} {
		if _, err := DecodeInventory(raw); !errors.Is(err, ErrInventory) {
			t.Fatalf("accepted malformed inventory: %v", err)
		}
	}
}
