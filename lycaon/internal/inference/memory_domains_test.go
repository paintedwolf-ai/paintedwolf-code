package inference

import (
	"errors"
	"math"
	"testing"
)

func TestUnifiedMemoryChargesRAMAndDeviceWorkingSet(t *testing.T) {
	host, _ := NewMemoryBudget(15)
	device, _ := NewMemoryBudget(10)
	reservations, err := ReserveMemory(host, device, 5, 10, true)
	if err != nil || len(reservations) != 2 || host.Snapshot().ReservedBytes != 15 || device.Snapshot().ReservedBytes != 10 {
		t.Fatalf("unified reservation = %v, %v, host=%+v device=%+v", reservations, err, host.Snapshot(), device.Snapshot())
	}
	for _, reservation := range reservations {
		reservation.Release()
		reservation.Release()
	}
	if host.Snapshot().ReservedBytes != 0 || device.Snapshot().ReservedBytes != 0 {
		t.Fatal("released allocation retained a memory charge")
	}
	if _, err := ReserveMemory(host, device, 5, 11, true); !errors.Is(err, ErrCapacity) {
		t.Fatalf("physical RAM overcommit = %v", err)
	}
	if _, err := ReserveMemory(host, device, 1, 11, true); !errors.Is(err, ErrCapacity) {
		t.Fatalf("device working-set overcommit = %v", err)
	}
	if host.Snapshot().ReservedBytes != 0 || device.Snapshot().ReservedBytes != 0 {
		t.Fatal("failed allocation retained a partial charge")
	}
}

func TestDiscreteMemoryDoesNotChargeVRAMToHostRAM(t *testing.T) {
	host, _ := NewMemoryBudget(5)
	device, _ := NewMemoryBudget(10)
	reservations, err := ReserveMemory(host, device, 5, 10, false)
	if err != nil || host.Snapshot().ReservedBytes != 5 || device.Snapshot().ReservedBytes != 10 {
		t.Fatalf("discrete reservation = %v, host=%+v device=%+v", err, host.Snapshot(), device.Snapshot())
	}
	for _, reservation := range reservations {
		reservation.Release()
	}
	for _, invalid := range []struct {
		host, device           *MemoryBudget
		hostBytes, deviceBytes uint64
	}{
		{nil, device, 1, 1}, {host, nil, 1, 1}, {host, host, 1, 1},
		{host, device, 0, 0}, {host, device, math.MaxUint64, 1},
	} {
		if _, err := ReserveMemory(invalid.host, invalid.device, invalid.hostBytes, invalid.deviceBytes, true); !errors.Is(err, ErrCapacity) {
			t.Fatalf("invalid admission = %v", err)
		}
	}
	device.Close()
	if _, err := ReserveMemory(host, device, 1, 1, false); !errors.Is(err, ErrClosed) || host.Snapshot().ReservedBytes != 0 {
		t.Fatalf("closed device admission = %v, host=%+v", err, host.Snapshot())
	}
}
