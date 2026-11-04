package confine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestViewStorageIsDeniedAfterEveryGrant(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve", err)
	storage := filepath.Join(base, "storage")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(storage, "inner"), 0o700))
	boundary := confine.BoundaryOf(&confine.Confinement{
		Roots:             []string{base},
		GrantedWriteRoots: []string{storage},
		ReadRoots:         []string{storage},
		View:              confine.ViewBoundary{DeniedPaths: []string{storage}},
	})
	if !boundary.Applied {
		t.Fatal("boundary not applied")
	}
	inner := filepath.Join(storage, "inner", "file")
	read := boundary.Filesystem.Verdict(confine.AccessRead, inner)
	if read.Allowed || read.Layer != confine.FloorReadControlPlane || !read.Layer.Recovery().Terminal() {
		t.Fatalf("read verdict = %+v", read)
	}
	write := boundary.Filesystem.Verdict(confine.AccessWrite, inner)
	if write.Allowed || write.Layer != confine.FloorControlPlane || !write.Layer.Recovery().Terminal() {
		t.Fatalf("write verdict = %+v", write)
	}
	if outside := boundary.Filesystem.Verdict(confine.AccessWrite, filepath.Join(base, "file")); !outside.Allowed {
		t.Fatalf("the rest of the root was closed: %+v", outside)
	}
}

// A rule written on an alias applies to nothing, so the floor holds the path's canonical form.
func TestViewStorageRuleNamesTheCanonicalPath(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve", err)
	storage := filepath.Join(base, "storage")
	testutil.FailErr(t, "mkdir", os.Mkdir(storage, 0o700))
	alias := filepath.Join(base, "alias")
	testutil.FailErr(t, "symlink", os.Symlink(storage, alias))
	profile, err := confine.BuildSeatbeltProfile(confine.Confinement{Roots: []string{base}, View: confine.ViewBoundary{DeniedPaths: []string{alias}}})
	testutil.FailErr(t, "profile", err)
	if strings.Contains(profile, `"`+alias+`"`) {
		t.Fatal("the profile names the alias")
	}
	verdict := confine.BoundaryOf(&confine.Confinement{Roots: []string{base}, View: confine.ViewBoundary{DeniedPaths: []string{alias}}}).Filesystem.Verdict(confine.AccessWrite, storage)
	if verdict.Allowed {
		t.Fatalf("canonical path allowed through an aliased rule: %+v", verdict)
	}
}

func TestViewListenerDenialFollowsTheLoopbackGrant(t *testing.T) {
	profile, err := confine.BuildSeatbeltProfile(confine.Confinement{
		Roots: []string{t.TempDir()}, LoopbackConnect: true,
		View: confine.ViewBoundary{DeniedLoopbackPorts: []uint16{2049}},
	})
	testutil.FailErr(t, "profile", err)
	allow := strings.Index(profile, `(allow network-outbound (remote ip "localhost:*"))`)
	deny := strings.Index(profile, `(deny network-outbound (remote ip "localhost:2049"))`)
	if allow < 0 || deny < allow {
		t.Fatalf("listener denial must follow the loopback grant: allow=%d deny=%d", allow, deny)
	}
}

// A narrowed grant cannot name a listener, and losing its last port never widens it to every port.
func TestNarrowedLoopbackGrantNeverNamesAListener(t *testing.T) {
	render := func(ports ...uint16) string {
		profile, err := confine.BuildSeatbeltProfile(confine.Confinement{
			Roots: []string{t.TempDir()}, LoopbackConnect: true, LoopbackConnectPorts: ports,
			View: confine.ViewBoundary{DeniedLoopbackPorts: []uint16{2049}},
		})
		testutil.FailErr(t, "profile", err)
		return profile
	}
	if profile := render(2049, 8080); strings.Contains(profile, `(allow network-outbound (remote ip "localhost:2049"))`) || !strings.Contains(profile, `(allow network-outbound (remote ip "localhost:8080"))`) {
		t.Fatalf("narrowed grant: %s", profile)
	}
	if profile := render(2049); strings.Contains(profile, `(allow network-outbound (remote ip "localhost:`) {
		t.Fatalf("an emptied grant allows loopback: %s", profile)
	}
}

func TestProfileDeniesMountingAndUnmounting(t *testing.T) {
	profile, err := confine.BuildSeatbeltProfile(confine.Confinement{Roots: []string{t.TempDir()}})
	testutil.FailErr(t, "profile", err)
	for _, want := range []string{"(deny file-mount)", "(deny file-unmount)"} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile lacks %s", want)
		}
	}
}

// A view's boundary is a live host fact: it stays out of the reviewed digest.
func TestViewBoundaryRequestReachesTheConfinement(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	if !confine.Available() {
		t.Skip("confinement unavailable on this host")
	}
	root := t.TempDir()
	box, ok := confine.DefaultConfinement(confine.Request{
		Roots: []string{root}, Egress: confine.EgressDeny,
		View: confine.ViewBoundary{DeniedPaths: []string{"", root + "/s", root + "/s"}, DeniedLoopbackPorts: []uint16{0, 7, 7}},
	})
	if !ok {
		t.Fatal("no confinement")
	}
	if len(box.View.DeniedPaths) != 1 || len(box.View.DeniedLoopbackPorts) != 1 || box.View.DeniedLoopbackPorts[0] != 7 {
		t.Fatalf("view = %+v", box.View)
	}
}

// A branch's own tree stays open below a base closed to it, and a pristine tree stays read-only
// even where the worker may write.
func TestReopenedRootAndWriteDeniedPristineVerdicts(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve", err)
	own := filepath.Join(base, "own")
	pristine := filepath.Join(own, "pristine")
	testutil.FailErr(t, "mkdir", os.MkdirAll(pristine, 0o700))
	boundary := confine.BoundaryOf(&confine.Confinement{
		Roots: []string{own},
		View:  confine.ViewBoundary{DeniedPaths: []string{base}, ReopenedPaths: []string{own}, WriteDeniedPaths: []string{pristine}},
	})
	rules := boundary.Filesystem
	if !rules.Verdict(confine.AccessRead, filepath.Join(own, "f")).Allowed || !rules.Verdict(confine.AccessWrite, filepath.Join(own, "f")).Allowed {
		t.Fatal("the worker's own root was closed")
	}
	for _, access := range []confine.FilesystemAccess{confine.AccessRead, confine.AccessWrite} {
		if rules.Verdict(access, filepath.Join(base, "sibling", "f")).Allowed {
			t.Fatalf("a sibling allowed %s", access)
		}
	}
	if !rules.Verdict(confine.AccessRead, filepath.Join(pristine, "f")).Allowed {
		t.Fatal("the pristine tree must stay readable")
	}
	if v := rules.Verdict(confine.AccessWrite, filepath.Join(pristine, "f")); v.Allowed || !v.Layer.Recovery().Terminal() {
		t.Fatalf("pristine write verdict = %+v", v)
	}
}

// A placement folder holds as the state folder does: closed, with its worker base left to a worker
// that owns a branch there, and never a write root by itself.
func TestPlacementFolderIsCoveredLikeTheStateFolder(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve", err)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(base, "state"))
	placement := filepath.Join(base, "volume", ".paintedwolf")
	branch := filepath.Join(placement, "worker-branches", "project", "job")
	testutil.FailErr(t, "mkdir", os.MkdirAll(branch, 0o700))
	testutil.FailErr(t, "mkdir state", os.MkdirAll(filepath.Join(base, "state"), 0o700))
	confine.SetPlacementFolders(func() []string { return []string{placement} })
	t.Cleanup(func() { confine.SetPlacementFolders(nil) })

	if !confine.ControlPlaneTree(filepath.Join(placement, "registry")) {
		t.Fatal("the placement folder is not control plane")
	}
	if confine.ControlPlaneTree(branch) {
		t.Fatal("a placed branch is not a workspace")
	}
	if refused, code := confine.AttachedWriteRootRefused(branch); refused {
		t.Fatalf("a placed branch was refused: %s", code)
	}
	if refused, _ := confine.AttachedWriteRootRefused(filepath.Join(placement, "registry")); !refused {
		t.Fatal("a write root in the placement folder was admitted")
	}
	if refused, code := confine.AttachedWriteRootRefused(filepath.Dir(placement)); !refused || code != confine.WriteRootCodeContainsState {
		t.Fatalf("a root holding the placement folder: refused=%v code=%s", refused, code)
	}
	rules := confine.BoundaryOf(&confine.Confinement{Roots: []string{branch}, ReadRoots: []string{branch}}).Filesystem
	inside := filepath.Join(branch, "f")
	if !rules.Verdict(confine.AccessRead, inside).Allowed || !rules.Verdict(confine.AccessWrite, inside).Allowed {
		t.Fatal("a worker could not use its own placed branch")
	}
	registry := filepath.Join(placement, "registry")
	if rules.Verdict(confine.AccessRead, registry).Allowed || rules.Verdict(confine.AccessWrite, registry).Allowed {
		t.Fatal("the placement folder is reachable")
	}
}
