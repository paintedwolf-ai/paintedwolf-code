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
	for name, refusal := range map[string]func(string) (bool, string){
		"attached": confine.AttachedWriteRootRefused,
		"granted":  confine.GrantedWriteRootRefused,
	} {
		if refused, code := refusal(f.parent); !refused || code != confine.WriteRootCodeContainsState {
			t.Fatalf("%s root above the folder: refused=%v code=%s", name, refused, code)
		}
		if refused, code := refusal(f.branch); refused && name == "attached" {
			t.Fatalf("attached root at the branch: code=%s", code)
		}
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
