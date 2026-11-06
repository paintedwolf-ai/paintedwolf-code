package guard_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// The requirement follows the current intent window, whatever came before it.
func TestCoordinatorEvidenceOptional(t *testing.T) {
	t.Parallel()
	read := api.Message{Role: api.MessageRoleTool, Content: "{}", EvidenceHandles: []string{"read#1"}}
	control := api.Message{Role: api.MessageRoleTool, Content: "{}"}
	user := func(content string) api.Message { return api.Message{Role: api.MessageRoleUser, Content: content} }
	reply := api.Message{Role: api.MessageRoleAssistant, Content: "Reply."}
	for _, tc := range []struct {
		name     string
		history  []api.Message
		ev       guidance.CloseoutEvidence
		optional bool
	}{
		{"AC-CLO-02 a tool-free turn", []api.Message{user("who wins?"), reply}, guidance.CloseoutEvidence{}, true},
		{"a later tool-free turn", []api.Message{user("a"), reply, user("b"), reply}, guidance.CloseoutEvidence{}, true},
		{"AC-CLO-04 a later turn after file work", []api.Message{user("a"), read, reply, user("thanks"), reply}, guidance.CloseoutEvidence{}, true},
		{"a control tool mints no evidence", []api.Message{user("a"), control, reply}, guidance.CloseoutEvidence{}, true},
		{"a current observation", []api.Message{user("a"), read, reply}, guidance.CloseoutEvidence{}, false},
		{"a current worker leg", []api.Message{user("a"), reply}, guidance.CloseoutEvidence{LegObservations: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := guard.CoordinatorEvidenceOptional(tc.history, tc.ev); got != tc.optional {
				t.Fatalf("optional = %v want %v", got, tc.optional)
			}
		})
	}
}

type failingLedger struct{ investigateLedgerReader }

func (failingLedger) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, errLedgerUnavailable
}

var errLedgerUnavailable = context.DeadlineExceeded

// A ledger failure is an error, never an empty ledger that exempts the closeout.
func TestEvaluateCoordinatorCloseoutGrounding_ledgerErrorIsNoExemption(t *testing.T) {
	t.Parallel()
	sess := &api.Session{ID: "parent-1"}
	history := []api.Message{{Role: api.MessageRoleUser, Content: "go"}}
	_, err := guard.EvaluateCoordinatorCloseoutGrounding(context.Background(), failingLedger{}, sess, history,
		toolcontract.SurfaceImplementInvestigate, markdownDraft("An answer."), evidence.CitationRoots{})
	if err == nil {
		t.Fatal("expected the ledger error")
	}
}

// A prose-only closeout with current evidence signals citations required; the
// assembler mints its grounding, not the guard.
func TestEvaluateCoordinatorCloseoutGrounding_uncitedAnswerReturnsCitationsRequired(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sess := &api.Session{ID: "parent-1", WorkspacePath: root}
	readJSON := `{"content":"1|package foo\n","path":"internal/foo.go","offset":1,"end_line":1}`
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "research"},
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "read", ID: "r1", Args: map[string]any{"path": "internal/foo.go"}}},
		},
		{Role: api.MessageRoleTool, Content: readJSON, EvidenceHandles: []string{"read#1"}, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	ledger := investigateLedgerReader{root: root, sessionID: sess.ID, msgs: history[1:]}
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, toolcontract.SurfaceImplementInvestigate, markdownDraft("## Found foo\n\nUpdated internal/foo.go."), evidence.CitationRoots{ProjectDir: root},
	)
	testutil.FailErr(t, "guard.EvaluateCoordinatorCloseoutGrounding failed", err)
	if verdict.Code != guidance.InvestCitationsRequiredCode || !verdict.CitationsRequired {
		t.Fatalf("verdict = %+v", verdict)
	}
	if verdict.Grounding != nil {
		t.Fatalf("grounding=%+v want nil (guard signals; emitAssembledCloseout mints grounding)", verdict.Grounding)
	}
}

// AC-CLO-03: exempt turns still validate what they supply.
func TestEvaluateCoordinatorCloseoutGrounding_validatesSuppliedReferencesOnExemptTurns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sess := &api.Session{ID: "parent-1", WorkspacePath: root}
	history := []api.Message{{Role: api.MessageRoleUser, Content: "who wins?"}}
	for _, tc := range []struct{ name, content, code string }{
		{"fabricated handle", "No preference [read#9:1].", guidance.InvestHandleNotObservedCode},
		{"fabricated path", `No preference [phantom.go:1 "x"].`, guidance.InvestHandleNotObservedCode},
		{"unobserved link", "No preference, per [a page](https://example.test/x).", guidance.InvestURLNotObservedCode},
		{"artifact never produced", "No preference.\n[artifact:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee]", guidance.InvestHandleNotObservedCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(context.Background(), nil, sess, history,
				toolcontract.SurfaceImplementInvestigate, markdownDraft(tc.content), evidence.CitationRoots{ProjectDir: root})
			testutil.FailErr(t, "guard.EvaluateCoordinatorCloseoutGrounding failed", err)
			if verdict.Code != tc.code {
				t.Fatalf("code=%q want %q", verdict.Code, tc.code)
			}
			if verdict.Grounding == nil || verdict.Grounding.Traced {
				t.Fatalf("grounding=%+v want untraced", verdict.Grounding)
			}
		})
	}
}

// AC-CLO-02: an exempt reply is grounded, and honestly not observed-source success.
func TestEvaluateCoordinatorCloseoutGrounding_exemptReplyIsEvidenceOptional(t *testing.T) {
	t.Parallel()
	sess := &api.Session{ID: "parent-1"}
	history := []api.Message{{Role: api.MessageRoleUser, Content: "who wins?"}}
	// No roots at all: the grounding stays non-nil.
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(context.Background(), nil, sess, history,
		toolcontract.SurfaceImplementInvestigate, markdownDraft("No preference.\n[verify: inspection - Nothing to run]"), evidence.CitationRoots{})
	testutil.FailErr(t, "guard.EvaluateCoordinatorCloseoutGrounding failed", err)
	if strings.TrimSpace(verdict.Code) != "" {
		t.Fatalf("code=%q want pass", verdict.Code)
	}
	g := verdict.Grounding
	if g == nil || !g.Traced || !g.EvidenceOptional {
		t.Fatalf("grounding=%+v want explicit evidence-optional", g)
	}
	if g.Verification == nil || g.Verification.Method != "inspection" {
		t.Fatalf("verification = %+v", g.Verification)
	}
	if len(g.CitedEvidence) != 0 || len(g.EvidenceRecords) != 0 {
		t.Fatalf("an exempt reply names no evidence: %+v", g)
	}
	for _, check := range g.Checks {
		if !check.Vacuous {
			t.Fatalf("check %+v claims work it did not do", check)
		}
	}
}

// A valid citation on an exempt turn is ordinary traced grounding.
func TestEvaluateCoordinatorCloseoutGrounding_validCitationOnExemptTurnIsTraced(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sess := &api.Session{ID: "parent-1", WorkspacePath: root}
	readJSON := `{"content":"1|package foo\n","path":"internal/foo.go","offset":1,"end_line":1}`
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "research"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read", ID: "r1", Args: map[string]any{"path": "internal/foo.go"}}}},
		{Role: api.MessageRoleTool, Content: readJSON, EvidenceHandles: []string{"read#1"}, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
		{Role: api.MessageRoleAssistant, Content: "Found foo."},
		{Role: api.MessageRoleUser, Content: "say more"},
	}
	ledger := investigateLedgerReader{root: root, sessionID: sess.ID, msgs: history[1:3]}
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(context.Background(), ledger, sess, history,
		toolcontract.SurfaceImplementInvestigate, markdownDraft("Still foo [read#1:1]."), evidence.CitationRoots{ProjectDir: root})
	testutil.FailErr(t, "guard.EvaluateCoordinatorCloseoutGrounding failed", err)
	if verdict.Code != "" || verdict.Grounding == nil || !verdict.Grounding.Traced || verdict.Grounding.EvidenceOptional {
		t.Fatalf("verdict = %+v", verdict)
	}
}

func TestEvaluateCoordinatorCloseoutGrounding_rejectsPhantomCitationOnLaterNoToolTurn(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sess := &api.Session{ID: "parent-1", WorkspacePath: root}
	readJSON := `{"content":"1|package foo\n","path":"internal/foo.go","offset":1,"end_line":1}`
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "research"},
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "read", ID: "r1", Args: map[string]any{"path": "internal/foo.go"}}},
		},
		{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
		{Role: api.MessageRoleAssistant, Content: "Found foo."},
		{Role: api.MessageRoleUser, Content: "say more"},
	}
	ledger := investigateLedgerReader{root: root, sessionID: sess.ID, msgs: history[1:3]}
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, toolcontract.SurfaceImplementInvestigate, markdownDraft(`Still about foo [phantom.go:1 "package phantom"].`), evidence.CitationRoots{ProjectDir: root},
	)
	testutil.FailErr(t, "guard.EvaluateCoordinatorCloseoutGrounding failed", err)
	if strings.TrimSpace(verdict.Code) == "" {
		t.Fatal("expected grounding reject on later no-tool turn with phantom citation")
	}
	if verdict.Grounding != nil && verdict.Grounding.Traced {
		t.Fatalf("grounding=%+v want not traced", verdict.Grounding)
	}
}
