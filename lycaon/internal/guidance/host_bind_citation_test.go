package guidance_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

type readSpec struct {
	path string
	line int
	text string
}

// bindLedger builds a read-only evidence ledger from one or more single-line reads.
func bindLedger(specs ...readSpec) evidence.Ledger {
	var msgs []api.Message
	for i, s := range specs {
		content := fmt.Sprintf(`{"path":%q,"content":%q,"offset":%d,"end_line":%d,"limit":1}`,
			s.path, fmt.Sprintf("%d|%s", s.line, s.text), s.line, s.line)
		msgs = append(msgs,
			api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
				{Name: "read", ID: fmt.Sprintf("c%d", i), Args: map[string]any{"path": s.path, "offset": s.line, "limit": 1}},
			}},
			api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: content}},
		)
	}
	return ledgertest.BuildFromMessages("", msgs)
}

// A loose worker citation (stale handle) whose excerpt matches exactly one ledger
// record binds: the report lands (no reject Code) with a bind advisory.
func TestBindArm_UniqueMatchBindsWorkerCitation(t *testing.T) {
	ev := bindLedger(readSpec{"app.ts", 12, "func NewBoard() {"})
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Evidence: "grep#9", Excerpt: "func NewBoard() {"},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != "" {
		t.Fatalf("eval.Code = %q, want bound (no reject)", eval.Code)
	}
	if eval.BindAdvisoryCount != 1 {
		t.Fatalf("BindAdvisoryCount = %d, want 1 (advisories=%v)", eval.BindAdvisoryCount, eval.BindAdvisoryTokens)
	}
	if len(eval.Resolutions) != 1 || eval.Resolutions[0].Verdict != evidence.VerdictBound {
		t.Fatalf("resolutions = %+v, want one bound", eval.Resolutions)
	}
	if got := eval.BindAdvisoryTokens[0]; !strings.Contains(got, "→ read#1") {
		t.Fatalf("advisory = %q, want bind to read#1", got)
	}
}

// An excerpt present in more than one record is ambiguous — the host never silently
// picks one, so the citation still rejects.
func TestBindArm_AmbiguousStillRejects(t *testing.T) {
	ev := bindLedger(
		readSpec{"a.go", 5, "return errNotFound"},
		readSpec{"b.go", 8, "return errNotFound"},
	)
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Evidence: "grep#9", Excerpt: "return errNotFound"},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != guidance.WorkerEvidenceHandleUnknownCode {
		t.Fatalf("eval.Code = %q, want reject on ambiguous match", eval.Code)
	}
	if eval.BindAdvisoryCount != 0 {
		t.Fatalf("BindAdvisoryCount = %d, want 0 on reject", eval.BindAdvisoryCount)
	}
}

// Zero candidates leaves the exact resolver's reject in place.
func TestBindArm_ZeroCandidatesStillRejects(t *testing.T) {
	ev := bindLedger(readSpec{"app.ts", 12, "func NewBoard() {"})
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Evidence: "grep#9", Excerpt: "this text is nowhere in the ledger"},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != guidance.WorkerEvidenceHandleUnknownCode {
		t.Fatalf("eval.Code = %q, want reject on zero candidates", eval.Code)
	}
}

// An excerpt that is not a verbatim substring of any record body never binds, even
// when it shares words with a real record.
func TestBindArm_ExcerptNotInBodyNeverBinds(t *testing.T) {
	ev := bindLedger(readSpec{"app.ts", 12, "func NewBoard() {"})
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Evidence: "grep#9", Excerpt: "func DestroyBoard() {"},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != guidance.WorkerEvidenceHandleUnknownCode {
		t.Fatalf("eval.Code = %q, want reject when excerpt not verbatim in body", eval.Code)
	}
}

// A sub-meaningful-span excerpt never binds (triviality floor).
func TestBindArm_ShortExcerptNeverBinds(t *testing.T) {
	ev := bindLedger(readSpec{"app.ts", 12, "func NewBoard() {"})
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{}, []guidance.WorkerFindingInput{
		{Evidence: "grep#9", Excerpt: "func"},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != guidance.WorkerEvidenceHandleUnknownCode {
		t.Fatalf("eval.Code = %q, want reject on trivial excerpt", eval.Code)
	}
}
