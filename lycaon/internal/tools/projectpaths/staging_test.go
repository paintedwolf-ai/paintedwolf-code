package projectpaths_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestStagingPolicyDoesNotAuthorizeContentMutation(t *testing.T) {
	root := t.TempDir()
	recorder := &mutationRecorder{}
	tc := tools.ToolContext{
		Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root",
		MutationRecorder: recorder,
	}
	// Agent policy resolves for review; the approval gate, not the resolver, decides.
	for _, path := range []string{
		"AGENTS.md", "pkg/AGENTS.md",
		filepath.Join(settingsoverlay.DirName(), "approvals.yaml"),
		filepath.Join(settingsoverlay.DirName(), "mcp.yaml"),
	} {
		_, err := projectpaths.ResolveGitStage(t.Context(), nil, tc, path)
		testutil.FailErr(t, "stage policy", err)
		_, err = projectpaths.ResolveWrite(t.Context(), nil, tc, path)
		testutil.FailErr(t, "resolve policy for review", err)
	}
	recorder.paths = nil
	_, err := projectpaths.ResolveGitStage(t.Context(), nil, tc, "source.go")
	testutil.FailErr(t, "stage ordinary source", err)
	if len(recorder.paths) != 0 {
		t.Fatalf("staging recorded file edits: %v", recorder.paths)
	}
	_, err = projectpaths.ResolveWrite(t.Context(), nil, tc, "source.go")
	testutil.FailErr(t, "edit ordinary source", err)
	if len(recorder.paths) != 1 || recorder.paths[0] != "source.go" {
		t.Fatalf("content mutation not recorded: %v", recorder.paths)
	}
}

func TestStagingRetainsRepositoryAndWorkerBoundaries(t *testing.T) {
	root := t.TempDir()
	tc := tools.ToolContext{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root"}
	for _, path := range []string{".git/config", ".git/hooks/pre-commit", filepath.Join(t.TempDir(), "AGENTS.md")} {
		if _, err := projectpaths.ResolveGitStage(t.Context(), nil, tc, path); err == nil {
			t.Errorf("staging accepted protected path %s", path)
		}
	}
	tc.WorkerJobID = "unclaimed-worker"
	_, err := projectpaths.ResolveGitStage(t.Context(), nil, tc, "AGENTS.md")
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "WORKER_WRITE_WITHOUT_BRANCH" {
		t.Fatalf("worker staged on primary tree: %v", err)
	}
}
