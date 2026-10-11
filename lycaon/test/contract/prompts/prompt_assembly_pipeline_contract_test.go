package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestPromptLoopNoDuplicateAssemblyHooks keeps promptloop outside assembly steps.
// It may only re-run DeterministicFit after system prompts land.
func TestPromptLoopNoDuplicateAssemblyHooks(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	forbidden := []string{
		"AssemblePromptHistory",
		"EvictSupersededReads",
		"PruneToolOutputs",
		"TrimSummarizeChunk",
	}
	dir := filepath.Join(root, "lycaon", "internal", "coordinator", "promptloop")
	var violations []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		body := string(raw)
		for _, tok := range forbidden {
			if strings.Contains(body, tok) {
				violations = append(violations, path+": "+tok)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("promptloop must not duplicate assembly steps; use one pipeline entry:\n%s", strings.Join(violations, "\n"))
	}
}

func TestAssemblePromptHistoryDoesNotRewriteToolBodies(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	// The session entry point and the promptassembly pipeline it delegates to.
	paths := []string{filepath.Join(root, "lycaon", "internal", "session", "history", "prompt_assembly.go")}
	pipeline, err := filepath.Glob(filepath.Join(root, "lycaon", "internal", "session", "promptassembly", "*.go"))
	if err != nil || len(pipeline) == 0 {
		t.Fatalf("glob promptassembly sources: %v", err)
	}
	for _, path := range append(paths, pipeline...) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(raw)
		for _, tok := range []string{
			"DietAgedSummarizeMessages",
			"PruneToolOutputs",
			"CompactOversizedChunksOnly",
			"PruneHistoryDedupLedger",
		} {
			if strings.Contains(body, tok) {
				t.Fatalf("%s must not rewrite committed tool bodies via %q", path, tok)
			}
		}
	}
}

func TestAssemblePromptHistoryEntryPoints(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	checks := []struct {
		path string
		want string
	}{
		{filepath.Join(root, "lycaon", "internal", "session", "history", "compaction.go"), "m.Assemble(ctx, sess, history, surfaceID)"},
		{filepath.Join(root, "lycaon", "internal", "session", "history", "prompt_assembly.go"), "promptassembly.Assemble("},
		{filepath.Join(root, "lycaon", "internal", "session", "promptassembly", "assembly.go"), "FilterPromptHistory"},
		{filepath.Join(root, "lycaon", "internal", "session", "promptassembly", "assembly.go"), "restoreSealedToolBodies"},
		{filepath.Join(root, "lycaon", "internal", "session", "promptassembly", "assembly.go"), "sealToolRoleBodies"},
		{filepath.Join(root, "lycaon", "internal", "llm", "compaction", "summarize_chunk.go"), "TrimSummarizeChunk"},
	}
	for _, c := range checks {
		raw, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatalf("read %s: %v", c.path, err)
		}
		if !strings.Contains(string(raw), c.want) {
			t.Fatalf("%s must reference %q", c.path, c.want)
		}
	}
}
