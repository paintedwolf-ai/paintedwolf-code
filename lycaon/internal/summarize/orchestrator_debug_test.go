package summarize

import (
	"context"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/observability"
)

func TestRunDebugEnabledWritesCapture(t *testing.T) {
	observability.CloseSummarizeDebug()
	dir := t.TempDir()
	logPath := dir + "/summarize-debug.jsonl"
	t.Setenv("LYCAON_SUMMARIZE_DEBUG", "1")
	t.Setenv("LYCAON_SUMMARIZE_DEBUG_FILE", logPath)

	body := "Cross-link [tool feedback](docs/agent-tool-feedback.md)."
	cands := []Candidate{{
		RelPath: "docs/summarize-tool.md", Kind: KindFile,
		ContentHash: "abc", Body: body,
	}}
	eng := NewEngine(fakeGather{GatherResult{
		Mode: ModeRepo, Candidates: cands,
		Stats: GatherStats{Mode: ModeRepo, Candidates: 1, PathIsFile: true},
	}}, DefaultCaps())

	if _, err := eng.Run(context.Background(), Request{Task: "explain summarize", MaxAnchors: 12}); err != nil {
		t.Fatalf("run: %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read debug log: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("expected debug JSONL rows")
	}
}
