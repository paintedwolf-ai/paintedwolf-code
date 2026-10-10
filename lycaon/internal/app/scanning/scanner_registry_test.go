package scanning

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/scan"
	scanregistry "github.com/lycaon/lycaon/internal/scan/registry"
	"github.com/lycaon/lycaon/internal/testutil"
)

type scannerResources struct {
	name    string
	order   int
	release func(context.Context) error
}

func (r *scannerResources) Track(name string, order int, release func(context.Context) error) {
	r.name, r.order, r.release = name, order, release
}

func TestDefaultScannerRegistryRegistersImmediateOwnedCleanup(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	resources := &scannerResources{}
	loaded, err := loadScannerRegistry(t.Context(), Dependencies{ModuleRoot: configlayout.FindModuleRoot(), Resources: resources}, exec.ProcessPriorityBelowNormal, nil)
	testutil.FailErr(t, "allocate default registry", err)
	if resources.name != "scanner-registry" || resources.order >= 130 || resources.release == nil {
		t.Fatal("default registry has no cleanup before database shutdown")
	}
	testutil.FailErr(t, "release default registry", resources.release(t.Context()))
	if !errors.Is(loaded.(*scanregistry.Impl).Reload(), context.Canceled) {
		t.Fatal("allocation cleanup did not seal actual registry")
	}
}

func TestInjectedScannerRegistryRemainsCallerOwned(t *testing.T) {
	injected := &scan.MockRegistry{}
	resources := &scannerResources{}
	loaded, err := loadScannerRegistry(t.Context(), Dependencies{Fixtures: ScannerFixtures{Registry: injected}, Resources: resources}, exec.ProcessPriorityBelowNormal, nil)
	testutil.FailErr(t, "bind injected registry", err)
	if loaded != injected || resources.release != nil {
		t.Fatal("host closed the caller-provided scanner fixture")
	}
}

func TestCanceledScannerAllocationRegistersNoGeneration(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	resources := &scannerResources{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	loaded, err := loadScannerRegistry(ctx, Dependencies{ModuleRoot: configlayout.FindModuleRoot(), Resources: resources}, exec.ProcessPriorityBelowNormal, nil)
	if !errors.Is(err, context.Canceled) || loaded != nil || resources.release != nil {
		t.Fatalf("canceled allocation: registry=%v error=%v cleanup=%v", loaded, err, resources.release != nil)
	}
}
