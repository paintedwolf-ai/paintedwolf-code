package editordoc

import (
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

func TestObserveAcceptedAgentEditsWEAOwningReadWitnessAndDisabledZeroAlloc(t *testing.T) {
	svc := &AgentService{}
	doc := &Document{
		ID:       "doc-1",
		Path:     "internal/app/service.go",
		Revision: 6,
	}
	doc.authorship = authorshipContext{sessionID: "11111111-2222-3333-4444-555555555555", toolCallID: "call-1"}
	batch := []*agentAcceptance{
		{
			input: AgentEdit{
				ProjectID:        "proj-1",
				DocumentID:       "doc-1",
				ExpectedRevision: 3,
				OperationID:      "op-rebase-1",
				SessionID:        "11111111-2222-3333-4444-555555555555",
				ToolCallID:       "call-1",
			},
			document: doc,
			previous: 5, // Legitimate non-overlapping CRDT rebase from rev 3 -> rev 5 -> rev 6
		},
	}
	results := []*AgentEditResult{{Document: batch[0].document, Saved: true}}

	// 1. Disabled: 0 allocs/op.
	restoreOff := cmddiag.SetObserver(cmddiag.NewObserver(cmddiag.Config{Enabled: false}))
	if svc.observeAcceptedAgentEditsWEA(batch, results) {
		restoreOff()
		t.Fatal("expected false when diagnostics disabled")
	}
	allocs := testing.AllocsPerRun(100, func() {
		_ = svc.observeAcceptedAgentEditsWEA(batch, results)
	})
	restoreOff()
	if allocs != 0 {
		t.Fatalf("disabled observeAcceptedAgentEditsWEA allocated %v allocs/op, want 0", allocs)
	}

	// 2. Enabled with NO owning read recorded -> incomplete (must NOT fabricate qualifying read).
	var mu sync.Mutex
	var findings []cmddiag.Finding
	obs := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 16,
		Now:           func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) },
		Sink: func(f cmddiag.Finding) {
			mu.Lock()
			findings = append(findings, f)
			mu.Unlock()
		},
	})
	restoreOn := cmddiag.SetObserver(obs)
	defer restoreOn()

	if !svc.observeAcceptedAgentEditsWEA(batch, results) {
		t.Fatal("expected enabled observation to enqueue")
	}

	// 3. With real whole-document read recorded in a.agentReads -> 0 read-basis findings on clean CRDT rebase.
	svc.RememberAgentRead("proj-1", "11111111-2222-3333-4444-555555555555", "doc-1", 3)
	batchWithRead := []*agentAcceptance{
		{
			input: AgentEdit{
				ProjectID:        "proj-1",
				DocumentID:       "doc-1",
				ExpectedRevision: 3,
				OperationID:      "op-rebase-with-read",
				SessionID:        "11111111-2222-3333-4444-555555555555",
				ToolCallID:       "call-1",
			},
			document: batch[0].document,
			previous: 5,
		},
	}
	if !svc.observeAcceptedAgentEditsWEA(batchWithRead, results) {
		t.Fatal("expected rebase-with-read observation to enqueue")
	}

	// 4. With only a partial span read recorded -> RuleWEAEditStaleReadBasis contradiction!
	svcPartial := &AgentService{}
	svcPartial.RememberAgentReadSpans("proj-1", "11111111-2222-3333-4444-555555555555", "doc-1", 3, []documentcore.LineSpan{{StartLine: 1, EndLine: 5}})
	batchPartial := []*agentAcceptance{
		{
			input: AgentEdit{
				ProjectID:        "proj-1",
				DocumentID:       "doc-1",
				ExpectedRevision: 3,
				OperationID:      "op-partial-read",
				SessionID:        "11111111-2222-3333-4444-555555555555",
				ToolCallID:       "call-1",
			},
			document: batch[0].document,
			previous: 3,
		},
	}
	if !svcPartial.observeAcceptedAgentEditsWEA(batchPartial, results) {
		t.Fatal("expected partial-read observation to enqueue")
	}
	obs.Close()

	mu.Lock()
	defer mu.Unlock()
	findBy := func(scope string, rule cmddiag.RuleID) (cmddiag.Finding, bool) {
		for _, f := range findings {
			if f.ProgramID == scope && f.RuleID == rule {
				return f, true
			}
		}
		return cmddiag.Finding{}, false
	}

	if f, ok := findBy("op-rebase-1", cmddiag.RuleWEAEditStaleReadBasis); !ok || f.Disposition != cmddiag.DispositionIncomplete {
		t.Fatalf("missing read witness must emit incomplete, got %+v (all=%+v)", f, findings)
	}
	if f, ok := findBy("op-rebase-with-read", cmddiag.RuleWEAEditStaleReadBasis); ok {
		t.Fatalf("clean rebase with real whole read must emit 0 read-basis findings, got %+v", f)
	}
	if f, ok := findBy("op-rebase-with-read", cmddiag.RuleWEAEditWrongAuthor); !ok || f.Disposition != cmddiag.DispositionIncomplete {
		t.Fatalf("missing sourceledger version witness must emit incomplete (never self-confirm), got %+v", f)
	}
	if f, ok := findBy("op-partial-read", cmddiag.RuleWEAEditStaleReadBasis); !ok || f.Disposition != cmddiag.DispositionContradiction {
		t.Fatalf("non-qualifying partial span read must emit contradiction, got %+v", f)
	}
}
