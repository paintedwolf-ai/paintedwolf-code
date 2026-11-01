package bialy_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/testutil"
)

func sharedDevices(t *testing.T) *inference.Devices {
	t.Helper()
	devices, err := inference.NewDevices(8)
	testutil.FailErr(t, "new shared devices", err)
	t.Cleanup(devices.Close)
	return devices
}

func sharedResources(t *testing.T) *bialy.Resources {
	t.Helper()
	memory, err := inference.NewMemoryBudget(1024)
	testutil.FailErr(t, "new shared memory", err)
	t.Cleanup(memory.Close)
	return &bialy.Resources{Devices: sharedDevices(t),
		PhysicalDevice: inference.PhysicalDevice{Kind: "pci", Address: "0000:03:00.0"},
		HostMemory:     memory, DeviceMemory: memory, HostBytes: 128, DeviceBytes: 256}
}

func TestBialyRejectsMissingPhysicalIdentityForSharedAdmission(t *testing.T) {
	cfg := fakeConfig(t)
	cfg.Resources = sharedResources(t)
	client := bialy.New(cfg)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Warm(t.Context()); !errors.Is(err, inference.ErrIdentity) {
		t.Fatalf("missing physical identity: %v", err)
	}
}

func TestBialyCancellationRetainsSharedAdmissionUntilReaped(t *testing.T) {
	cfg := fakeConfig(t)
	cfg.Resources = sharedResources(t)
	identity := inference.PhysicalDevice{Kind: "pci", Address: "0000:03:00.0"}
	device, err := cfg.Resources.Devices.Bind(identity)
	testutil.FailErr(t, "bind generation adapter", err)
	body := strings.Replace(fakeEngine, `"engine":{`, `"physical_device":{"kind":"pci","address":"0000:03:00.0"},"engine":{`, 1)
	body = strings.Replace(body, `  case "$line" in`, `  case "$line" in
    *'"task":"hold"'*) while IFS= read -r ignored; do :; done; exit 0 ;;
  esac
  case "$line" in`, 1)
	testutil.FailErr(t, "install blocking engine", os.WriteFile(cfg.Binary, []byte(body), 0o755))
	client := bialy.New(cfg)
	t.Cleanup(func() { _ = client.Close() })
	testutil.FailErr(t, "warm engine", client.Warm(t.Context()))
	if got := cfg.Resources.HostMemory.Snapshot().ReservedBytes; got != 384 {
		t.Fatalf("resident decision allocation = %d, want 384", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, _, err := client.Rank(ctx, decide.HeadCodeRank, "hold", []string{"a", "b"})
		finished <- err
	}()
	testutil.WaitFor(t, func() bool { return device.Snapshot().Active != nil })
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled rank: %v", err)
	}
	if got := device.Snapshot(); got.Active == nil || got.Active.Owner != "bialy" {
		t.Fatalf("canceled caller released physical work: %+v", got)
	}
	testutil.FailErr(t, "reap canceled engine", client.Close())
	if got := device.Snapshot(); got.Active != nil {
		t.Fatalf("reaped engine retained admission: %+v", got)
	}
	if got := cfg.Resources.HostMemory.Snapshot().ReservedBytes; got != 0 {
		t.Fatalf("reaped decision allocation = %d, want 0", got)
	}
	permit, err := device.Acquire(t.Context(), inference.Work{Owner: "generation", Generation: 1, Priority: inference.Foreground})
	testutil.FailErr(t, "generation after reap", err)
	permit.Complete()
}
