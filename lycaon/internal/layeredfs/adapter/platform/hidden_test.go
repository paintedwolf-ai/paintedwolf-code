//go:build darwin || linux

package platform_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A home directory attached as a root holds the engine's state folder; through this host's mount
// the view lists, stats and opens nothing under it and cannot create it.
func TestMountedViewOfAHomeRootHidesTheStateFolder(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve home", err)
	state := filepath.Join(home, ".config", "paintedwolf")
	for rel, content := range map[string]string{"notes.txt": "notes\n", ".config/paintedwolf/store.db": "state\n"} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(home, filepath.Dir(rel)), 0o755))
		testutil.FailErr(t, "seed "+rel, os.WriteFile(filepath.Join(home, rel), []byte(content), 0o644))
	}
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve temp", err)
	selector := platform.NewSelector(platform.Config{Confinement: availableConfinement{}})
	chosen, _ := overBudgetChoice(t, selector, filepath.Join(tmp, "probe"), home)
	view, err := chosen.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: "view-hidden", BackingDir: filepath.Join(tmp, "backing"), RuntimeDir: filepath.Join(tmp, "runtime"),
		QuiesceHook: func(context.Context, string) error { return nil }, FlushClientHook: func(context.Context, string) error { return nil },
		Grants:      []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: home, IsPrimary: true}},
		HiddenPaths: []string{state},
	})
	testutil.FailErr(t, "Prepare", err)
	t.Cleanup(func() { _, _ = chosen.Release(context.Background(), view.ViewID) })
	mounted := filepath.Join(view.PrimaryWorkPath, ".config")

	entries, err := os.ReadDir(mounted)
	testutil.FailErr(t, "list .config", err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if slices.Contains(names, "paintedwolf") {
		t.Fatalf(".config lists %v", names)
	}
	for _, rel := range []string{"paintedwolf", "paintedwolf/store.db"} {
		if _, err := os.Stat(filepath.Join(mounted, rel)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("stat %s = %v, want absent", rel, err)
		}
		if _, err := os.ReadFile(filepath.Join(mounted, rel)); err == nil {
			t.Fatalf("opened %s", rel)
		}
	}
	if err := os.Mkdir(filepath.Join(mounted, "paintedwolf"), 0o755); err == nil {
		t.Fatal("created the state folder through the mount")
	}
	if got, err := os.ReadFile(filepath.Join(state, "store.db")); err != nil || string(got) != "state\n" {
		t.Fatalf("the state folder changed: %q %v", got, err)
	}
}
