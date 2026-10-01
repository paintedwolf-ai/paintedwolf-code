package editordoc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRootIdentitySurvivesMissingDirectory(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve fixture directory", err)
	alias := filepath.Join(base, "alias")
	testutil.FailErr(t, "create directory alias", os.Symlink(base, alias))
	want := filepath.Join(base, "project")
	p := &project.Project{Roots: []project.Root{{ID: "root", Path: filepath.Join(alias, "project")}}}
	for _, exists := range []bool{false, true, false} {
		if exists {
			testutil.FailErr(t, "create project directory", os.Mkdir(want, 0o755))
		} else {
			testutil.FailErr(t, "remove project directory", os.RemoveAll(want))
		}
		if got := rootPath(p, "root"); got != want {
			t.Fatalf("root identity with exists=%v = %q, want %q", exists, got, want)
		}
	}
	if got := rootPath(p, "detached"); got != "" {
		t.Fatalf("detached root identity = %q", got)
	}
}
