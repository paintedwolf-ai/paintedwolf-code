package localmodels_test

import (
	"errors"
	"math"
	"testing"

	"github.com/lycaon/lycaon/internal/app/localmodels"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDecisionCapacitySurvivesGenerationReloads(t *testing.T) {
	for _, unified := range []bool{false, true} {
		name := "discrete"
		identity := inference.PhysicalDevice{Kind: "pci", Address: "0000:03:00.0"}
		if unified {
			name = "unified"
			identity = inference.PhysicalDevice{Kind: "apple_silicon"}
		}
		t.Run(name, func(t *testing.T) {
			facts := inference.DeviceFacts{PhysicalDevice: identity, TotalBytes: 8 << 30, RecommendedBytes: 6 << 30, UnifiedMemory: unified}
			plan := bialy.MemoryPlan{HostBytes: 384 << 20, DeviceBytes: 1600 << 20, MaxRows: 2}
			r, err := localmodels.AllocateResources(8<<30, facts, plan)
			testutil.FailErr(t, "partition physical capacity", err)
			t.Cleanup(r.Close)
			hostTotal := r.GenerationHost.Snapshot().CapacityBytes + r.Decision.HostMemory.Snapshot().CapacityBytes
			deviceTotal := r.GenerationDevice.Snapshot().CapacityBytes + r.Decision.DeviceMemory.Snapshot().CapacityBytes
			if hostTotal != 6<<30 || deviceTotal != 6<<30 {
				t.Fatalf("physical domains over/underallocated: host=%d device=%d", hostTotal, deviceTotal)
			}
			gen, err := r.GenerationDevice.Reserve(r.GenerationDevice.Snapshot().CapacityBytes)
			testutil.FailErr(t, "fill generation partition", err)
			defer gen.Release()
			for range 3 {
				reserved, err := inference.ReserveMemory(r.Decision.HostMemory, r.Decision.DeviceMemory, plan.HostBytes, plan.DeviceBytes, unified)
				testutil.FailErr(t, "restart decision engine at full generation capacity", err)
				for _, held := range reserved {
					held.Release()
				}
			}
			if _, err := r.GenerationDevice.Reserve(1); !errors.Is(err, inference.ErrCapacity) {
				t.Fatalf("generation borrowed decision capacity: %v", err)
			}
			first, err := r.Devices.Bind(identity)
			testutil.FailErr(t, "bind generation queue", err)
			second, err := r.Decision.Devices.Bind(identity)
			testutil.FailErr(t, "bind decision queue", err)
			if first != second {
				t.Fatal("runtimes received separate physical queues")
			}
			r.Close()
			if _, err := r.Decision.HostMemory.Reserve(1); !errors.Is(err, inference.ErrClosed) {
				t.Fatalf("closed resource pool accepted work: %v", err)
			}
		})
	}
}

func TestResourcePlanRejectsMissingCapacity(t *testing.T) {
	for _, name := range []string{"ram", "device", "available", "overflow", "identity", "unified", "rows", "host", "decision-device"} {
		t.Run(name, func(t *testing.T) {
			facts := inference.DeviceFacts{PhysicalDevice: inference.PhysicalDevice{Kind: "apple_silicon"}, TotalBytes: 8 << 30, RecommendedBytes: 6 << 30, UnifiedMemory: true}
			plan := bialy.MemoryPlan{HostBytes: 384 << 20, DeviceBytes: 1600 << 20, MaxRows: 2}
			ram := uint64(8 << 30)
			switch name {
			case "ram":
				ram = 4 << 30
			case "device":
				facts.RecommendedBytes = 9 << 30
			case "available":
				available := uint64(1 << 30)
				facts.AvailableBytes = &available
			case "overflow":
				plan.HostBytes = math.MaxUint64
			case "identity":
				facts.PhysicalDevice = inference.PhysicalDevice{}
			case "unified":
				facts.UnifiedMemory = false
			case "rows":
				plan.MaxRows = 8
			case "host":
				plan.HostBytes = 0
			case "decision-device":
				plan.DeviceBytes = 0
			}
			if r, err := localmodels.AllocateResources(ram, facts, plan); err == nil {
				r.Close()
				t.Fatal("invalid physical plan accepted")
			}
		})
	}
}

func TestMLXWorkspaceRemainsReservedAcrossBothRuntimeLifetimes(t *testing.T) {
	facts := inference.DeviceFacts{PhysicalDevice: inference.PhysicalDevice{Kind: "apple_silicon"}, TotalBytes: 8 << 30, RecommendedBytes: 6 << 30, UnifiedMemory: true}
	plan := bialy.MemoryPlan{HostBytes: 384 << 20, DeviceBytes: 2 << 30, WorkspaceBytes: 1 << 30, MaxRows: 2}
	resources, err := localmodels.AllocateResources(8<<30, facts, plan)
	testutil.FailErr(t, "allocate shared workspace", err)
	defer resources.Close()
	if resources.SharedWorkspaceBytes != plan.WorkspaceBytes || resources.Decision.WorkspaceBytes != plan.WorkspaceBytes {
		t.Fatal("workspace grant lost at composition")
	}
	if resources.GenerationHost.Snapshot().CapacityBytes != (6<<30)-plan.HostBytes-plan.DeviceBytes {
		t.Fatal("workspace was not retained in the decision partition")
	}
	facts.PhysicalDevice = inference.PhysicalDevice{Kind: "pci", Address: "0000:03:00.0"}
	if _, err := localmodels.AllocateResources(8<<30, facts, plan); err == nil {
		t.Fatal("unqualified adapter shared transient workspace")
	}
}

func TestEightGiBMacAdmitsSixHeadsAndSixteenKGenerationContext(t *testing.T) {
	facts := inference.DeviceFacts{PhysicalDevice: inference.PhysicalDevice{Kind: "apple_silicon"}, TotalBytes: 8 << 30, RecommendedBytes: (16 << 30) / 3, UnifiedMemory: true}
	decision := bialy.MemoryPlan{HostBytes: 384 << 20, DeviceBytes: 643835514 + 6*59881676 + (64 << 20) + (512 << 20), WorkspaceBytes: 512 << 20, MaxRows: 2}
	resources, err := localmodels.AllocateResources(8<<30, facts, decision)
	testutil.FailErr(t, "allocate minimum Mac", err)
	defer resources.Close()
	// Pinned prototype inspection at 16,384 context tokens and 128-token prefill chunks.
	const generationPeak, generationWorkspace, generationHost = 3914104832, 402653184, 536870912
	resident, err := inference.ReserveMemory(resources.GenerationHost, resources.GenerationDevice, generationHost, generationPeak-min(generationWorkspace, resources.SharedWorkspaceBytes), true)
	testutil.FailErr(t, "admit prototype context", err)
	defer func() {
		for _, held := range resident {
			held.Release()
		}
	}()
	decisionResident, err := inference.ReserveMemory(resources.Decision.HostMemory, resources.Decision.DeviceMemory, decision.HostBytes, decision.DeviceBytes, true)
	testutil.FailErr(t, "admit six decision heads", err)
	for _, held := range decisionResident {
		held.Release()
	}
}
