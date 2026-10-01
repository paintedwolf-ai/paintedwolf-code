package projectroot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveAbsTwoRoots(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "lycaon")
	secondary := filepath.Join(dir, "lycaon-den")
	for _, p := range []string{primary, secondary} {
		if err := os.MkdirAll(filepath.Join(p, "src"), 0o755); err != nil {
			testutil.FailErr(t, "create directory", err)
		}
	}
	roots := []RootRef{
		{ID: "p", Label: "lycaon", Path: primary, IsPrimary: true},
		{ID: "d", Label: "lycaon-den", Path: secondary, IsPrimary: false},
	}
	abs, root, err := ResolveAbs(roots, "p", "@lycaon-den/src")
	testutil.FailErr(t, "ResolveAbs failed", err)
	want := filepath.Join(secondary, "src")
	if abs != want {
		t.Fatalf("abs = %q want %q", abs, want)
	}
	if root.Label != "lycaon-den" {
		t.Fatalf("root label = %q", root.Label)
	}
	display := Qualify(roots[0], root, abs)
	if display != "@lycaon-den/src" {
		t.Fatalf("Qualify = %q", display)
	}
}

func TestResolveAbsUnknownLabel(t *testing.T) {
	roots := []RootRef{{ID: "p", Label: "lycaon", Path: t.TempDir(), IsPrimary: true}}
	_, _, err := ResolveAbs(roots, "p", "@missing/foo")
	if !errors.Is(err, ErrUnknownRootLabel) {
		t.Fatalf("ResolveAbs unknown label = %v want ErrUnknownRootLabel", err)
	}
}

func TestResolveAbsDoesNotRepairQualifiedLabels(t *testing.T) {
	roots := []RootRef{{ID: "p", Label: "main", Path: t.TempDir(), IsPrimary: true}}
	_, _, err := ResolveAbs(roots, "p", "@main /foo")
	if !errors.Is(err, ErrUnknownRootLabel) {
		t.Fatalf("err = %v, want ErrUnknownRootLabel", err)
	}
}

func TestBranchDirIsStableAcrossLabelRename(t *testing.T) {
	dir, err := BranchDirForID(RootRef{ID: "root-id", Label: "before"}.ID)
	testutil.FailErr(t, "derive branch directory", err)
	renamed, err := BranchDirForID(RootRef{ID: "root-id", Label: "after"}.ID)
	testutil.FailErr(t, "derive renamed branch directory", err)
	if renamed != dir {
		t.Fatalf("branch directory changed after label rename: %q != %q", renamed, dir)
	}
}

func TestResolveAbsNoRoots(t *testing.T) {
	_, _, err := ResolveAbs(nil, "", "src/foo")
	if !errors.Is(err, ErrNoProjectRoots) {
		t.Fatalf("err = %v want ErrNoProjectRoots", err)
	}
}

func TestIsUnionDiscoveryPath(t *testing.T) {
	for _, p := range []string{"", ".", "./", "src/foo"} {
		want := p == "" || p == "." || p == "./"
		if got := IsUnionDiscoveryPath(p); got != want {
			t.Fatalf("IsUnionDiscoveryPath(%q) = %v want %v", p, got, want)
		}
	}
}

func TestActiveRootAndScopeRel(t *testing.T) {
	roots := []RootRef{{ID: "p", Label: "lycaon", Path: "/tmp/lycaon", IsPrimary: true}}
	active, err := ActiveRoot(roots, "")
	if err != nil || active.ID != "p" {
		t.Fatalf("ActiveRoot = %+v err=%v", active, err)
	}
	rel := ScopeRel(active, "/tmp/lycaon/src/foo.go")
	if rel != "src/foo.go" {
		t.Fatalf("ScopeRel = %q", rel)
	}
}

func TestIsVirtualRootLabel(t *testing.T) {
	if !IsVirtualRootLabel("scratch") || !IsVirtualRootLabel("SCRATCH") || !IsVirtualRootLabel(" Scratch ") {
		t.Fatal("IsVirtualRootLabel must recognize scratch variants")
	}
	if IsVirtualRootLabel("main") || IsVirtualRootLabel("draft") || IsVirtualRootLabel("") {
		t.Fatal("IsVirtualRootLabel must reject non-virtual roots")
	}
}
