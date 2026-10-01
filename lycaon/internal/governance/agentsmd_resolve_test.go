package governance_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

// agentsMDFixture materializes a testdata tree into a temp copy, renaming the
// overlay placeholder to whatever this build calls it. Tests then read the same
// directory the resolver looks in.
func agentsMDFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", "agentsmd", name)
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		for i, p := range parts {
			if p == testutil.OverlayFixtureDirName {
				parts[i] = settingsoverlay.DirName()
			}
		}
		target := filepath.Join(dst, filepath.Join(parts...))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
	testutil.FailErr(t, "materialize agentsmd fixture "+name, err)
	return dst
}

func chainPaths(chain []governance.ResolvedAgentsMD) []string {
	out := make([]string, len(chain))
	for i, item := range chain {
		out[i] = item.Path
	}
	return out
}

func assertPathSetEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	gm := make(map[string]int, len(got))
	for _, p := range got {
		gm[p]++
	}
	wm := make(map[string]int, len(want))
	for _, p := range want {
		wm[p]++
	}
	if len(gm) != len(wm) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	for k, n := range wm {
		if gm[k] != n {
			t.Fatalf("%s: got %v, want %v", label, got, want)
		}
	}
}

func TestListIndexSkipsHiddenDirectories(t *testing.T) {
	root := t.TempDir()
	writeAgentsMD(t, root, "AGENTS.md", "# Root\n")
	writeAgentsMD(t, root, "pkg/AGENTS.md", "# Package\n")
	writeAgentsMD(t, root, ".claude/worktrees/wt/AGENTS.md", "# Worktree copy\n")

	index, err := governance.ListIndex(context.Background(), root)
	testutil.FailErr(t, "ListIndex", err)
	got := chainPaths(index)
	want := []string{"AGENTS.md", "pkg/AGENTS.md"}
	assertPathSetEqual(t, "index paths", got, want)
}

func TestResolveChainAppliesHiddenTreeWhenWorkingThere(t *testing.T) {
	root := t.TempDir()
	writeAgentsMD(t, root, "AGENTS.md", "# Root\n")
	writeAgentsMD(t, root, ".claude/worktrees/wt/AGENTS.md", "# Worktree policy\n")

	chain, err := governance.ResolveChain(root, ".claude/worktrees/wt/main.go")
	testutil.FailErr(t, "ResolveChain", err)
	got := chainPaths(chain)
	want := []string{"AGENTS.md", ".claude/worktrees/wt/AGENTS.md"}
	assertPathSetEqual(t, "chain paths", got, want)
}

func TestListIndexOmitsTestdataAndNodeModules(t *testing.T) {
	root := t.TempDir()
	writeAgentsMD(t, root, "AGENTS.md", "# Root\n")
	writeAgentsMD(t, root, "pkg/AGENTS.md", "# Package\n")
	writeAgentsMD(t, root, "internal/testdata/agentsmd/monorepo/AGENTS.md", "# Fixture\n")
	writeAgentsMD(t, root, "node_modules/pkg/AGENTS.md", "# Dependency\n")

	index, err := governance.ListIndex(context.Background(), root)
	testutil.FailErr(t, "ListIndex", err)
	got := chainPaths(index)
	want := []string{"AGENTS.md", "pkg/AGENTS.md"}
	assertPathSetEqual(t, "index paths", got, want)
}

func TestResolveChainAppliesTestdataWhenWorkingThere(t *testing.T) {
	root := t.TempDir()
	writeAgentsMD(t, root, "AGENTS.md", "# Root\n")
	writeAgentsMD(t, root, "internal/testdata/agentsmd/monorepo/AGENTS.md", "# Fixture policy\n")

	chain, err := governance.ResolveChain(root, "internal/testdata/agentsmd/monorepo/x.go")
	testutil.FailErr(t, "ResolveChain", err)
	got := chainPaths(chain)
	want := []string{"AGENTS.md", "internal/testdata/agentsmd/monorepo/AGENTS.md"}
	assertPathSetEqual(t, "chain paths", got, want)
}

func writeAgentsMD(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		testutil.FailErr(t, "MkdirAll", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		testutil.FailErr(t, "WriteFile", err)
	}
}

func TestResolveChainRootOnly(t *testing.T) {
	root := agentsMDFixture(t, "monorepo")
	chain, err := governance.ResolveChain(root, "lycaon/foo.go")
	testutil.FailErr(t, "resolve chain root only", err)
	got := chainPaths(chain)
	want := []string{"AGENTS.md", "lycaon/AGENTS.md"}
	assertPathSetEqual(t, "chain paths", got, want)
}

func TestResolveChainNestedAccumulation(t *testing.T) {
	root := agentsMDFixture(t, "monorepo")
	chain, err := governance.ResolveChain(root, "lycaon/internal/x.go")
	testutil.FailErr(t, "resolve nested chain", err)
	got := chainPaths(chain)
	want := []string{"AGENTS.md", "lycaon/AGENTS.md"}
	assertPathSetEqual(t, "chain paths", got, want)
}

func TestResolveChainJurisdictionExcludesSibling(t *testing.T) {
	root := agentsMDFixture(t, "monorepo")
	chain, err := governance.ResolveChain(root, "lycaon/internal/x.go")
	testutil.FailErr(t, "resolve jurisdiction chain", err)
	for _, item := range chain {
		if item.Path == "lycaon-den/AGENTS.md" {
			t.Fatalf("sibling package AGENTS.md must not apply: %v", chainPaths(chain))
		}
	}
}

func TestResolveChainLycaonOverlay(t *testing.T) {
	root := agentsMDFixture(t, "monorepo_lycaon")
	chain, err := governance.ResolveChain(root, "README.md")
	testutil.FailErr(t, "resolve overlay chain", err)
	got := chainPaths(chain)
	want := []string{"AGENTS.md", settingsoverlay.Rel("AGENTS.md")}
	assertPathSetEqual(t, "chain paths", got, want)
}

func TestResolveChainMissingFilesSkipped(t *testing.T) {
	root := agentsMDFixture(t, "monorepo")
	chain, err := governance.ResolveChain(root, "missing/deep/file.go")
	testutil.FailErr(t, "resolve chain with missing intermediates", err)
	if len(chain) == 0 {
		t.Fatal("expected root AGENTS.md in chain")
	}
	if chain[0].Path != "AGENTS.md" {
		t.Fatalf("first path = %q, want AGENTS.md", chain[0].Path)
	}
}

func TestListIndexFindsAll(t *testing.T) {
	root := agentsMDFixture(t, "monorepo")
	index, err := governance.ListIndex(context.Background(), root)
	testutil.FailErr(t, "list index", err)
	got := chainPaths(index)
	want := []string{"AGENTS.md", "lycaon-den/AGENTS.md", "lycaon/AGENTS.md"}
	assertPathSetEqual(t, "index paths", got, want)
	for _, item := range index {
		if item.Content != "" {
			t.Fatalf("index entry %q must not load content", item.Path)
		}
	}
}

func TestResolveChainPreservesFrontmatterAsMarkdown(t *testing.T) {
	root := t.TempDir()
	body := "---\ndescription: ordinary markdown\n---\n# Policy\n"
	writeAgentsMD(t, root, "AGENTS.md", body)

	chain, err := governance.ResolveChain(root, "README.md")
	testutil.FailErr(t, "resolve chain", err)
	if len(chain) != 1 {
		t.Fatalf("chain len = %d, want 1", len(chain))
	}
	if chain[0].Content != body {
		t.Fatalf("content = %q want %q", chain[0].Content, body)
	}
}

func TestListIndexSkipsSymlinkedAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside policy"), 0o644); err != nil {
		testutil.FailErr(t, "write outside", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	index, err := governance.ListIndex(context.Background(), root)
	testutil.FailErr(t, "ListIndex", err)
	if len(index) != 0 {
		t.Fatalf("symlinked AGENTS.md must not be indexed: %+v", index)
	}
	chain, err := governance.ResolveChain(root, "README.md")
	testutil.FailErr(t, "ResolveChain", err)
	if len(chain) != 0 {
		t.Fatalf("symlinked AGENTS.md must not be loaded: %+v", chain)
	}
}

func TestResolveChainReadsFullBodyUnderStructuralCeiling(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("x", governance.DefaultAgentsMDInjectMaxBodyBytes*2)
	writeAgentsMD(t, root, "AGENTS.md", body)

	chain, err := governance.ResolveChain(root, "README.md")
	testutil.FailErr(t, "ResolveChain", err)
	if len(chain) != 1 {
		t.Fatalf("chain len = %d, want 1", len(chain))
	}
	// A heading digest needs the whole document, not just the inject budget's
	// worth of it, so a body several times over that budget still reads in full.
	if got, want := len(chain[0].Content), len(body); got != want {
		t.Fatalf("body read %d bytes, want the full %d-byte body", got, want)
	}
}

func TestResolveChainBoundsRepositoryReadAtStructuralCeiling(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("x", governance.AgentsMDReadMaxBytes*2)
	writeAgentsMD(t, root, "AGENTS.md", body)

	chain, err := governance.ResolveChain(root, "README.md")
	testutil.FailErr(t, "ResolveChain", err)
	if len(chain) != 1 {
		t.Fatalf("chain len = %d, want 1", len(chain))
	}
	if got, max := len(chain[0].Content), governance.AgentsMDReadMaxBytes+1; got > max {
		t.Fatalf("body read %d bytes, want at most %d", got, max)
	}
}

func TestResolveChainTreatsExistingScopeDirectoryAsWorkUnderIt(t *testing.T) {
	root := t.TempDir()
	writeAgentsMD(t, root, "AGENTS.md", "root\n")
	writeAgentsMD(t, root, "pkg/AGENTS.md", "package\n")

	chain, err := governance.ResolveChain(root, "pkg")
	testutil.FailErr(t, "ResolveChain", err)
	got := chainPaths(chain)
	want := []string{"AGENTS.md", "pkg/AGENTS.md"}
	assertPathSetEqual(t, "directory scope chain", got, want)
}
func TestResolveChainPrecedenceOrderNearestLast(t *testing.T) {
	root := agentsMDFixture(t, "monorepo")
	chain, err := governance.ResolveChain(root, "lycaon/foo.go")
	testutil.FailErr(t, "resolve precedence chain", err)
	if len(chain) < 2 {
		t.Fatal("expected at least two entries")
	}
	if chain[len(chain)-1].Path != "lycaon/AGENTS.md" {
		t.Fatalf("nearest last = %q, want lycaon/AGENTS.md", chain[len(chain)-1].Path)
	}
}

func TestResolveChainEscapesRoot(t *testing.T) {
	root := agentsMDFixture(t, "monorepo")
	_, err := governance.ResolveChain(root, "../outside.go")
	if err == nil {
		t.Fatal("expected escape error")
	}
}
