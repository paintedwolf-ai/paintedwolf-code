package editordoc

import (
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

func TestObserveAcceptedAgentEditsWEACleanRebaseAndDisabledZeroAlloc(t *testing.T) {
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
			document: &Document{
				ID:       "doc-1",
				Path:     "internal/app/service.go",
				Revision: 6,
			},
			previous: 5, // Legitimate non-overlapping CRDT rebase from rev 3 -> rev 5 -> rev 6
		},
	}
	results := []*AgentEditResult{{Document: batch[0].document, Saved: true}}

	// Disabled: 0 allocs/op.
	restoreOff := cmddiag.SetObserver(cmddiag.NewObserver(cmddiag.Config{Enabled: false}))
	if observeAcceptedAgentEditsWEA(batch, results) {
		restoreOff()
		t.Fatal("expected false when diagnostics disabled")
	}
	allocs := testing.AllocsPerRun(100, func() {
		_ = observeAcceptedAgentEditsWEA(batch, results)
	})
	restoreOff()
	if allocs != 0 {
		t.Fatalf("disabled observeAcceptedAgentEditsWEA allocated %v allocs/op, want 0", allocs)
	}

	// Enabled: clean CRDT rebase emits 0 findings.
	var mu sync.Mutex
	var findings []cmddiag.Finding
	obs := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 8,
		Now:           func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) },
		Sink: func(f cmddiag.Finding) {
			mu.Lock()
			findings = append(findings, f)
			mu.Unlock()
		},
	})
	restoreOn := cmddiag.SetObserver(obs)
	defer restoreOn()

	if !observeAcceptedAgentEditsWEA(batch, results) {
		t.Fatal("expected enabled observation to enqueue")
	}
	obs.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(findings) != 0 {
		t.Fatalf("clean CRDT rebase must emit 0 findings, got %+v", findings)
	}
}
