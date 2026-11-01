package nativegen

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func repairFixture(t *testing.T) *Runtime {
	t.Helper()
	_, runtime := fixtureProvider(t)
	testutil.FailErr(t, "warm repair fixture", runtime.Warm(t.Context(), DefaultModelID))
	manifest := runtime.loaded.process.lease.Manifest
	raw, err := json.Marshal(modelstore.SignedManifest{Manifest: manifest, Signature: "fixture"})
	testutil.FailErr(t, "encode repair seed", err)
	runtime.config.SeedDirectory = t.TempDir()
	testutil.FailErr(t, "write seed manifest", os.WriteFile(filepath.Join(runtime.config.SeedDirectory, "manifest.json"), raw, 0o600))
	testutil.FailErr(t, "write seed payload", os.WriteFile(filepath.Join(runtime.config.SeedDirectory, "model.gguf"), []byte("fixture weights"), 0o600))
	return runtime
}

func TestRepairReapsReaderBeforeReplacingDamagedRevision(t *testing.T) {
	runtime := repairFixture(t)
	previous := runtime.loaded.process
	testutil.FailErr(t, "remove installed payload", os.Remove(filepath.Join(previous.lease.Dir, "model.gguf")))
	testutil.FailErr(t, "repair installed payload", runtime.Repair(t.Context()))
	select {
	case <-previous.done:
	default:
		t.Fatal("repair retained the previous reader")
	}
	if runtime.generation != 2 || runtime.loaded.process == previous {
		t.Fatal("repair did not load a new process")
	}
	if runtime.config.HostMemory.Snapshot().ReservedBytes != 768<<20 || runtime.config.DeviceMemory.Snapshot().ReservedBytes != 256<<20 {
		t.Fatal("repair did not replace memory reservations")
	}
	if got := runtime.config.ModelStore.Status(DefaultModelID, "vulkan"); got.State != modelstore.StateReady {
		t.Fatalf("repaired store: %+v", got)
	}
}

func TestRepairFailureCanBeRetriedWithoutRestartingHost(t *testing.T) {
	runtime := repairFixture(t)
	payload := filepath.Join(runtime.config.SeedDirectory, "model.gguf")
	testutil.FailErr(t, "damage seed payload", os.WriteFile(payload, []byte("invalid weights"), 0o600))
	if err := runtime.Repair(t.Context()); err == nil {
		t.Fatal("accepted damaged repair seed")
	}
	if runtime.loaded != nil || runtime.config.HostMemory.Snapshot().ReservedBytes != 0 || runtime.config.DeviceMemory.Snapshot().ReservedBytes != 0 {
		t.Fatal("failed repair retained process resources")
	}
	testutil.FailErr(t, "restore seed payload", os.WriteFile(payload, []byte("fixture weights"), 0o600))
	testutil.FailErr(t, "retry repaired seed", runtime.Repair(t.Context()))
}

func TestCanceledRepairLeavesCurrentProcessIntact(t *testing.T) {
	runtime := repairFixture(t)
	previous := runtime.loaded.process
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runtime.Repair(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled repair: %v", err)
	}
	if runtime.loaded.process != previous {
		t.Fatal("canceled repair replaced current process")
	}
}
