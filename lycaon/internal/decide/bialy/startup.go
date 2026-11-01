package bialy

import (
	"context"
	"errors"
	"math"

	"github.com/lycaon/lycaon/internal/inference"
)

// Resources binds model loading and inference to the host's physical budgets.
type Resources struct {
	Devices        *inference.Devices
	PhysicalDevice inference.PhysicalDevice
	HostMemory     *inference.MemoryBudget
	DeviceMemory   *inference.MemoryBudget
	HostBytes      uint64
	DeviceBytes    uint64
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
	if r.Devices == nil || r.HostMemory == nil || r.DeviceMemory == nil || r.HostBytes == 0 || r.DeviceBytes == 0 {
		return nil, errors.New("decide: incomplete startup resource plan")
	}
	device, err := r.Devices.Bind(r.PhysicalDevice)
	if err != nil {
		return nil, err
	}
	if r.PhysicalDevice.Kind == "apple_silicon" && r.HostMemory != r.DeviceMemory {
		return nil, errors.New("decide: Apple silicon requires shared memory accounting")
	}
	grant := &startupGrant{device: device, generation: processSequence.Add(1)}
	hostBytes, deviceBytes := r.HostBytes, r.DeviceBytes
	if r.HostMemory == r.DeviceMemory {
		if hostBytes > math.MaxUint64-deviceBytes {
			return nil, inference.ErrCapacity
		}
		deviceBytes += hostBytes
		hostBytes = 0
	}
	if hostBytes > 0 {
		reservation, err := r.HostMemory.Reserve(hostBytes)
		if err != nil {
			return nil, err
		}
		grant.reservations = append(grant.reservations, reservation)
	}
	reservation, err := r.DeviceMemory.Reserve(deviceBytes)
	if err != nil {
		grant.release()
		return nil, err
	}
	grant.reservations = append(grant.reservations, reservation)
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
