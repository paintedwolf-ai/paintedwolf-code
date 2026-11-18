//go:build darwin || linux

package projectsource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

type outsideReadProject struct{ *Project }

func (p outsideReadProject) SourceRoots() []projectroot.RootRef {
	return append(p.Project.SourceRoots(), projectroot.OutsideRef("outside"))
}

func TestOutsideReadRequiresTheRecordedAddress(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve fixture directory", err)
	folder := filepath.Join(base, "folder")
	replacement := filepath.Join(base, "replacement")
	for dir, text := range map[string]string{folder: "recorded", replacement: "replacement"} {
		testutil.FailErr(t, "create fixture directory", os.Mkdir(dir, 0o700))
		testutil.FailErr(t, "seed fixture text", os.WriteFile(filepath.Join(dir, "note.txt"), []byte(text), 0o600))
	}
	path, ok := projectroot.OutsideRel(filepath.Join(folder, "note.txt"))
	if !ok {
		t.Fatal("fixture lacks an outside address")
	}
	p := outsideReadProject{fixtureProject(t.TempDir())}
	req := SourceReadRequest{RootID: "outside", Path: path}
	result, err := ReadProjectSource(p, req)
	testutil.FailErr(t, "read recorded address", err)
	if result.Content != "recorded" || result.Path != path {
		t.Fatalf("outside source = %+v", result)
	}
	req.RootID = ""
	if _, err := ReadProjectSource(p, req); !errors.Is(err, ErrSourceNotFound) && !errors.Is(err, ErrSourcePathDenied) {
		t.Fatalf("implicit outside source = %v", err)
	}
	req.RootID = "outside"
	testutil.FailErr(t, "move recorded parent", os.Rename(folder, filepath.Join(base, "moved")))
	if _, err := ReadProjectSource(p, req); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("missing outside source = %v", err)
	}
	testutil.FailErr(t, "replace parent with a link", os.Symlink(replacement, folder))
	if _, err := ReadProjectSource(p, req); !errors.Is(err, ErrSourcePathDenied) {
		t.Fatalf("redirected outside source = %v", err)
	}
}
