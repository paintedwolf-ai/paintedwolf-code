package native

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestGitCommitStagesHumanPolicyWithoutEditingIt(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	testutil.FailErr(t, "mkdir nested policy", os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	const content = "Human-authored project policy.\n"
	paths := []string{"AGENTS.md", "src/AGENTS.md"}
	for _, path := range paths {
		testutil.FailErr(t, "write policy fixture", os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644))
	}
	boundary := nativefixture.Boundary(t)
	tctx := nativefixture.Context(dir)
	hash, _, err := GitMutations{Git: git.NewManager(), Boundary: boundary}.Commit(t.Context(), tctx, GitCommitRequest{Message: "Record project policy", Paths: paths})
	testutil.FailErr(t, "commit human policy", err)
	if hash == "" {
		t.Fatal("policy commit returned no hash")
	}
	for _, path := range paths {
		if got := gittest.Run(t, dir, "show", "HEAD:"+path); got != content {
			t.Errorf("committed policy = %q, want %q", got, content)
		}
		write := &WriteTool{Boundary: boundary}
		_, err := write.Run(t.Context(), map[string]any{"path": path, "content": "Agent-authored rules\n"}, tctx)
		if err == nil {
			t.Error("policy write succeeded without review")
		}
		current, err := os.ReadFile(filepath.Join(dir, path))
		testutil.FailErr(t, "read policy after denied write", err)
		if string(current) != content {
			t.Errorf("policy bytes changed: %q", current)
		}
	}
}
