package nativegen

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWarmAdmitsAndPublishesContextWithinSharedBudgets(t *testing.T) {
	provider, runtime := fixtureProvider(t)
	t.Setenv("PW_NATIVEGEN_CONTEXT_PLAN", "1")
	runtime.config.ContextLimit = 32768
	runtime.config.DeviceMemory, _ = inference.NewMemoryBudget(384 << 20)
	other, err := runtime.config.DeviceMemory.Reserve(64 << 20)
	if err != nil {
		t.Fatalf("reserve decision memory: %v", err)
	}
	defer other.Release()
	if err := runtime.Warm(t.Context(), DefaultModelID); err != nil {
		t.Fatalf("warm: %v", err)
	}
	entry, _ := provider.ModelEntry(DefaultModelID)
	if entry.ContextLength != 8192 || runtime.ContextWindow() != 8192 || runtime.config.ContextLimit != 32768 {
		t.Fatalf("admitted window: %+v, %d", entry, runtime.ContextWindow())
	}
	if runtime.config.DeviceMemory.Snapshot().ReservedBytes != 384<<20 {
		t.Fatal("admission leaked failed candidate reservations")
	}
	req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Compute."}}, MaxTokens: 2}
	if _, err := provider.Complete(t.Context(), req); err != nil {
		t.Fatalf("complete admitted request: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if runtime.config.DeviceMemory.Snapshot().ReservedBytes != 64<<20 {
		t.Fatal("close released another model's reservation")
	}
}

func TestWarmRefusesCapacityBelowMinimumContext(t *testing.T) {
	_, runtime := fixtureProvider(t)
	t.Setenv("PW_NATIVEGEN_CONTEXT_PLAN", "1")
	runtime.config.ContextLimit = 32768
	runtime.config.DeviceMemory, _ = inference.NewMemoryBudget(255 << 20)
	if err := runtime.Warm(t.Context(), DefaultModelID); !errors.Is(err, inference.ErrCapacity) {
		t.Fatalf("minimum capacity: %v", err)
	}
	if runtime.loaded != nil || runtime.config.DeviceMemory.Snapshot().ReservedBytes != 0 || runtime.config.HostMemory.Snapshot().ReservedBytes != 0 {
		t.Fatal("refused load retained resources")
	}
}

func TestContextAdmissionUsesAvailableCacheBlocks(t *testing.T) {
	for _, limit := range []int{2, 4096, 32768, 65535, 65536} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			provider, runtime := fixtureProvider(t)
			t.Setenv("PW_NATIVEGEN_CONTEXT_PLAN", "1")
			runtime.config.ContextLimit = limit
			const availableTokens = 40704
			capacity := uint64(192<<20) + availableTokens*(16<<10) + (4 << 10)
			runtime.config.DeviceMemory, _ = inference.NewMemoryBudget(capacity)
			if err := runtime.Warm(t.Context(), DefaultModelID); err != nil {
				t.Fatalf("warm: %v", err)
			}
			entry, _ := provider.ModelEntry(DefaultModelID)
			if want := min(limit, availableTokens); entry.ContextLength != want {
				t.Fatalf("context=%d, want %d", entry.ContextLength, want)
			}
			if err := runtime.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if runtime.config.DeviceMemory.Snapshot().ReservedBytes != 0 || runtime.config.HostMemory.Snapshot().ReservedBytes != 0 {
				t.Fatal("context search retained reservations")
			}
		})
	}
}

func TestContextAdmissionUsesUnifiedHostCapacity(t *testing.T) {
	_, runtime := fixtureProvider(t)
	t.Setenv("PW_NATIVEGEN_CONTEXT_PLAN", "1")
	runtime.config.ContextLimit = 65536
	runtime.config.UnifiedMemory = true
	runtime.config.DeviceMemory, _ = inference.NewMemoryBudget(2 << 30)
	runtime.config.HostMemory, _ = inference.NewMemoryBudget((768+192)<<20 + 40704*(16<<10))
	if err := runtime.Warm(t.Context(), DefaultModelID); err != nil {
		t.Fatalf("warm unified context: %v", err)
	}
	if runtime.ContextWindow() != 40704 {
		t.Fatalf("unified context=%d", runtime.ContextWindow())
	}
}

func TestSharedWorkspaceChargesOnlyTheLargerTransientGrant(t *testing.T) {
	for _, shared := range []uint64{0, 32 << 20, 64 << 20, 128 << 20} {
		device, _ := inference.NewMemoryBudget(512 << 20)
		host, _ := inference.NewMemoryBudget(1 << 30)
		runtime := RuntimeConfig{HostMemory: host, DeviceMemory: device, UnifiedMemory: true, SharedWorkspaceBytes: shared}
		plan := resourcePlan{HostReservationBytes: startupHostBytes, DeviceReservationBytes: 256 << 20, WorkspaceBytes: 64 << 20}
		child := &rpcProcess{}
		if err := reserveLoaded(child, &runtime, plan); err != nil {
			t.Fatalf("reserve shared workspace: %v", err)
		}
		want := plan.DeviceReservationBytes - min(shared, plan.WorkspaceBytes)
		if host.Snapshot().ReservedBytes != want || device.Snapshot().ReservedBytes != want {
			t.Fatalf("shared=%d: host=%+v device=%+v", shared, host.Snapshot(), device.Snapshot())
		}
		for _, reservation := range child.reservations {
			reservation.Release()
		}
		if host.Snapshot().ReservedBytes != 0 || device.Snapshot().ReservedBytes != 0 {
			t.Fatal("generation reservation leaked")
		}
	}
}
