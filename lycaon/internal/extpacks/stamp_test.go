package extpacks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDesiredStampTracksProjectFile(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	projectDir := t.TempDir()
	path := ProjectDesiredPath(projectDir)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))

	absent := DesiredStamp([]string{projectDir})

	testutil.FailErr(t, "write", os.WriteFile(path,
		[]byte("format: 1\ndisabled: []\n"), 0o644))
	created := DesiredStamp([]string{projectDir})
	if created == absent {
		t.Fatal("stamp must change when the project file appears")
	}

	testutil.FailErr(t, "rewrite", os.WriteFile(path,
		[]byte("format: 1\ndisabled: [guidance/some-note]\n"), 0o644))
	if edited := DesiredStamp([]string{projectDir}); edited == created {
		t.Fatal("stamp must change when the project file is edited")
	}

	if DeviceDesiredStamp() != DesiredStamp(nil) {
		t.Fatal("device stamp must not depend on a project dir")
	}
}

func TestActiveIsStaleAfterExternalDeviceWrite(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	t.Cleanup(ClearActive)

	if _, err := ApplyCatalog(t.Context(), nil, nil); err != nil {
		testutil.FailErr(t, "apply", err)
	}
	if ActiveIsStale() {
		t.Fatal("freshly applied catalog must not read as stale")
	}

	path, err := DeviceDesiredPath()
	testutil.FailErr(t, "device path", err)
	testutil.FailErr(t, "external write", os.WriteFile(path,
		[]byte("format: 1\ndisabled: [guidance/some-note]\n"), 0o600))

	if !ActiveIsStale() {
		t.Fatal("device desired write outside the API must mark the catalog stale")
	}

	if _, err := ApplyCatalog(t.Context(), nil, nil); err != nil {
		testutil.FailErr(t, "reapply", err)
	}
	if ActiveIsStale() {
		t.Fatal("reapply must clear staleness")
	}
}

func TestRefusedResolveStopsRetrying(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	t.Cleanup(ClearActive)

	if _, err := ApplyCatalog(t.Context(), nil, nil); err != nil {
		testutil.FailErr(t, "apply", err)
	}

	path, err := DeviceDesiredPath()
	testutil.FailErr(t, "device path", err)
	testutil.FailErr(t, "disable platform", os.WriteFile(path,
		[]byte("format: 1\npacks:\n  - id: painted-wolf/platform\n    enabled: false\n"), 0o600))
	if !ActiveIsStale() {
		t.Fatal("write must mark stale")
	}

	if _, err := ApplyCatalog(t.Context(), nil, nil); err == nil {
		t.Fatal("disabling platform must refuse to install a catalog")
	}
	if ActiveIsStale() {
		t.Fatal("a refused resolve must still record the file state it refused")
	}
	if !Active().PackContributed("painted-wolf/platform") {
		t.Fatal("previous catalog must survive a refused resolve")
	}
}

func TestActiveRefresherReleasePreservesReplacement(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	t.Cleanup(ClearActive)
	setActiveWithStamp(&EffectiveCatalog{}, "stale")
	oldCalls, newCalls := 0, 0
	releaseOld := SetActiveRefresher(func(context.Context) { oldCalls++ })
	releaseNew := SetActiveRefresher(func(context.Context) { newCalls++ })
	t.Cleanup(releaseNew)
	releaseOld()
	releaseOld()
	RefreshActiveIfStale(t.Context())
	if oldCalls != 0 || newCalls != 1 {
		t.Fatalf("refresh calls old=%d latest=%d", oldCalls, newCalls)
	}
	releaseNew()
	RefreshActiveIfStale(t.Context())
	if newCalls != 1 {
		t.Fatal("released refresher was invoked")
	}
}
