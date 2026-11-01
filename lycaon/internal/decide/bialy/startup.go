package bialy

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/inference"
)

// Resources binds model loading and inference to the host's physical budgets.
type Resources struct {
	Devices        *inference.Devices
	PhysicalDevice inference.PhysicalDevice
	HostMemory     *inference.MemoryBudget
	DeviceMemory   *inference.MemoryBudget
	UnifiedMemory  bool
	HostBytes      uint64
	DeviceBytes    uint64
	MaxRows        int
}

type startupGrant struct {
	device       *inference.Device
	permit       *inference.Permit
	reservations []*inference.Reservation
	generation   uint64
}

func (r *Resources) acquire(ctx context.Context) (*startupGrant, error) {
	if r == nil {
		return nil, nil
	}
	if r.MaxRows < 1 || r.MaxRows > 32 {
		return nil, errors.New("decide: batch rows must be between 1 and 32")
	}
	if r.Devices == nil || r.HostMemory == nil || r.DeviceMemory == nil || r.HostBytes == 0 || r.DeviceBytes == 0 {
		return nil, errors.New("decide: incomplete startup resource plan")
	}
	device, err := r.Devices.Bind(r.PhysicalDevice)
	if err != nil {
		return nil, err
	}
	if r.PhysicalDevice.Kind == "apple_silicon" && !r.UnifiedMemory {
		return nil, errors.New("decide: Apple silicon requires shared memory accounting")
	}
	grant := &startupGrant{device: device, generation: processSequence.Add(1)}
	grant.reservations, err = inference.ReserveMemory(r.HostMemory, r.DeviceMemory, r.HostBytes, r.DeviceBytes, r.UnifiedMemory)
	if err != nil {
		grant.release()
		return nil, err
	}
	grant.permit, err = device.Acquire(ctx, inference.Work{Owner: "bialy", Generation: grant.generation, Priority: inference.Decision})
	if err != nil {
		grant.release()
		return nil, err
	}
	return grant, nil
}

func (g *startupGrant) release() {
	if g == nil {
		return
	}
	if g.permit != nil {
		g.permit.Complete()
	}
	for _, reservation := range g.reservations {
		reservation.Release()
	}
}

func (r *Resources) deviceArgument() string {
	switch r.PhysicalDevice.Kind {
	case "apple_silicon":
		return "mlx"
	case "pci":
		return "vulkan-pci:" + r.PhysicalDevice.Address
	case "cpu":
		return "cpu"
	default:
		return ""
	}
}
