package nativegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/inference"
)

func TestLoadAuditsContentBeforeStartingProcess(t *testing.T) {
	_, runtime := fixtureProvider(t)
	store := runtime.config.ModelStore
	lease, err := store.AcquireLease(DefaultModelID, "vulkan", "fixture", 1)
	if err != nil {
		t.Fatalf("fixture lease: %v", err)
	}
	path := filepath.Join(lease.Dir, "model.gguf")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat fixture: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("make corruption fixture writable: %v", err)
	}
	if err := os.WriteFile(path, []byte("damaged weights"), 0o600); err != nil {
		t.Fatalf("damage fixture: %v", err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore timestamp: %v", err)
	}
	lease.Release()
	if err := runtime.ensureLoaded(t.Context(), DefaultModelID); err == nil {
		t.Fatal("loaded corrupted model")
	}
	if runtime.loaded != nil || runtime.config.HostMemory.Snapshot().ReservedBytes != 0 || runtime.config.DeviceMemory.Snapshot().ReservedBytes != 0 {
		t.Fatal("failed audit retained a process or memory")
	}
}

func TestLoadRespectsTheRuntimeHostReservation(t *testing.T) {
	for _, small := range []bool{false, true} {
		name, capacity := "full", uint64(768 << 20)
		if small {
			name, capacity = "compact", 512 << 20
		}
		t.Run(name, func(t *testing.T) {
			_, runtime := fixtureProvider(t)
			if small {
				t.Setenv("PW_NATIVEGEN_SMALL_HOST_PLAN", "1")
			}
			var err error
			runtime.config.HostMemory, err = inference.NewMemoryBudget(capacity)
			if err != nil {
				t.Fatalf("host budget: %v", err)
			}
			if err := runtime.Warm(t.Context(), DefaultModelID); err != nil {
				t.Fatalf("warm within declared capacity: %v", err)
			}
			if runtime.config.HostMemory.Snapshot().ReservedBytes != capacity {
				t.Fatal("runtime did not reserve its complete host plan")
			}
			if err := runtime.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if runtime.config.HostMemory.Snapshot().ReservedBytes != 0 {
				t.Fatal("closed runtime retained host memory")
			}
		})
	}
}
