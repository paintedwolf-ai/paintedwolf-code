package survey

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/llm"
)

func TestResolveSynthesisCurateTaskUsesDelegationBrief(t *testing.T) {
	task := resolveSynthesisCurateTask(" prove auth middleware wiring ", evidence.InitLedger(), "")
	if task != "prove auth middleware wiring" {
		t.Fatalf("task = %q", task)
	}
}

func TestResolveSynthesisCurateTaskDefaultFromObjectives(t *testing.T) {
	snapshot := evidence.AssembleLedger([]evidence.Record{
		{
			Handle: "worker-a-objective-1",
			Kind:   "worker_report",
			Body:   []string{"objective: trace login handler"},
		},
	})
	task := resolveSynthesisCurateTask("", snapshot, "investigate")
	if !strings.Contains(task, "trace login handler") {
		t.Fatalf("task = %q", task)
	}
	if strings.Contains(task, "user prompt") {
		t.Fatalf("task must not leak user prompt: %q", task)
	}
}

func TestResolveSynthesisCurateTaskDefaultFallback(t *testing.T) {
	task := resolveSynthesisCurateTask("", evidence.InitLedger(), "")
	if !strings.HasPrefix(task, "synthesis: rank worker evidence for investigate closeout") {
		t.Fatalf("task = %q", task)
	}
}

func TestRenderEvidenceDigest(t *testing.T) {
	digest := RenderEvidenceDigest(llm.CurationResult{
		Selections: []llm.MaterializedSelection{{
			Triple:     evidence.Triple{Path: "main.go", Line: 12, Excerpt: "func main()"},
			Resolution: evidence.Resolution{Handle: "grep#1", Path: "main.go", Line: 12},
			Lines:      []string{"func main()"},
		}},
		Gloss:  []llm.GlossLine{{Label: "start at main"}},
		Report: llm.CurationReport{Selected: 1, Total: 10},
	}, []string{"scout/job-1"})
	if !strings.Contains(digest, "## Evidence digest (curated)") {
		t.Fatalf("missing header: %q", digest)
	}
	if !strings.Contains(digest, "[grep#1] main.go:12") {
		t.Fatalf("missing highlight: %q", digest)
	}
	if !strings.Contains(digest, "Navigation: start at main") {
		t.Fatalf("missing gloss: %q", digest)
	}
	if !strings.Contains(digest, "selected/total: 1/10") {
		t.Fatalf("missing report: %q", digest)
	}
}

func TestTruncateTail(t *testing.T) {
	got := TruncateTail("0123456789", 4)
	if got != "…(truncated)\n6789" {
		t.Fatalf("TruncateTail = %q", got)
	}
}
