package extensionstate_test

import (
	"bytes"
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
	"gopkg.in/yaml.v3"
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

func TestPublishDeviceCatalogChecksStockRangesWithoutRewritingLocks(t *testing.T) {
	for _, tc := range []struct {
		name, dependency, constraint string
		want                         bool
	}{
		{"compatible", extpacks.PlatformPackID, "^1.0.0", true},
		{"incompatible", extpacks.PlatformPackID, "^99.0.0", false},
		{"missing", "painted-wolf/missing", "^1.0.0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := deviceCatalogFixture(t)
			author := t.TempDir()
			manifest := extpacks.Manifest{ManifestVersion: extpacks.ManifestVersion, ID: "acme/upgrade", Name: "Upgrade fixture", Version: "1.0.0", Compatibility: extpacks.ManifestCompatibility{ExtensionAPI: "^1.0.0"}, Dependencies: map[string]extpacks.DependencyRequest{tc.dependency: {Version: tc.constraint}}}
			raw, err := yaml.Marshal(manifest)
			testutil.FailErr(t, "encode manifest", err)
			testutil.FailErr(t, "write manifest", os.WriteFile(filepath.Join(author, "extension.yaml"), raw, 0o600))
			integrity, err := extpacks.PackTreeIntegrity(author)
			testutil.FailErr(t, "hash author pack", err)
			desired := extpacks.EmptyDesired()
			desired.Packs = []extpacks.DesiredPack{{ID: manifest.ID, Source: author, Development: true}}
			desiredBytes, err := extpacks.EncodeDesired(desired)
			testutil.FailErr(t, "encode device intent", err)
			writeDeviceDesired(t, string(desiredBytes))
			lock := extpacks.EmptyLock()
			lock.Packages = []extpacks.LockedPackage{{ID: manifest.ID, Version: manifest.Version, Source: author, Integrity: integrity, Kind: extpacks.PackKindPath, Dependencies: map[string]string{tc.dependency: "0.9.0"}}}
			lockBytes, err := extpacks.EncodeLock(lock)
			testutil.FailErr(t, "encode prior stock selection", err)
			lockPath, err := extpacks.DeviceLockPath()
			testutil.FailErr(t, "device lock path", err)
			testutil.FailErr(t, "write lock", os.WriteFile(lockPath, lockBytes, 0o600))
			eff, view, err := extensionstate.PublishDeviceCatalog(t.Context(), cache, nil, nil)
			testutil.FailErr(t, "publish catalog after host update", err)
			if view == nil || floorDiag(eff) {
				t.Fatal("stock dependency replaced committed state with the stock floor")
			}
			if eff.PackContributed(manifest.ID) != tc.want {
				t.Fatalf("extension contribution: %+v", eff.Packs)
			}
			if !eff.PackContributed(extpacks.PlatformPackID) {
				t.Fatal("platform did not contribute")
			}
			for path, want := range map[string][]byte{lockPath: lockBytes, filepath.Join(author, "extension.yaml"): raw} {
				got, err := os.ReadFile(path)
				testutil.FailErr(t, "read unchanged artifact", err)
				if !bytes.Equal(got, want) {
					t.Fatalf("catalog publication rewrote %s", path)
				}
			}
		})
	}
}
