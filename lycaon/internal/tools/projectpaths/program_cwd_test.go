//go:build darwin || linux

package projectpaths_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/workspace"
)

// The command call's cwd binds the program's initial directory frame: a
// repo-relative directory, a labeled non-primary root, or a typed refusal.
func TestProgramPathsBindCallCwd(t *testing.T) {
	tc, root := readSessionContext(t)
	other := t.TempDir()
	testutil.FailErr(t, "seed other sub", os.MkdirAll(filepath.Join(other, "sub"), 0o755))
	tc.Source.Roots = append(tc.Source.Roots, projectroot.RootRef{ID: "r2", Label: "other", Path: other})
	for _, c := range []struct{ cwd, want string }{
		{"", root},
		{"pkg", filepath.Join(root, "pkg")},
		{"@other", other},
		{"@other/sub", filepath.Join(other, "sub")},
	} {
		paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, c.cwd)
		testutil.FailErr(t, "bind cwd "+c.cwd, err)
		if got := paths.Cwd().Abs; filepath.Clean(got) != filepath.Clean(c.want) {
			t.Fatalf("cwd %q bound %q, want %q", c.cwd, got, c.want)
		}
	}
	for _, c := range []struct{ cwd, code string }{
		{"../outside", "CWD_OUT_OF_SCOPE"},
		{"@missing", "CWD_OUT_OF_SCOPE"},
		{"a.go", "CWD_NOT_DIRECTORY"},
	} {
		_, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, c.cwd)
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != c.code {
			t.Fatalf("cwd %q error = %v, want %s", c.cwd, err, c.code)
		}
	}
}

// On a write worker the call cwd resolves under the private branch, never
// onto the primary tree.
func TestProgramPathsBindCallCwdUnderWorkerBranch(t *testing.T) {
	root := t.TempDir()
	branch := filepath.Join(root, "branch")
	testutil.FailErr(t, "mkdir pkg", os.MkdirAll(filepath.Join(root, "pkg"), 0o755))
	testutil.FailErr(t, "mkdir branch pkg", os.MkdirAll(filepath.Join(branch, "pkg"), 0o755))
	testutil.FailErr(t, "write job meta", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branch), workspace.JobMeta{
		Roots: []workspace.JobMetaRoot{{ID: "root", Path: root, IsPrimary: true}},
	}))
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess", WorkerJobID: "job", Agent: "implement"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
			WorkerBranchRoot: branch, BranchWorkspace: testutil.CompleteBranchWorkspace{}},
	}
	for _, c := range []struct{ cwd, want string }{{"", branch}, {"pkg", filepath.Join(branch, "pkg")}} {
		paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, c.cwd)
		testutil.FailErr(t, "bind worker cwd "+c.cwd, err)
		if got := paths.Cwd().Abs; filepath.Clean(got) != filepath.Clean(c.want) {
			t.Fatalf("worker cwd %q bound %q, want %q", c.cwd, got, c.want)
		}
	}
	_, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "../../outside")
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "CWD_OUT_OF_SCOPE" {
		t.Fatalf("worker cwd escape error = %v", err)
	}
}
