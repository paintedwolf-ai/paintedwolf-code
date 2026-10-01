package toolscope_test

import (
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolscope"
)

func TestLoadToolsScopeYAML(t *testing.T) {
	cfg, err := toolscope.Load()
	testutil.FailErr(t, "load tools-scope", err)
	if cfg.RootStructuralFileCount != toolscope.Default().RootStructuralFileCount {
		t.Fatalf("root_structural_file_count = %d want %d", cfg.RootStructuralFileCount, toolscope.Default().RootStructuralFileCount)
	}
}

func TestLoadTakesThresholdsFromCatalog(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{
		config.ToolsScope: "version: 1\nroot_structural_file_count: 1234\n",
	})
	cfg, err := toolscope.Load()
	testutil.FailErr(t, "load overlaid tools-scope", err)
	if cfg.RootStructuralFileCount != 1234 {
		t.Fatalf("root_structural_file_count = %d want 1234", cfg.RootStructuralFileCount)
	}
}

func TestNeedsRootScopeGuardFailSafe(t *testing.T) {
	if !toolscope.NeedsRootScopeGuard(0, false, 50_000) {
		t.Fatal("unknown count must fail safe to denseness")
	}
	if !toolscope.NeedsRootScopeGuard(50_000, true, 50_000) {
		t.Fatal("threshold count must guard")
	}
	if toolscope.NeedsRootScopeGuard(100, true, 50_000) {
		t.Fatal("small known count must not guard")
	}
}
