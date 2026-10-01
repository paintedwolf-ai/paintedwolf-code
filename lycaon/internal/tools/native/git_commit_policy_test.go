package native

import (
	"encoding/json"
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
	paths := []any{"AGENTS.md", "src/AGENTS.md"}
	for _, path := range paths {
		testutil.FailErr(t, "write policy fixture", os.WriteFile(filepath.Join(dir, path.(string)), []byte(content), 0o644))
	}
	boundary := nativefixture.Boundary(t)
	tctx := nativefixture.Context(dir)
	tool := &GitCommitTool{Git: git.NewManager(), Boundary: boundary}
	out, err := tool.Run(t.Context(), map[string]any{"message": "Record project policy", "paths": paths}, tctx)
	testutil.FailErr(t, "commit human policy", err)
	var receipt struct {
		Available bool `json:"available"`
	}
	testutil.FailErr(t, "decode commit receipt", json.Unmarshal([]byte(out), &receipt))
	if !receipt.Available {
		t.Fatalf("policy commit unavailable: %s", out)
	}
	for _, path := range paths {
		if got := gittest.Run(t, dir, "show", "HEAD:"+path.(string)); got != content {
			t.Errorf("committed policy = %q, want %q", got, content)
		}
		write := &WriteTool{Boundary: boundary}
		_, err := write.Run(t.Context(), map[string]any{"path": path, "content": "Agent-authored rules\n"}, tctx)
		if err == nil {
			t.Error("policy write succeeded without review")
		}
		current, err := os.ReadFile(filepath.Join(dir, path.(string)))
		testutil.FailErr(t, "read policy after denied write", err)
		if string(current) != content {
			t.Errorf("policy bytes changed: %q", current)
		}
	}
}
