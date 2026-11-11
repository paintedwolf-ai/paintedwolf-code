package nativegen

import (
	"math"
	"testing"
)

func TestResourceAdmissionRejectsInvalidPlansBeforeReservation(t *testing.T) {
	valid := resourcePlan{ResidentWeightsBytes: 4, MappedPLEBytes: 20, KVCapacityBytes: 2, WorkspaceBytes: 3, AllocatorCacheBytes: 1, DeviceReservationBytes: 10, HostReservationBytes: 5}
	for _, mutate := range []func(*resourcePlan){
		func(p *resourcePlan) { p.ResidentWeightsBytes = 0 },
		func(p *resourcePlan) { p.DeviceReservationBytes++ },
		func(p *resourcePlan) { p.KVCapacityBytes = math.MaxUint64 },
		func(p *resourcePlan) { p.HostReservationBytes = math.MaxUint64 },
	} {
		plan := valid
		mutate(&plan)
		if err := plan.validate(); err == nil {
			t.Fatalf("accepted invalid plan: %+v", plan)
		}
	}
}
