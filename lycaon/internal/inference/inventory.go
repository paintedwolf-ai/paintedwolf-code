package inference

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const MaxInventoryBytes = 64 << 10

var ErrInventory = errors.New("inference: invalid device inventory")

type Inventory struct {
	Protocol int           `json:"protocol"`
	Backend  string        `json:"backend"`
	Devices  []DeviceFacts `json:"devices"`
}

type DeviceFacts struct {
	PhysicalDevice   PhysicalDevice `json:"physical_device"`
	Name             string         `json:"name"`
	UnifiedMemory    bool           `json:"unified_memory"`
	TotalBytes       uint64         `json:"total_bytes"`
	RecommendedBytes uint64         `json:"recommended_bytes"`
	AvailableBytes   *uint64        `json:"available_bytes"`
}

// DecodeInventory accepts facts from the packaged platform adapter.
func DecodeInventory(raw []byte) (Inventory, error) {
	var inventory Inventory
	if len(raw) == 0 || len(raw) > MaxInventoryBytes {
		return inventory, ErrInventory
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return Inventory{}, fmt.Errorf("%w: %w", ErrInventory, err)
	}
	if decoder.Decode(new(any)) != io.EOF || inventory.Protocol != 1 || inventory.Devices == nil || len(inventory.Devices) > 32 {
		return Inventory{}, ErrInventory
	}
	if inventory.Backend != "mlx" && inventory.Backend != "vulkan" {
		return Inventory{}, ErrInventory
	}
	seen := make(map[string]bool)
	for _, device := range inventory.Devices {
		key, err := device.PhysicalDevice.key()
		if err != nil || seen[key] || device.Name == "" || len(device.Name) > 256 || device.TotalBytes == 0 || device.RecommendedBytes == 0 || device.RecommendedBytes > device.TotalBytes {
			return Inventory{}, ErrInventory
		}
		if device.AvailableBytes != nil && *device.AvailableBytes > device.TotalBytes {
			return Inventory{}, ErrInventory
		}
		if inventory.Backend == "mlx" && (device.PhysicalDevice.Kind != "apple_silicon" || !device.UnifiedMemory) || inventory.Backend == "vulkan" && device.PhysicalDevice.Kind != "pci" {
			return Inventory{}, ErrInventory
		}
		seen[key] = true
	}
	return inventory, nil
}

func (d *DeviceFacts) UnmarshalJSON(raw []byte) error {
	type fields DeviceFacts
	wire := struct {
		*fields
		UnifiedMemory *bool `json:"unified_memory"`
	}{fields: (*fields)(d)}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.UnifiedMemory == nil {
		return ErrInventory
	}
	d.UnifiedMemory = *wire.UnifiedMemory
	return nil
}
