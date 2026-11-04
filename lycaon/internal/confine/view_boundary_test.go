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
