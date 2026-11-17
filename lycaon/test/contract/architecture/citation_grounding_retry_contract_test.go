package contract

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const surfaceImplementSynthesis = "implement_synthesis"

// sessionReadLedger serves one session's own reads and no worker legs.
type sessionReadLedger struct {
	root      string
	sessionID string
	msgs      []api.Message
}

func (r sessionReadLedger) LoadLedger(_ context.Context, sessionID string) (evidence.Ledger, error) {
	if sessionID != r.sessionID {
		return evidence.Ledger{}, nil
	}
	return ledgertest.BuildFromMessages(r.root, r.msgs), nil
}

func (sessionReadLedger) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

// closeoutGroundingVerdict grounds one closeout draft against a turn that read
// internal/retention.go.
func closeoutGroundingVerdict(t *testing.T, surfaceID, markdown string) guard.CloseoutGroundingVerdict {
	t.Helper()
	root := t.TempDir()
	sess := &api.Session{ID: "parent-1", WorkspacePath: root}
	readJSON := `{"content":"1|package retention\n","path":"internal/retention.go","offset":1,"end_line":1}`
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "research"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", ID: "r1", Args: map[string]any{"path": "internal/retention.go"}}}},
		{Role: api.MessageRoleTool, Content: readJSON, EvidenceHandles: []string{"read#1"}, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	ledger := sessionReadLedger{root: root, sessionID: sess.ID, msgs: history[1:]}
	draft := guidance.ParseCloseoutDraft(markdown, citationmd.Options{})
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(context.Background(), ledger, sess, history, surfaceID, draft, evidence.CitationRoots{ProjectDir: root})
	contractcheck.FailErr(t, "evaluate closeout grounding", err)
	return verdict
}

// Each grounding verdict selects its repair: a citation the model can fix is
// retried in session; a grounded closeout needs none.
func TestCloseoutGroundingVerdictsSelectTheirRetry(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	const (
		grounded   = `Retention surveyed [internal/retention.go:1 "package retention"].`
		phantom    = `A phantom path [phantom.go:1 "package phantom"].`
		unobserved = `Retention surveyed [internal/retention.go:1 "package retention"], per [the doc](https://unobserved.example/doc).`
	)
	for _, tc := range []struct {
		name, surface, markdown, code string
		retry                         bool
	}{
		{"synthesis grounded", surfaceImplementSynthesis, grounded, "", false},
		{"synthesis phantom path", surfaceImplementSynthesis, phantom, guidance.SynthHandleNotInLegsCode, true},
		{"synthesis unobserved url", surfaceImplementSynthesis, unobserved, guidance.SynthURLNotObservedCode, true},
		{"investigate grounded", toolcontract.SurfaceImplementInvestigate, grounded, "", false},
		{"investigate phantom path", toolcontract.SurfaceImplementInvestigate, phantom, guidance.InvestHandleNotObservedCode, true},
		{"investigate unobserved url", toolcontract.SurfaceImplementInvestigate, unobserved, guidance.InvestURLNotObservedCode, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verdict := closeoutGroundingVerdict(t, tc.surface, tc.markdown)
			if verdict.Code != tc.code || verdict.CitationsRequired {
				t.Fatalf("verdict code = %q citations required = %v, want %q", verdict.Code, verdict.CitationsRequired, tc.code)
			}
			if got := cfg.IsInSessionRetry(verdict.Code); got != tc.retry {
				t.Fatalf("in-session retry for %q = %v, want %v", verdict.Code, got, tc.retry)
			}
		})
	}
}

// An answer that cites nothing while the ledger holds citable evidence is
// attached by the host: the verdict warns and never asks the model to retry.
func TestMissingCoordinatorCitationsWarnWithoutModelRetry(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	for surface, code := range map[string]string{
		surfaceImplementSynthesis:                guidance.SynthCitationsRequiredCode,
		toolcontract.SurfaceImplementInvestigate: guidance.InvestCitationsRequiredCode,
	} {
		t.Run(surface, func(t *testing.T) {
			t.Parallel()
			verdict := closeoutGroundingVerdict(t, surface, "## Found retention\n\nUpdated internal/retention.go.")
			if verdict.Code != code || !verdict.CitationsRequired {
				t.Fatalf("verdict = %+v, want %s with citations required", verdict, code)
			}
			if cfg.IsInSessionRetry(verdict.Code) {
				t.Fatalf("%s asks the model to retry", verdict.Code)
			}
			if entry := cfg.HintCodes[verdict.Code]; entry.Effect != "warn" {
				t.Fatalf("%s effect = %q, want warn", verdict.Code, entry.Effect)
			}
		})
	}
}
