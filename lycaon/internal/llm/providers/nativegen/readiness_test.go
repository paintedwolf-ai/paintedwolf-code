package nativegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReadinessTracksAdmissionExitAndClose(t *testing.T) {
	_, runtime := fixtureProvider(t)
	if got := runtime.Readiness(); got.State != "idle" || got.Action != "retry" {
		t.Fatalf("unloaded readiness: %+v", got)
	}
	testutil.FailErr(t, "warm readiness fixture", runtime.Warm(t.Context(), DefaultModelID))
	_, release, err := runtime.acquire(t.Context())
	testutil.FailErr(t, "hold inference gate", err)
	got := runtime.Readiness()
	release()
	if got.State != "ready" || got.Revision != "fixture" || got.ContextTokens != 64 {
		t.Fatalf("admitted readiness: %+v", got)
	}
	runtime.loaded.process.close()
	if got := runtime.Readiness(); got.State != "idle" || got.Action != "retry" {
		t.Fatalf("exited readiness: %+v", got)
	}
	testutil.FailErr(t, "close readiness fixture", runtime.Close())
	if got := runtime.Readiness(); got.State != "stopped" || got.Action != "none" {
		t.Fatalf("closed readiness: %+v", got)
	}
}

func TestReadinessDistinguishesCapacityAndRepair(t *testing.T) {
	t.Run("capacity", func(t *testing.T) {
		_, runtime := fixtureProvider(t)
		var err error
		runtime.config.DeviceMemory, err = inference.NewMemoryBudget(1)
		testutil.FailErr(t, "limit device capacity", err)
		if err := runtime.Warm(t.Context(), DefaultModelID); err == nil {
			t.Fatal("admitted insufficient device capacity")
		}
		if got := runtime.Readiness(); got.State != "insufficient_memory" || got.Action != "retry" {
			t.Fatalf("capacity readiness: %+v", got)
		}
	})
	t.Run("repair", func(t *testing.T) {
		runtime := repairFixture(t)
		directory := runtime.loaded.process.lease.Dir
		runtime.unload()
		testutil.FailErr(t, "remove installed payload", os.Remove(filepath.Join(directory, "model.gguf")))
		if err := runtime.Warm(t.Context(), DefaultModelID); err == nil {
			t.Fatal("loaded missing payload")
		}
		if got := runtime.Readiness(); got.State != "repair_required" || got.Action != "repair" {
			t.Fatalf("damaged readiness: %+v", got)
		}
		testutil.FailErr(t, "repair readiness fixture", runtime.Repair(t.Context()))
		if got := runtime.Readiness(); got.State != "ready" || got.Action != "none" {
			t.Fatalf("repaired readiness: %+v", got)
		}
	})
}
