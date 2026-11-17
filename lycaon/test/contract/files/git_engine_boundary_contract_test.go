package contract

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Each write sink refuses Git internals with the guard's class. The class distinguishes the Git write
// guard from the broader metadata floor beneath it, which would refuse these paths without naming one.
func TestGitBoundaryWriteSinksRefuseGitInternals(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "project", SessionID: "session", Agent: toolprofiles.DefaultToolProfileID},
		Source: tools.InvocationSource{
			Roots:               []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID:        "root",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject,
		},
	}
	for path, class := range map[string]string{".git/config": "config", ".git/hooks/pre-commit": "hooks"} {
		_, err := projectpaths.ResolveWrite(t.Context(), nil, tctx, path)
		assertGitInternalsDenied(t, "ResolveWrite "+path, err, class)
	}

	var archive bytes.Buffer
	zipped := zip.NewWriter(&archive)
	entry, err := zipped.Create(".git/hooks/pre-commit")
	contractcheck.FailErr(t, "zip entry", err)
	_, err = entry.Write([]byte("#!/bin/sh\n"))
	contractcheck.FailErr(t, "zip content", err)
	contractcheck.FailErr(t, "zip close", zipped.Close())
	contractcheck.FailErr(t, "write archive", os.WriteFile(filepath.Join(project, "a.zip"), archive.Bytes(), 0o644))
	tool := &native.ExtractArchiveTool{Boundary: contractcheck.ProdToolBoundary(t)}
	_, err = tool.Run(t.Context(), map[string]any{"path": "a.zip", "dest": "out"}, tctx)
	assertGitInternalsDenied(t, "extract_archive", err, "hooks")
	if _, err := os.Lstat(filepath.Join(project, "out", ".git", "hooks", "pre-commit")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused archive entry reached the project: %v", err)
	}
}

func assertGitInternalsDenied(t *testing.T, sink string, err error, class string) {
	t.Helper()
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" || reject.Data["class"] != class {
		t.Fatalf("%s = %v, want GIT_INTERNALS_WRITE_DENIED of class %s", sink, err, class)
	}
}
