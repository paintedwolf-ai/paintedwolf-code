package nativegen

import (
	"errors"
	"math"
	"testing"

	"github.com/lycaon/lycaon/internal/inference"
)

func TestResourceAdmissionSharesUnifiedMemoryAndRollsBackDiscreteFailure(t *testing.T) {
	plan := resourcePlan{ResidentWeightsBytes: 4, MappedPLEBytes: 20, KVCapacityBytes: 2, WorkspaceBytes: 3, AllocatorCacheBytes: 1, DeviceReservationBytes: 10, HostReservationBytes: 5}
	unified, _ := inference.NewMemoryBudget(15)
	reservations, err := plan.reserve(unified, unified)
	if err != nil || len(reservations) != 1 || unified.Snapshot().ReservedBytes != 15 {
		t.Fatalf("unified reservation = %v, %v, %+v", reservations, err, unified.Snapshot())
	}
	if _, err := plan.reserve(unified, unified); !errors.Is(err, inference.ErrCapacity) {
		t.Fatalf("overcommitted unified memory: %v", err)
	}
	reservations[0].Release()
	reservations[0].Release()
	if unified.Snapshot().ReservedBytes != 0 {
		t.Fatal("reservation was not released exactly once")
	}
	host, _ := inference.NewMemoryBudget(5)
	device, _ := inference.NewMemoryBudget(9)
	if _, err := plan.reserve(host, device); !errors.Is(err, inference.ErrCapacity) {
		t.Fatalf("overcommitted discrete memory: %v", err)
	}
	if host.Snapshot().ReservedBytes != 0 || device.Snapshot().ReservedBytes != 0 {
		t.Fatal("failed admission retained memory")
	}
	device, _ = inference.NewMemoryBudget(10)
	reservations, err = plan.reserve(host, device)
	if err != nil || len(reservations) != 2 {
		t.Fatalf("discrete reservation = %v, %v", reservations, err)
	}
	for _, reservation := range reservations {
		reservation.Release()
	}
}

func TestResourceAdmissionRejectsInvalidPlansBeforeReservation(t *testing.T) {
	budget, _ := inference.NewMemoryBudget(math.MaxUint64)
	valid := resourcePlan{ResidentWeightsBytes: 4, MappedPLEBytes: 20, KVCapacityBytes: 2, WorkspaceBytes: 3, AllocatorCacheBytes: 1, DeviceReservationBytes: 10, HostReservationBytes: 5}
	for _, mutate := range []func(*resourcePlan){
		func(p *resourcePlan) { p.ResidentWeightsBytes = 0 },
		func(p *resourcePlan) { p.DeviceReservationBytes++ },
		func(p *resourcePlan) { p.KVCapacityBytes = math.MaxUint64 },
		func(p *resourcePlan) { p.HostReservationBytes = math.MaxUint64 },
	} {
		plan := valid
		mutate(&plan)
		if _, err := plan.reserve(budget, budget); err == nil {
			t.Fatalf("accepted invalid plan: %+v", plan)
		}
		if budget.Snapshot().ReservedBytes != 0 {
			t.Fatal("invalid plan retained memory")
		}
	}
	if _, err := valid.reserve(nil, budget); err == nil {
		t.Fatal("accepted missing host admission")
	}
}
