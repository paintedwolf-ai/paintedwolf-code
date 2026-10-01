package extensionstate_test

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func deviceCatalogFixture(t *testing.T) *catalogview.Cache {
	t.Helper()
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	extpacks.ClearActive()
	t.Cleanup(extpacks.ClearActive)
	root := configlayout.FindModuleRoot()
	if root == "" {
		t.Fatal("module root not found")
	}
	return catalogview.NewCache(root, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func writeDeviceDesired(t *testing.T, body string) {
	t.Helper()
	path, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "device desired path", err)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o700))
	testutil.FailErr(t, "write desired", os.WriteFile(path, []byte(body), 0o600))
}

func floorDiag(eff *extpacks.EffectiveCatalog) bool {
	for _, d := range eff.Diagnostics {
		if d.Code == extpacks.DiagDesiredStateRejected {
			return true
		}
	}
	return false
}

func TestPublishDeviceCatalogServesCommittedState(t *testing.T) {
	cache := deviceCatalogFixture(t)
	eff, view, err := extensionstate.PublishDeviceCatalog(t.Context(), cache, nil, nil)
	testutil.FailErr(t, "publish device catalog", err)
	if view == nil || eff == nil {
		t.Fatal("expected catalog and view")
	}
	if floorDiag(eff) {
		t.Fatal("clean state must not degrade to the stock floor")
	}
	if extpacks.Active() != eff {
		t.Fatal("expected the committed catalog to be active")
	}
}

func TestPublishDeviceCatalogFloorsOnDisabledPlatform(t *testing.T) {
	cache := deviceCatalogFixture(t)
	writeDeviceDesired(t, "format: 1\npacks:\n  - id: painted-wolf/platform\n    enabled: false\ndisabled: []\nown: {}\n")

	eff, view, err := extensionstate.PublishDeviceCatalog(t.Context(), cache, nil, nil)
	testutil.FailErr(t, "publish fallback catalog", err)
	if view == nil {
		t.Fatal("expected a stock-floor view")
	}
	if !floorDiag(eff) {
		t.Fatalf("expected %s diagnostic, got %v", extpacks.DiagDesiredStateRejected, eff.Diagnostics)
	}
	testutil.FailErr(t, "floor catalog must boot", eff.BootError())
	if extpacks.Active() != eff {
		t.Fatal("expected the fallback catalog to be active")
	}
}

// Disabling an optional tool schema does not require the stock fallback.
func TestPublishDeviceCatalogServesCommittedStateWithDisabledToolSchema(t *testing.T) {
	cache := deviceCatalogFixture(t)
	writeDeviceDesired(t, "format: 1\npacks: []\ndisabled: [tools/schemas/write]\nown: {}\n")

	eff, view, err := extensionstate.PublishDeviceCatalog(t.Context(), cache, nil, nil)
	testutil.FailErr(t, "publish committed view", err)
	if view == nil || view.ToolSchemas == nil {
		t.Fatal("expected a committed view with tool schemas")
	}
	if floorDiag(eff) {
		t.Fatal("a disabled tool schema must not degrade to the stock floor")
	}
	if eff.HasLoaded("tools/schemas/write") {
		t.Fatal("the user's disable must be honored, not overridden by the floor")
	}
	found := false
	for _, d := range eff.Diagnostics {
		if d.Code == extpacks.DiagToolSchemaMissing && d.UnitID == "tools/schemas/write" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %s diagnostic, got %v", extpacks.DiagToolSchemaMissing, eff.Diagnostics)
	}
	if extpacks.Active() != eff {
		t.Fatal("expected the committed catalog to be active")
	}
}
