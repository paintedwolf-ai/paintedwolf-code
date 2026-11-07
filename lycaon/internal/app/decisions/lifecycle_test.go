package decisions

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCloseReapsOwnedEngineBeforeReleasingMemory(t *testing.T) {
	directory := t.TempDir()
	for _, file := range bialy.ShippedModel.Files {
		path := filepath.Join(directory, filepath.FromSlash(file.Path))
		testutil.FailErr(t, "create checkpoint directory", os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "write fixture payload", os.WriteFile(path, []byte("fixture"), 0o600))
	}
	testutil.FailErr(t, "complete fixture checkpoint", os.WriteFile(filepath.Join(directory, bialy.CompleteMarker), []byte("fixture"), 0o600))
	binary := filepath.Join(directory, "bialy")
	const script = `#!/bin/sh
IFS= read -r request
printf '%s\n' '{"id":1,"physical_device":{"kind":"pci","address":"0000:03:00.0"},"engine":{"name":"fixture","model":"fixture","device":"vulkan:0","dtype":"f16"}}'
while IFS= read -r request; do :; done
`
	testutil.FailErr(t, "write fixture engine", os.WriteFile(binary, []byte(script), 0o700))
	devices, err := inference.NewDevices(2)
	testutil.FailErr(t, "device admission", err)
	t.Cleanup(devices.Close)
	host, err := inference.NewMemoryBudget(1024)
	testutil.FailErr(t, "host budget", err)
	device, err := inference.NewMemoryBudget(1024)
	testutil.FailErr(t, "device budget", err)
	resources := &bialy.Resources{Devices: devices, HostMemory: host, DeviceMemory: device,
		PhysicalDevice: inference.PhysicalDevice{Kind: "pci", Address: "0000:03:00.0"},
		HostBytes:      128, DeviceBytes: 256, MaxRows: 2}
	var runtime Runtime
	testutil.FailErr(t, "load decision runtime", runtime.Load(nil, bialy.Config{Binary: binary, ModelDir: directory, Resources: resources}))
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	testutil.FailErr(t, "warm decision runtime", runtime.Warm(t.Context()))
	if got := host.Snapshot().ReservedBytes; got != 128 {
		t.Fatalf("owned process has %d reserved bytes", got)
	}
	testutil.FailErr(t, "close decision runtime", runtime.Close(t.Context()))
	if host.Snapshot().ReservedBytes != 0 || device.Snapshot().ReservedBytes != 0 {
		t.Fatal("runtime close returned before process reservations were released")
	}
}

func TestCloseDoesNotOwnInjectedDecisionEngine(t *testing.T) {
	engine := &externalEngine{}
	var runtime Runtime
	testutil.FailErr(t, "inject decision engine", runtime.Load(engine, bialy.Config{}))
	testutil.FailErr(t, "close runtime", runtime.Close(t.Context()))
	if engine.closed {
		t.Fatal("runtime closed an injected engine")
	}
}

type externalEngine struct {
	warmFixture
	closed bool
}

func (e *externalEngine) Close() error { e.closed = true; return nil }
