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
	display := Qualify(roots, "p", root, abs)
	if display != "@lycaon-den/src" {
		t.Fatalf("Qualify = %q", display)
	}
}

func TestDisplayPathKeysOnActiveRoot(t *testing.T) {
	roots := []RootRef{
		{ID: "p", Label: "lycaon", Path: t.TempDir(), IsPrimary: true},
		{ID: "d", Label: "lycaon-den", Path: t.TempDir(), IsPrimary: false},
	}
	cases := []struct {
		activeRootID string
		rootID       string
		rel          string
		want         string
	}{
		// Active root displays bare.
		{"d", "d", "src/foo.go", "src/foo.go"},
		{"d", "d", ".", "."},
		{"d", "d", "", "."},
		// Other roots keep the @label prefix.
		{"d", "p", "src/foo.go", "@lycaon/src/foo.go"},
		{"d", "p", ".", "@lycaon"},
		{"d", "p", "", "@lycaon"},
		// Empty activeRootID falls back to primary.
		{"", "p", "src/foo.go", "src/foo.go"},
		{"", "d", "src/foo.go", "@lycaon-den/src/foo.go"},
		// Active == primary is byte-identical to the old primary-qualified output.
		{"p", "p", "src/foo.go", "src/foo.go"},
		{"p", "d", "src/foo.go", "@lycaon-den/src/foo.go"},
		// Unlabeled roots always display bare relative paths.
		{"d", "u", "src/foo.go", "src/foo.go"},
	}
	for _, tc := range cases {
		if got := DisplayPath(roots, tc.activeRootID, tc.rootID, tc.rel); got != tc.want {
			t.Fatalf("DisplayPath(active=%q, root=%q, rel=%q) = %q want %q", tc.activeRootID, tc.rootID, tc.rel, got, tc.want)
		}
	}
}

func TestQualifyKeysOnActiveRoot(t *testing.T) {
	primary := t.TempDir()
	secondary := t.TempDir()
	roots := []RootRef{
		{ID: "p", Label: "lycaon", Path: primary, IsPrimary: true},
		{ID: "d", Label: "lycaon-den", Path: secondary, IsPrimary: false},
	}
	cases := []struct {
		activeRootID string
		root         RootRef
		abs          string
		want         string
	}{
		{"d", roots[1], filepath.Join(secondary, "src", "foo.go"), "src/foo.go"},
		{"d", roots[1], secondary, "."},
		{"d", roots[0], filepath.Join(primary, "src", "foo.go"), "@lycaon/src/foo.go"},
		{"d", roots[0], primary, "@lycaon"},
		{"", roots[0], filepath.Join(primary, "src", "foo.go"), "src/foo.go"},
		{"", roots[1], filepath.Join(secondary, "src", "foo.go"), "@lycaon-den/src/foo.go"},
		{"p", roots[0], filepath.Join(primary, "src", "foo.go"), "src/foo.go"},
		{"p", roots[1], filepath.Join(secondary, "src", "foo.go"), "@lycaon-den/src/foo.go"},
	}
	for _, tc := range cases {
		if got := Qualify(roots, tc.activeRootID, tc.root, tc.abs); got != tc.want {
			t.Fatalf("Qualify(active=%q, root=%q, abs=%q) = %q want %q", tc.activeRootID, tc.root.ID, tc.abs, got, tc.want)
		}
	}
}

func TestQualifyDisplayRoundTripActiveSecondary(t *testing.T) {
	primary := t.TempDir()
	secondary := t.TempDir()
	roots := []RootRef{
		{ID: "p", Label: "lycaon", Path: primary, IsPrimary: true},
		{ID: "d", Label: "lycaon-den", Path: secondary, IsPrimary: false},
	}
	for _, active := range []string{"", "p", "d"} {
		for _, root := range roots {
			for _, rel := range []string{".", "src", "src/foo.go", "a b/c.txt"} {
				abs := filepath.Join(root.Path, filepath.FromSlash(rel))
				display := DisplayPath(roots, active, root.ID, rel)
				gotAbs, gotRoot, err := ResolveAbs(roots, active, display)
				if err != nil {
					t.Fatalf("ResolveAbs(active=%q, %q) failed: %v", active, display, err)
				}
				if gotAbs != abs || gotRoot.ID != root.ID {
					t.Fatalf("round trip active=%q: %q -> (%q, %q) want (%q, %q)", active, display, gotAbs, gotRoot.ID, abs, root.ID)
				}
			}
		}
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
