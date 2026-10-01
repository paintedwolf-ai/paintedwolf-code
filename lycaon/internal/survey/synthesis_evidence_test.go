package survey

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
)

type countingCurator struct {
	calls int
}

func (c *countingCurator) Curate(_ context.Context, _ evidence.Ledger, _ llm.CurationFocus, _ int) (llm.CurationResult, error) {
	c.calls++
	return llm.CurationResult{
		Selections: []llm.MaterializedSelection{{
			Triple:     evidence.Triple{Path: "a.go", Line: 1, Excerpt: "x"},
			Resolution: evidence.Resolution{Handle: "grep#1", Path: "a.go", Line: 1},
			Lines:      []string{"x"},
		}},
		Report: llm.CurationReport{Selected: 1, Total: 1},
	}, nil
}

type stubLedgerReader struct {
	ledgers map[string]evidence.Ledger
}

func (s stubLedgerReader) LoadLedger(_ context.Context, sessionID string) (evidence.Ledger, error) {
	if s.ledgers == nil {
		return evidence.InitLedger(), nil
	}
	return s.ledgers[sessionID], nil
}

func TestMaybeCurateSynthesisEvidencePassthroughUnderBudget(t *testing.T) {
	cur := &countingCurator{}
	out, err := MaybeCurateSynthesisEvidence(EvidenceInput{
		Ctx:             context.Background(),
		ParentSessionID: "parent",
		Envelopes: []WorkerEnvelope{{
			JobID:     "j1",
			AgentType: "scout",
			Body:      strings.Repeat("x", 100),
			Report:    WorkerReportSnapshot{LegStatus: "complete"},
		}},
		MergedBytes: 100,
		WorkerCount: 1,
		Reader:      stubLedgerReader{},
		Curator:     cur,
		AllowCurate: true,
	})
	testutil.FailErr(t, "MaybeCurateSynthesisEvidence failed", err)
	if out.Curated || out.EvidenceDigest != "" {
		t.Fatalf("expected passthrough: %+v", out)
	}
	if cur.calls != 0 {
		t.Fatalf("Curate calls = %d want 0", cur.calls)
	}
}

func TestMaybeCurateSynthesisEvidenceOneCurateOverBudget(t *testing.T) {
	cur := &countingCurator{}
	block := strings.Repeat("x", SynthesisEvidenceBudget+1)
	out, err := MaybeCurateSynthesisEvidence(EvidenceInput{
		Ctx:             context.Background(),
		ParentSessionID: "parent",
		Envelopes: []WorkerEnvelope{{
			JobID:     "j1",
			AgentType: "scout",
			Body:      block,
			Report:    WorkerReportSnapshot{LegStatus: "complete", ObjectivesMet: []string{"mapped routes"}},
		}},
		MergedBytes:    len(block),
		WorkerCount:    1,
		Reader:         stubLedgerReader{},
		Curator:        cur,
		AllowCurate:    true,
		TopologyOutput: block,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Curated || !strings.Contains(out.EvidenceDigest, "Evidence digest (curated)") {
		t.Fatalf("expected digest: %+v", out)
	}
	if cur.calls != 1 {
		t.Fatalf("Curate calls = %d want 1", cur.calls)
	}
	if len(out.TopologyOutput) > SynthesisTopologyTailBytes+32 {
		t.Fatalf("topology tail not truncated: len=%d", len(out.TopologyOutput))
	}
}

func TestBuildSynthesisSnapshotSurveyOnlyChildLedger(t *testing.T) {
	child := evidence.AssembleLedger([]evidence.Record{
		{Handle: "grep#1", Kind: "grep", Survey: true, Path: "a.go", Body: []string{"line"}},
		{Handle: "read#1", Kind: "read", Survey: false, Path: "b.go", Body: []string{"literal"}},
	})
	reader := stubLedgerReader{ledgers: map[string]evidence.Ledger{"child-1": child}}
	snapshot, stats, err := BuildSynthesisSnapshot(context.Background(), SnapshotInput{
		Envelopes: []WorkerEnvelope{{
			JobID:          "j1",
			ChildSessionID: "child-1",
			AgentType:      "scout",
			Report:         WorkerReportSnapshot{LegStatus: "complete"},
		}},
		MergedBytes: 100,
		WorkerCount: 1,
		Reader:      reader,
	})
	testutil.FailErr(t, "BuildSynthesisSnapshot failed", err)
	if stats.WorkerCount != 1 {
		t.Fatalf("worker count = %d", stats.WorkerCount)
	}
	var grepSeen, literalRead bool
	for _, rec := range snapshot.Handles {
		if rec.Kind == "grep" && rec.Survey {
			grepSeen = true
		}
		if rec.Kind == "read" {
			literalRead = true
		}
	}
	if !grepSeen {
		t.Fatal("expected survey grep handle in snapshot")
	}
	if literalRead {
		t.Fatal("literal read must not enter synthesis snapshot")
	}
}

var _ guidance.EvidenceLedgerReader = stubLedgerReader{}
