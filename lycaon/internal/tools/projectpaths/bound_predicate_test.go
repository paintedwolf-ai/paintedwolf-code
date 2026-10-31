//go:build darwin || linux

package projectpaths_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestBoundPredicatesUseMetadataAndCurrentDirectory(t *testing.T) {
	tc, root := readSessionContext(t)
	testutil.FailErr(t, "empty fixture", os.WriteFile(filepath.Join(root, "pkg", "empty"), nil, 0600))
	testutil.FailErr(t, "contained link", os.Symlink("b.go", filepath.Join(root, "pkg", "link")))
	testutil.FailErr(t, "dangling link", os.Symlink("missing", filepath.Join(root, "pkg", "dangling")))
	paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "pkg")
	testutil.FailErr(t, "bind current directory", err)
	for _, c := range []struct {
		path, op string
		want     bool
	}{
		{"b.go", "-s", true}, {"empty", "-s", false}, {"missing", "-s", false}, {"link", "-s", true}, {"dangling", "-s", false},
		{"b.go", "-e", true}, {"b.go/child", "-e", false}, {"b.go/", "-e", false}, {"b.go", "-f", true}, {"b.go", "-d", false}, {".", "-d", true},
		{"link", "-L", true}, {"dangling", "-L", true}, {"dangling", "-e", false},
		{"b.go", "-r", true}, {"b.go", "-w", true}, {"b.go", "-x", false}, {".", "-x", true},
	} {
		t.Run(c.op+" "+c.path, func(t *testing.T) {
			bound, err := paths.Bind(t.Context(), c.path, false)
			testutil.FailErr(t, "bind operand", err)
			got, err := bound.TestPath(c.op)
			testutil.FailErr(t, "bound predicate", err)
			if got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestBoundPredicatesKeepReadScopeOnResolvedTargets(t *testing.T) {
	tc, root := readSessionContext(t)
	testutil.FailErr(t, "link to denied file", os.Symlink("../a.go", filepath.Join(root, "pkg", "link")))
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"read": true}, ReadGlobs: []string{"pkg", "pkg/**"}}})
	paths, err := projectpaths.NewProgramPaths(t.Context(), boundary, tc, "pkg")
	testutil.FailErr(t, "bind scoped cwd", err)
	bound, err := paths.Bind(t.Context(), "link", false)
	testutil.FailErr(t, "bind link entry", err)
	link, err := bound.TestPath("-L")
	testutil.FailErr(t, "allowed link metadata", err)
	if !link {
		t.Fatal("link metadata missing")
	}
	for _, op := range []string{"-f", "-s", "-r", "-w", "-x"} {
		if _, err := bound.TestPath(op); err == nil {
			t.Fatalf("%s bypassed target read scope", op)
		}
	}
}
