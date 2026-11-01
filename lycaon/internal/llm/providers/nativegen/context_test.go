package nativegen

import (
	"errors"
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
