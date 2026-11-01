package bialy_test

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBialyStartupHoldsAdmissionUntilHelloOrReap(t *testing.T) {
	cfg := fakeConfig(t)
	cfg.Resources = sharedResources(t)
	device, err := cfg.Resources.Devices.Bind(cfg.Resources.PhysicalDevice)
	testutil.FailErr(t, "bind loading device", err)
	marker := filepath.Join(filepath.Dir(cfg.Binary), "loading")
	body := "#!/bin/sh\ntouch '" + marker + "'\nwhile IFS= read -r ignored; do :; done\n"
	testutil.FailErr(t, "install loading engine", os.WriteFile(cfg.Binary, []byte(body), 0o755))
	client := bialy.New(cfg)
	t.Cleanup(func() { _ = client.Close() })
	finished := make(chan error, 1)
	go func() { finished <- client.Warm(t.Context()) }()
	testutil.WaitFor(t, func() bool { _, err := os.Stat(marker); return err == nil })
	if got := device.Snapshot(); got.Active == nil || got.Active.Owner != "bialy" {
		t.Fatalf("model loading lacks admission: %+v", got)
	}
	if got := cfg.Resources.HostMemory.Snapshot().ReservedBytes; got != 384 {
		t.Fatalf("model loading reserved %d bytes, want 384", got)
	}
	testutil.FailErr(t, "close loading engine", client.Close())
	if err := <-finished; err == nil {
		t.Fatal("interrupted load succeeded")
	}
	if got := device.Snapshot(); got.Active != nil {
		t.Fatalf("reaped load retained admission: %+v", got)
	}
	if got := cfg.Resources.HostMemory.Snapshot().ReservedBytes; got != 0 {
		t.Fatalf("reaped load retained %d bytes", got)
	}
}

func TestBialyStartupRejectsInvalidResourcePlans(t *testing.T) {
	for _, kind := range []string{"missing", "identity", "unified", "overflow", "device-capacity"} {
		t.Run(kind, func(t *testing.T) {
			cfg := fakeConfig(t)
			cfg.Resources = sharedResources(t)
			host := cfg.Resources.HostMemory
			switch kind {
			case "missing":
				cfg.Resources.DeviceMemory = nil
			case "identity":
				cfg.Resources.PhysicalDevice = inference.PhysicalDevice{}
			case "unified", "device-capacity":
				memory, err := inference.NewMemoryBudget(64)
				testutil.FailErr(t, "create separate device budget", err)
				t.Cleanup(memory.Close)
				cfg.Resources.DeviceMemory = memory
				if kind == "unified" {
					cfg.Resources.PhysicalDevice = inference.PhysicalDevice{Kind: "apple_silicon"}
					cfg.Resources.UnifiedMemory = false
				}
			case "overflow":
				cfg.Resources.HostBytes = math.MaxUint64
			}
			client := bialy.New(cfg)
			t.Cleanup(func() { _ = client.Close() })
			if err := client.Warm(t.Context()); err == nil {
				t.Fatal("invalid resource plan accepted")
			}
			if got := host.Snapshot().ReservedBytes; got != 0 {
				t.Fatalf("failed resource plan retained %d host bytes", got)
			}
		})
	}
}

func TestBialyCloseCancelsQueuedStartupWithoutLaunching(t *testing.T) {
	cfg := fakeConfig(t)
	cfg.Resources = sharedResources(t)
	device, err := cfg.Resources.Devices.Bind(cfg.Resources.PhysicalDevice)
	testutil.FailErr(t, "bind startup device", err)
	permit, err := device.Acquire(t.Context(), inference.Work{Owner: "generation", Generation: 1, Priority: inference.Foreground})
	testutil.FailErr(t, "hold generation work", err)
	defer permit.Complete()
	marker := filepath.Join(filepath.Dir(cfg.Binary), "started")
	body := "#!/bin/sh\ntouch '" + marker + "'\n" + strings.TrimPrefix(fakeEngine, "#!/bin/sh\n")
	testutil.FailErr(t, "install startup marker", os.WriteFile(cfg.Binary, []byte(body), 0o755))
	client := bialy.New(cfg)
	t.Cleanup(func() { _ = client.Close() })
	finished := make(chan error, 1)
	go func() { finished <- client.Warm(t.Context()) }()
	testutil.WaitFor(t, func() bool { return cfg.Resources.HostMemory.Snapshot().ReservedBytes != 0 })
	testutil.FailErr(t, "close queued startup", client.Close())
	if err := <-finished; err == nil {
		t.Fatal("closed startup succeeded")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unadmitted process launched: %v", err)
	}
	if got := cfg.Resources.HostMemory.Snapshot().ReservedBytes; got != 0 {
		t.Fatalf("queued startup retained %d bytes after close", got)
	}
	if got := device.Snapshot(); got.Active == nil || got.Active.Owner != "generation" {
		t.Fatalf("closing startup released another runtime's work: %+v", got)
	}
}

func TestBialyStartupMemoryFailureDoesNotLaunch(t *testing.T) {
	cfg := fakeConfig(t)
	cfg.Resources = sharedResources(t)
	cfg.Resources.DeviceBytes = 1024
	client := bialy.New(cfg)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Warm(t.Context()); !errors.Is(err, inference.ErrCapacity) {
		t.Fatalf("startup capacity refusal: %v", err)
	}
	if got := cfg.Resources.HostMemory.Snapshot().ReservedBytes; got != 0 {
		t.Fatalf("failed startup retained %d bytes", got)
	}
}

func TestBialyStartupIdentityMismatchReleasesResources(t *testing.T) {
	cfg := fakeConfig(t)
	cfg.Resources = sharedResources(t)
	body := strings.Replace(fakeEngine, `"engine":{`, `"physical_device":{"kind":"pci","address":"0000:04:00.0"},"engine":{`, 1)
	testutil.FailErr(t, "install wrong-device engine", os.WriteFile(cfg.Binary, []byte(body), 0o755))
	client := bialy.New(cfg)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Warm(t.Context()); !errors.Is(err, inference.ErrIdentity) {
		t.Fatalf("startup identity refusal: %v", err)
	}
	if got := cfg.Resources.HostMemory.Snapshot().ReservedBytes; got != 0 {
		t.Fatalf("wrong-device process retained %d bytes", got)
	}
	device, err := cfg.Resources.Devices.Bind(cfg.Resources.PhysicalDevice)
	testutil.FailErr(t, "bind expected device", err)
	if got := device.Snapshot(); got.Active != nil {
		t.Fatalf("wrong-device process retained startup work: %+v", got)
	}
}

func TestBialyManagedDeviceUsesPhysicalAddress(t *testing.T) {
	cfg := fakeConfig(t)
	cfg.Device = "vulkan:9"
	cfg.Resources = sharedResources(t)
	args := strings.Join(cfg.Args(), " ")
	if !strings.Contains(args, "--device vulkan-pci:0000:03:00.0") || strings.Contains(args, "vulkan:9") {
		t.Fatalf("managed device selection: %s", args)
	}
}
