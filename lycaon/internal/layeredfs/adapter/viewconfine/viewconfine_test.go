package viewconfine_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/denialprobe"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/viewconfine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func requireConfinement(t *testing.T) {
	t.Helper()
	confine.TestingSetAutoConfine(t)
	if !confine.Available() {
		t.Skip("this host applies no confinement")
	}
}

func TestAvailabilityIsRefusedWhereNoBoundaryApplies(t *testing.T) {
	err := viewconfine.New().Available(t.Context(), adapter.ProbeConfig{RuntimeDir: t.TempDir()})
	var aErr *adapter.AdapterError
	if !errors.As(err, &aErr) || aErr.ReasonCode != adapter.ReasonConfinementUnavailable {
		t.Fatalf("without confinement: %v", err)
	}
}

func TestAvailabilityIsProvenByARealDenialAndLeavesNothing(t *testing.T) {
	requireConfinement(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve", err)
	t.Setenv("LYCAON_CONFIG_DIR", dir)
	// The directory workspaces live in, the only place under the state root a worker may write.
	runtimeDir := enginepaths.WorkerBranchesRootUnder(dir)
	testutil.FailErr(t, "Available", viewconfine.New().Available(t.Context(), adapter.ProbeConfig{RuntimeDir: runtimeDir}))
	entries, err := os.ReadDir(runtimeDir)
	testutil.FailErr(t, "read scratch", err)
	if len(entries) != 0 {
		t.Fatalf("proof left %v", entries)
	}
}

// A path named through an alias must still be refused: a rule on the alias alone applies to nothing.
func TestDeniedPathNamedThroughAnAliasIsRefusedAtItsCanonicalPath(t *testing.T) {
	requireConfinement(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve", err)
	root, storage := filepath.Join(base, "root"), filepath.Join(base, "storage")
	for _, dir := range []string{root, storage} {
		testutil.FailErr(t, "mkdir", os.Mkdir(dir, 0o700))
	}
	alias := filepath.Join(base, "alias")
	testutil.FailErr(t, "symlink", os.Symlink(storage, alias))
	file := filepath.Join(storage, "file")
	testutil.FailErr(t, "write", os.WriteFile(file, []byte("x"), 0o600))

	box, ok := confine.DefaultConfinement(confine.Request{Roots: []string{root}, Egress: confine.EgressDeny, View: confine.ViewBoundary{DeniedPaths: []string{alias}}})
	if !ok {
		t.Fatal("no confinement")
	}
	results, err := viewconfine.Launch(t.Context(), box, root, []denialprobe.Step{
		{Kind: denialprobe.ReadFile, Path: file},
		{Kind: denialprobe.CreateFile, Path: filepath.Join(storage, "created")},
		{Kind: denialprobe.ReadFile, Path: filepath.Join(alias, "file")},
	})
	testutil.FailErr(t, "Launch", err)
	for _, result := range results {
		if !result.Denied() {
			t.Fatalf("%s %s was not refused: errno %d", result.Kind, result.Path, result.Errno)
		}
	}
	if _, err := os.Lstat(filepath.Join(storage, "created")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a denied create reached the disk: %v", err)
	}
}

func TestLaunchRefusesAProcessThatAnswersTooFewSteps(t *testing.T) {
	requireConfinement(t)
	root := t.TempDir()
	box, ok := confine.DefaultConfinement(confine.Request{Roots: []string{root}, Egress: confine.EgressDeny})
	if !ok {
		t.Fatal("no confinement")
	}
	if _, err := viewconfine.Launch(t.Context(), box, filepath.Join(root, "missing"), []denialprobe.Step{{Kind: denialprobe.Stat, Path: root}}); err == nil {
		t.Fatal("a launch that could not enter its directory reported results")
	}
}

// A real boundary, with a real placement folder standing where a volume root's engine folder would.
func TestPlacedBranchIsUsableAndItsFolderIsClosed(t *testing.T) {
	requireConfinement(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve", err)
	state := filepath.Join(base, "state")
	testutil.FailErr(t, "mkdir state", os.Mkdir(state, 0o700))
	t.Setenv("LYCAON_CONFIG_DIR", state)
	placement := filepath.Join(base, "volume", ".paintedwolf")
	branch := filepath.Join(enginepaths.WorkerBranchesRootUnder(placement), "project", "job")
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(branch, 0o700))
	registry := filepath.Join(placement, "registry")
	testutil.FailErr(t, "write registry", os.WriteFile(registry, []byte("x"), 0o600))
	confine.SetPlacementFolders(func() []string { return []string{placement} })
	t.Cleanup(func() { confine.SetPlacementFolders(nil) })

	box, ok := confine.DefaultConfinement(confine.Request{Roots: []string{branch}, ReadRoots: []string{branch}, Egress: confine.EgressDeny})
	if !ok {
		t.Fatal("a placed branch got no boundary")
	}
	results, err := viewconfine.Launch(t.Context(), box, branch, []denialprobe.Step{
		{Kind: denialprobe.CreateFile, Path: filepath.Join(branch, "worker-file")},
		{Kind: denialprobe.ReadFile, Path: registry},
		{Kind: denialprobe.CreateFile, Path: filepath.Join(placement, "worker-file")},
	})
	testutil.FailErr(t, "Launch", err)
	if !results[0].Succeeded() {
		t.Fatalf("a worker could not write its placed branch: errno %d", results[0].Errno)
	}
	for _, result := range results[1:] {
		if !result.Denied() {
			t.Fatalf("%s %s was not refused: errno %d", result.Kind, result.Path, result.Errno)
		}
	}
}
