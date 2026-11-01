// Package localmodels composes the application's local inference runtimes.
package localmodels

import (
	"errors"
	"math"

	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/inference"
)

// Resources keeps decision capacity available across generation and decision restarts.
type Resources struct {
	Devices              *inference.Devices
	GenerationHost       *inference.MemoryBudget
	GenerationDevice     *inference.MemoryBudget
	Decision             *bialy.Resources
	SharedWorkspaceBytes uint64
}

// AllocateResources leaves physical memory for the application and operating system.
func AllocateResources(ram uint64, device inference.DeviceFacts, decision bialy.MemoryPlan) (*Resources, error) {
	if ram < 8<<30 || device.RecommendedBytes == 0 || device.RecommendedBytes > device.TotalBytes || decision.HostBytes == 0 || decision.DeviceBytes == 0 || decision.MaxRows != 2 {
		return nil, errors.New("local models: unsupported resource plan")
	}
	if decision.WorkspaceBytes > 0 && (device.PhysicalDevice.Kind != "apple_silicon" || decision.WorkspaceBytes >= decision.DeviceBytes) {
		return nil, errors.New("local models: unsupported shared workspace plan")
	}
	hostCapacity := ram - max(uint64(2<<30), ram/4)
	deviceCapacity := device.RecommendedBytes
	if device.AvailableBytes != nil {
		deviceCapacity = min(deviceCapacity, *device.AvailableBytes)
	}
	decisionHost := decision.HostBytes
	if device.UnifiedMemory {
		if decisionHost > math.MaxUint64-decision.DeviceBytes {
			return nil, inference.ErrCapacity
		}
		decisionHost += decision.DeviceBytes
	}
	if decisionHost >= hostCapacity || decision.DeviceBytes >= deviceCapacity {
		return nil, inference.ErrCapacity
	}
	devices, _ := inference.NewDevices(256)
	if _, err := devices.Bind(device.PhysicalDevice); err != nil {
		return nil, err
	}
	if device.PhysicalDevice.Kind == "apple_silicon" && !device.UnifiedMemory {
		return nil, errors.New("local models: Apple silicon requires unified memory accounting")
	}
	generationHost, _ := inference.NewMemoryBudget(hostCapacity - decisionHost)
	generationDevice, _ := inference.NewMemoryBudget(deviceCapacity - decision.DeviceBytes)
	decisionHostBudget, _ := inference.NewMemoryBudget(decisionHost)
	decisionDeviceBudget, _ := inference.NewMemoryBudget(decision.DeviceBytes)
	return &Resources{
		Devices: devices, GenerationHost: generationHost, GenerationDevice: generationDevice,
		SharedWorkspaceBytes: decision.WorkspaceBytes,
		Decision: &bialy.Resources{
			Devices: devices, PhysicalDevice: device.PhysicalDevice,
			HostMemory: decisionHostBudget, DeviceMemory: decisionDeviceBudget,
			UnifiedMemory: device.UnifiedMemory, HostBytes: decision.HostBytes,
			DeviceBytes: decision.DeviceBytes, MaxRows: decision.MaxRows, WorkspaceBytes: decision.WorkspaceBytes,
		},
	}, nil
}

// Close refuses new work; runtime processes must already be reaped.
func (r *Resources) Close() {
	if r == nil {
		return
	}
	r.GenerationHost.Close()
	r.GenerationDevice.Close()
	r.Decision.HostMemory.Close()
	r.Decision.DeviceMemory.Close()
	r.Devices.Close()
}
