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
	installed := filepath.Join(runtime.loaded.process.lease.Dir, "model.gguf")
	testutil.FailErr(t, "make installed fixture writable", os.Chmod(installed, 0o600))
	testutil.FailErr(t, "damage installed payload", os.WriteFile(installed, []byte("invalid weights"), 0o600))
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

func TestMissingSeedDoesNotHideHealthyProcess(t *testing.T) {
	runtime := repairFixture(t)
	previous := runtime.loaded.process
	runtime.config.SeedDirectory = ""
	if err := runtime.Repair(t.Context()); err == nil {
		t.Fatal("repaired without a seed")
	}
	if runtime.loaded.process != previous || runtime.Readiness().State != "ready" {
		t.Fatal("rejected repair changed the healthy runtime")
	}
}

func TestFirstLoadImportsSeedAndRetriesFailedInstallation(t *testing.T) {
	for _, damaged := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "retry"}[damaged], func(t *testing.T) {
			runtime := repairFixture(t)
			runtime.unload()
			runtime.config.ModelStore = modelstore.New(t.TempDir(), fixtureAuthority{})
			payload := filepath.Join(runtime.config.SeedDirectory, "model.gguf")
			if damaged {
				testutil.FailErr(t, "damage initial seed", os.WriteFile(payload, []byte("damaged"), 0o600))
				if err := runtime.Warm(t.Context(), DefaultModelID); err == nil {
					t.Fatal("loaded damaged seed")
				}
				if runtime.Readiness().State != "repair_required" || runtime.loaded != nil || runtime.config.HostMemory.Snapshot().ReservedBytes != 0 {
					t.Fatal("initial import failure retained resources or lost repair state")
				}
				testutil.FailErr(t, "restore initial seed", os.WriteFile(payload, []byte("fixture weights"), 0o600))
			}
			testutil.FailErr(t, "load initial seed", runtime.Warm(t.Context(), DefaultModelID))
			if runtime.Readiness().State != "ready" || runtime.config.ModelStore.Status(DefaultModelID, "vulkan").State != modelstore.StateReady {
				t.Fatal("first load did not install and admit the seed")
			}
		})
	}
}

func TestRepairReusesVerifiedPayloadWhenSeedPayloadIsDamaged(t *testing.T) {
	runtime := repairFixture(t)
	installed := filepath.Join(runtime.loaded.process.lease.Dir, "model.gguf")
	before, err := os.Stat(installed)
	testutil.FailErr(t, "stat installed payload", err)
	seed := filepath.Join(runtime.config.SeedDirectory, "model.gguf")
	testutil.FailErr(t, "damage redundant seed", os.WriteFile(seed, []byte("invalid weights"), 0o600))
	testutil.FailErr(t, "repair with verified payload", runtime.Repair(t.Context()))
	after, err := os.Stat(filepath.Join(runtime.loaded.process.lease.Dir, "model.gguf"))
	testutil.FailErr(t, "stat repaired payload", err)
	if !os.SameFile(before, after) || runtime.Readiness().State != "ready" {
		t.Fatal("repair replaced verified bytes or lost readiness")
	}
	testutil.FailErr(t, "audit reused payload", runtime.loaded.process.lease.Audit(t.Context()))
}
