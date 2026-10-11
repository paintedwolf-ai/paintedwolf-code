package native_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func TestWriteWorkerBranchOutsideProjectTree(t *testing.T) {
	primary := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	testutil.FailErr(t, "write primary main", os.WriteFile(filepath.Join(primary, "main.go"), []byte("package main\n"), 0o644))

	branchRoot := enginepaths.JobBranchDir(
		filepath.Join(configDir, enginepaths.WorkerBranchesDirName),
		primary,
		"job-1",
	)
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(branchRoot, 0o755))
	testutil.FailErr(t, "write branch main", os.WriteFile(filepath.Join(branchRoot, "main.go"), []byte("package main\n"), 0o644))

	boundary := sandbox.NewBoundary(sandbox.Config{RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID:    "implementer",
		Tools: map[string]bool{"write": true},
	}})

	write := &native.WriteTool{Boundary: boundary}
	tctx := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "p", Path: primary, IsPrimary: true}},
			ActiveRootID:     "p",
			WorkerBranchRoot: branchRoot,
			BranchWorkspace:  testutil.CompleteBranchWorkspace{}},
		Identity: tools.InvocationIdentity{WorkerJobID: "job-1",
			Agent: "implementer"},
	}
	out, err := write.Run(context.Background(), map[string]any{
		"path":    "main.go",
		"content": "// TODO: review here\npackage main\n",
	}, tctx)
	testutil.FailErr(t, "write under worker branch", err)
	if out == "" {
		t.Fatal("expected write receipt")
	}
	data, err := os.ReadFile(filepath.Join(branchRoot, "main.go"))
	testutil.FailErr(t, "read branch main", err)
	if string(data) != "// TODO: review here\npackage main\n" {
		t.Fatalf("branch main.go = %q", string(data))
	}
}
