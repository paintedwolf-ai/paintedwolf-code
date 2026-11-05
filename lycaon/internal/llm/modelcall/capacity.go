package modelcall

import (
	"strconv"
	"sync/atomic"
)

// CapacityGroup identifies a shared serialized runtime for its lifetime.
type CapacityGroup struct {
	id uint64
}

var nextCapacityGroup atomic.Uint64

func NewCapacityGroup() *CapacityGroup {
	return &CapacityGroup{id: nextCapacityGroup.Add(1)}
}

// ProviderCapacity returns the runtime identity before provider decoration.
func ProviderCapacity(provider Provider) *CapacityGroup {
	if shared, ok := provider.(interface{ CapacityGroup() *CapacityGroup }); ok {
		return shared.CapacityGroup()
	}
	return nil
}

// CapacityKey groups aliases without conflating independent provider instances.
func CapacityKey(provider Provider) string {
	if group := ProviderCapacity(provider); group != nil && group.id != 0 {
		return "runtime:" + strconv.FormatUint(group.id, 10)
	}
	return "provider:" + provider.ID()
}
