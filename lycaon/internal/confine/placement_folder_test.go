//go:build darwin || linux

package confine_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

type placementGrantFixture struct {
	parent string
	folder string
	branch string
	other  string
	marker string
}

// newPlacementGrantFixture registers a placement folder for an isolated state
// folder, with the on-disk shapes the registry reads.
func newPlacementGrantFixture(t *testing.T) placementGrantFixture {
	t.Helper()
	state := filepath.Join(fspath.CanonicalPath(t.TempDir()), "state")
	testutil.FailErr(t, "create state", os.MkdirAll(state, 0o700))
	t.Setenv(configdir.EnvConfigDir, state)
	parent := fspath.CanonicalPath(t.TempDir())
	folder := filepath.Join(parent, enginepaths.WorkerPlacementDirName(os.Getuid(), state))
	branch := filepath.Join(enginepaths.WorkerBranchesRootUnder(folder), "0123456789abcdef", "job")
	other := filepath.Join(enginepaths.WorkerBranchesRootUnder(folder), "0123456789abcdef", "other")
	for _, dir := range []string{branch, other} {
		testutil.FailErr(t, "create branch", os.MkdirAll(dir, 0o700))
	}
	marker := filepath.Join(folder, "placement.json")
	writeJSON := func(path string, value any) {
		raw, err := json.Marshal(value)
		testutil.FailErr(t, "encode", err)
		testutil.FailErr(t, "write", os.WriteFile(path, raw, 0o600))
	}
	writeJSON(marker, map[string]any{"format_version": 1, "id": "placement", "state_key": enginepaths.ProjectKey(state)})
	writeJSON(filepath.Join(state, enginepaths.WorkerPlacementsFileName), map[string]any{
		"format_version": 1,
		"folders":        []map[string]any{{"id": "placement", "path": folder, "volume_root": parent, "kind": "volume_root"}},
		"branches":       map[string]string{},
	})
	return placementGrantFixture{parent: parent, folder: folder, branch: branch, other: other, marker: marker}
}

func TestPlacementFolderIsCoveredLikeTheStateFolder(t *testing.T) {
	f := newPlacementGrantFixture(t)
	if !confine.ControlPlaneTree(f.marker) {
		t.Fatal("placement marker is not control plane")
	}
	if confine.ControlPlaneTree(filepath.Join(f.branch, "a.go")) {
		t.Fatal("a branch tree must stay an agent workspace")
	}
	if !confine.ControlPlanePathDenied(f.marker, true, "", "") {
		t.Fatal("a write to the marker reaches approval")
	}
	if confine.ControlPlanePathDenied(filepath.Join(f.branch, "a.go"), true, "", "") {
		t.Fatal("a write in the branch is refused")
	}
	if refused, code := confine.AttachedWriteRootRefused(f.parent); refused {
		t.Fatalf("attached root above the folder refused: code=%s", code)
	}
	if refused, code := confine.AttachedWriteRootRefused(f.folder); refused {
		t.Fatalf("attached root at the folder refused structurally: code=%s", code)
	}
	if !confine.ControlPlanePathDenied(f.parent, true, "", "") {
		t.Fatal("destructive write targeting the placement folder parent must remain control-plane denied")
	}
	boundaryRoots, err := confine.WriteRootsForBoundary("", []string{f.folder, f.branch}, nil, "")
	testutil.FailErr(t, "write roots for boundary", err)
	if slices.Contains(boundaryRoots, f.folder) {
		t.Fatalf("placement folder admitted as executable boundary write root: %v", boundaryRoots)
	}
	if !slices.Contains(boundaryRoots, f.branch) {
		t.Fatalf("branch workspace excluded from executable boundary write roots: %v", boundaryRoots)
	}
	if refused, code := confine.GrantedWriteRootRefused(f.parent); refused {
		t.Fatalf("granted root above the folder refused: code=%s", code)
	}
	if refused, code := confine.GrantedWriteRootRefused(f.folder); !refused || code != confine.WriteRootCodeControlPlane {
		t.Fatalf("granted root at the folder: refused=%v code=%s", refused, code)
	}
	if refused, code := confine.AttachedWriteRootRefused(f.branch); refused {
		t.Fatalf("attached root at the branch: code=%s", code)
	}
	c := confine.Confinement{Roots: []string{f.branch}, GrantedWriteRoots: []string{f.parent}}
	if verdict := confine.BoundaryOf(&c).Filesystem.Verdict(confine.AccessWriteUnlink, f.parent); verdict.Allowed || verdict.Layer != confine.FloorControlPlane {
		t.Fatalf("placement folder parent unlink verdict = %+v", verdict)
	}
}

func TestDefaultRootAboveAPlacementFolderStaysAndDeniesTheFolder(t *testing.T) {
	state := filepath.Join(fspath.CanonicalPath(t.TempDir()), "state")
	testutil.FailErr(t, "create state", os.MkdirAll(state, 0o700))
	t.Setenv(configdir.EnvConfigDir, state)
	parent := fspath.CanonicalPath(t.TempDir())
	t.Setenv("TMPDIR", parent)
	folder := filepath.Join(parent, enginepaths.WorkerPlacementDirName(os.Getuid(), state))
	testutil.FailErr(t, "create folder", os.MkdirAll(folder, 0o700))
	testutil.FailErr(t, "write marker", os.WriteFile(filepath.Join(folder, "placement.json"),
		[]byte(`{"format_version":1,"id":"placement","state_key":"`+enginepaths.ProjectKey(state)+`"}`), 0o600))
	testutil.FailErr(t, "write registry", os.WriteFile(filepath.Join(state, enginepaths.WorkerPlacementsFileName),
		[]byte(`{"format_version":1,"folders":[{"id":"placement","path":"`+folder+`","volume_root":"`+parent+`","kind":"outside_roots"}],"branches":{}}`), 0o600))

	roots, err := confine.WriteRootsForBoundary("", []string{t.TempDir()}, nil, "")
	testutil.FailErr(t, "write roots", err)
	if !slices.Contains(roots, parent) {
		t.Fatalf("the temporary root was dropped: %v", roots)
	}
	specs, err := confine.HardDenyWriteSpecs(confine.Confinement{Roots: []string{t.TempDir()}})
	testutil.FailErr(t, "hard deny", err)
	denied := false
	for _, spec := range specs {
		denied = denied || (spec.Layer == confine.FloorControlPlane && spec.Subpath == folder)
	}
	if !denied {
		t.Fatalf("the folder is not denied inside the default root: %+v", specs)
	}
}

func TestAbsentPlacementFolderAddsNoRules(t *testing.T) {
	f := newPlacementGrantFixture(t)
	testutil.FailErr(t, "remove marker", os.Remove(f.marker))
	if confine.ControlPlaneTree(f.marker) {
		t.Fatal("an absent volume is still control plane")
	}
	specs, err := confine.HardDenyWriteSpecs(confine.Confinement{Roots: []string{t.TempDir()}})
	testutil.FailErr(t, "hard deny", err)
	for _, spec := range specs {
		if spec.Subpath == f.folder {
			t.Fatalf("rule for an absent folder: %+v", spec)
		}
	}
}
