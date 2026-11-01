package inference

import "fmt"

// SelectDevice preserves an explicit physical identity; automatic selection favors discrete capacity.
func SelectDevice(inventory Inventory, requested *PhysicalDevice) (DeviceFacts, error) {
	requestedKey := ""
	if requested != nil {
		var err error
		requestedKey, err = requested.key()
		if err != nil {
			return DeviceFacts{}, err
		}
	}
	var selected *DeviceFacts
	selectedKey := ""
	for i := range inventory.Devices {
		candidate := &inventory.Devices[i]
		key, err := candidate.PhysicalDevice.key()
		if err != nil {
			return DeviceFacts{}, err
		}
		if requested != nil {
			if key == requestedKey {
				return *candidate, nil
			}
			continue
		}
		if selected == nil || preferredDevice(*candidate, *selected, key, selectedKey) {
			selected, selectedKey = candidate, key
		}
	}
	if selected == nil {
		return DeviceFacts{}, fmt.Errorf("%w: no matching generation accelerator", ErrIdentity)
	}
	return *selected, nil
}

func preferredDevice(candidate, current DeviceFacts, candidateKey, currentKey string) bool {
	if candidate.UnifiedMemory != current.UnifiedMemory {
		return !candidate.UnifiedMemory
	}
	if candidate.RecommendedBytes != current.RecommendedBytes {
		return candidate.RecommendedBytes > current.RecommendedBytes
	}
	return candidateKey < currentKey
}
