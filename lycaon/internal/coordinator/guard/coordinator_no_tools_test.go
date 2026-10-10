package guard_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoordinatorEvidenceOptional(t *testing.T) {
	t.Parallel()

	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "who wins?"},
		{Role: api.MessageRoleAssistant, Content: "thinking"},
	}
	if !guard.CoordinatorEvidenceOptional(history, nil) {
		t.Fatal("expected optional on first turn with no tools")
	}
	if guard.CoordinatorEvidenceOptional(history, []string{"read"}) {
		t.Fatal("expected required when turn ran tools")
	}
	withTool := append(append([]api.Message(nil), history...), api.Message{Role: api.MessageRoleTool, Content: "{}"})
	if guard.CoordinatorEvidenceOptional(withTool, nil) {
		t.Fatal("expected required when tool message exists since user turn")
	}

	laterTurn := []api.Message{
		{Role: api.MessageRoleUser, Content: "who wins?"},
		{Role: api.MessageRoleAssistant, Content: "No preference."},
		{Role: api.MessageRoleUser, Content: "and again?"},
		{Role: api.MessageRoleAssistant, Content: "Still none."},
	}
	if guard.CoordinatorEvidenceOptional(laterTurn, nil) {
		t.Fatal("expected required on later turn even with no tools this turn")
	}

	laterWithInternal := []api.Message{
		{Role: api.MessageRoleUser, Content: "who wins?"},
		{Role: api.MessageRoleAssistant, Content: "No preference."},
		{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Kind: api.MessageKindHostLoopWake, Visibility: api.MessageVisibilityInternal},
		{Role: api.MessageRoleUser, Content: "and again?"},
	}
	if guard.CoordinatorEvidenceOptional(laterWithInternal, nil) {
		t.Fatal("expected required on later turn; internal rows must not reset first-turn")
	}
}

// Plain Markdown is a report without fields; a well-formed envelope and one
// with trailing junk after its first value both read as that envelope.
func TestPrepareCoordinatorCloseoutReadsEveryForm(t *testing.T) {
	t.Parallel()

	prepared, read, ok := guard.PrepareCoordinatorCloseoutContent(toolcontract.SurfaceImplementInvestigate, "Plain answer.", "")
	report, parsed := guidance.ParseCoordinatorCompletionReport(prepared)
	if !ok || !parsed || report.Synthesis != "Plain answer." || len(read.Unread) != 0 {
		t.Fatalf("prose: prepared=%q read=%+v", prepared, read)
	}
	if len(report.CitedEvidence) != 0 || len(report.CitedURLs) != 0 {
		t.Fatalf("expected empty citations, got evidence=%v urls=%v", report.CitedEvidence, report.CitedURLs)
	}
	malformed := `{"synthesis":"## Report\n\nDone."}, "cited_evidence":[],"cited_urls":[],"artifact_ids":[]}`
	prepared, _, ok = guard.PrepareCoordinatorCloseoutContent(toolcontract.SurfaceImplementInvestigate, malformed, "")
	report, parsed = guidance.ParseCoordinatorCompletionReport(prepared)
	if !ok || !parsed || report.Synthesis != "## Report\n\nDone." {
		t.Fatalf("salvaged envelope synthesis=%q parsed=%v", report.Synthesis, parsed)
	}
}

// Markdown with a trailing fence of report fields is one report. Fence
// members the report does not take are named, and the rest are kept; a
// trailing JSON block with no report member is part of the answer.
func TestPrepareCoordinatorCloseoutProseTrailer(t *testing.T) {
	t.Parallel()

	content := "## Report\n\nFixed the bug in `a.go`.\n\n```json\n{\"cited_evidence\":[{\"path\":\"a.go\",\"line\":3,\"excerpt\":\"x\"}],\"cited_urls\":[],\"artifact_ids\":[]}\n```"
	prepared, read, ok := guard.PrepareCoordinatorCloseoutContent(toolcontract.SurfaceImplementInvestigate, content, "")
	report, parsed := guidance.ParseCoordinatorCompletionReport(prepared)
	if !ok || !parsed || len(read.Unread) != 0 {
		t.Fatalf("assembled envelope not parseable: %q", prepared)
	}
	if report.Synthesis != "## Report\n\nFixed the bug in `a.go`." {
		t.Fatalf("synthesis=%q", report.Synthesis)
	}
	if len(report.CitedEvidence) != 1 || report.CitedEvidence[0].Path != "a.go" || report.CitedEvidence[0].Line != 3 {
		t.Fatalf("cited_evidence=%+v", report.CitedEvidence)
	}

	misplaced := "## Report\n\nBody.\n\n```json\n{\"findings\":[{\"id\":\"c1\",\"title\":\"A\",\"disposition\":\"act\",\"ask\":{\"do\":\"x\",\"effort\":\"small\"}}],\"set_asides\":[{\"scanner\":\"s\",\"paths\":[\"t/**\"],\"reason\":\"fixtures\"}]}\n```"
	prepared, read, ok = guard.PrepareCoordinatorCloseoutContent(toolcontract.SurfaceImplementInvestigate, misplaced, "")
	report, parsed = guidance.ParseCoordinatorCompletionReport(prepared)
	if !ok || !parsed || len(report.Findings) != 1 || len(report.SetAsides) != 1 || report.Synthesis != "## Report\n\nBody." {
		t.Fatalf("readable members lost: %q", prepared)
	}
	if len(read.Unread) != 1 || read.Unread[0].Path != "findings[0].ask" {
		t.Fatalf("unread = %+v, want findings[0].ask", read.Unread)
	}

	sample := "## Report\n\nConfig sample:\n\n```json\n{\"port\": 8080}\n```"
	prepared, read, ok = guard.PrepareCoordinatorCloseoutContent(toolcontract.SurfaceImplementInvestigate, sample, "")
	if report, parsed := guidance.ParseCoordinatorCompletionReport(prepared); !ok || !parsed || !strings.Contains(report.Synthesis, "8080") || len(read.Unread) != 0 {
		t.Fatalf("sample lost from prose: %q", prepared)
	}
}

func TestPrepareCoordinatorCloseoutContent(t *testing.T) {
	t.Parallel()

	rejectFmt := coordinatorRejectFmt(t)
	markdown := "## Missing rules audit\n\nFixed kinging in `script.js`."

	t.Run("markdown coerced on investigate surface", func(t *testing.T) {
		t.Parallel()
		prepared, _, ok := guard.PrepareCoordinatorCloseoutContent(toolcontract.SurfaceImplementInvestigate, markdown, "")
		if !ok {
			t.Fatal("expected markdown coercion")
		}
		report, parsed := guidance.ParseCoordinatorCompletionReport(prepared)
		if !parsed {
			t.Fatalf("prepared content not parseable: %q", prepared)
		}
		if report.Synthesis != markdown {
			t.Fatalf("synthesis=%q", report.Synthesis)
		}
		reject, blocked := guard.FormatHostNoToolTurnReject(
			&api.Session{Posture: api.SessionPostureBuild}, hostLoopHistory(
				nil),
			prepared, nil,
			toolcontract.SurfaceImplementInvestigate, true, surface.ImplementSessionState{}, rejectFmt, guard.BatchTurnGuard{})

		if blocked {
			t.Fatalf("coerced envelope should pass closeout guard, reject=%q", reject)
		}
	})

	t.Run("fenced json normalized on synthesis surface", func(t *testing.T) {
		t.Parallel()
		content := "```json\n{\"synthesis\":\"Report body.\",\"cited_evidence\":[{\"path\":\"a.go\",\"line\":1,\"excerpt\":\"x\"}]}\n```"
		prepared, _, ok := guard.PrepareCoordinatorCloseoutContent(spawn.SurfaceImplementSynthesis, content, "")
		if !ok {
			t.Fatal("expected fenced json normalization")
		}
		if strings.Contains(prepared, "```") {
			t.Fatalf("expected bare json, got %q", prepared)
		}
		reject, blocked := guard.FormatHostNoToolTurnReject(
			&api.Session{Posture: api.SessionPostureBuild}, hostLoopHistory(
				nil),
			prepared, nil,
			spawn.SurfaceImplementSynthesis, true, surface.ImplementSessionState{}, rejectFmt, guard.BatchTurnGuard{})

		if blocked {
			t.Fatalf("normalized envelope should pass closeout guard, reject=%q", reject)
		}
	})

	t.Run("non closeout surface unchanged", func(t *testing.T) {
		t.Parallel()
		prepared, _, ok := guard.PrepareCoordinatorCloseoutContent("implement_routing", markdown, "")
		if ok {
			t.Fatalf("expected no prepare on routing surface, got %q", prepared)
		}
		if prepared != markdown {
			t.Fatalf("content=%q", prepared)
		}
	})

	t.Run("citation-only retry stitches pinned synthesis", func(t *testing.T) {
		t.Parallel()
		const pinned = "## Architecture\n\nThe sidecar manages session state."
		const trailer = "```json\n{\"cited_evidence\":[{\"path\":\"internal/session.go\",\"line\":12,\"excerpt\":\"type Manager struct\"}],\"cited_urls\":[],\"artifact_ids\":[]}\n```"
		prepared, _, ok := guard.PrepareCoordinatorCloseoutContent(spawn.SurfaceImplementSynthesis, trailer, pinned)
		if !ok {
			t.Fatal("expected citation-only retry to stitch")
		}
		report, parsed := guidance.ParseCoordinatorCompletionReport(prepared)
		if !parsed || report.Synthesis != pinned || len(report.CitedEvidence) != 1 {
			t.Fatalf("stitched report = %+v parsed=%v", report, parsed)
		}
	})

	t.Run("citation-only fence without pinned synthesis rejects", func(t *testing.T) {
		t.Parallel()
		const trailer = "```json\n{\"cited_evidence\":[],\"cited_urls\":[]}\n```"
		if prepared, _, ok := guard.PrepareCoordinatorCloseoutContent(spawn.SurfaceImplementSynthesis, trailer, ""); ok {
			t.Fatalf("unexpected standalone citation closeout: %q", prepared)
		}
	})
}

// A prose-only investigate closeout with observed evidence returns
// INVEST_CITATIONS_REQUIRED so the loop requests citation repair. The guard does not mint grounding.
func TestEvaluateCoordinatorCloseoutGrounding_coercedProseReturnsCitationsRequired(t *testing.T) {
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
	}
	ledger := investigateLedgerReader{root: root, sessionID: sess.ID, msgs: history[1:]}
	prepared, _, ok := guard.PrepareCoordinatorCloseoutContent(toolcontract.SurfaceImplementInvestigate, "## Found foo\n\nUpdated internal/foo.go.", "")
	if !ok {
		t.Fatal("expected markdown coercion")
	}
	report, parsed := guidance.ParseCoordinatorCompletionReport(prepared)
	if !parsed {
		t.Fatalf("prepared content not parseable: %q", prepared)
	}
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, toolcontract.SurfaceImplementInvestigate, report, []string{"read"}, evidence.CitationRoots{ProjectDir: root},
	)
	testutil.FailErr(t, "guard.EvaluateCoordinatorCloseoutGrounding failed", err)
	if verdict.Code != guidance.InvestCitationsRequiredCode {
		t.Fatalf("code=%q want %q", verdict.Code, guidance.InvestCitationsRequiredCode)
	}
	if !guidance.CitationsRequiredFamily(verdict.Code) {
		t.Fatal("expected CitationsRequiredFamily for missing typed references")
	}
	if verdict.Grounding != nil {
		t.Fatalf("grounding=%+v want nil (guard signals; emitAssembledCloseout mints grounding)", verdict.Grounding)
	}
}

func TestEvaluateCoordinatorCloseoutGrounding_skipsWhenNoToolsRunOnFirstTurn(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sess := &api.Session{ID: "parent-1", WorkspacePath: root}
	history := []api.Message{{Role: api.MessageRoleUser, Content: "who wins?"}}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "No preference.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "phantom.go", Line: 1, Excerpt: "package phantom",
		}},
	}
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), nil, sess, history, toolcontract.SurfaceImplementInvestigate, report, nil, evidence.CitationRoots{ProjectDir: root},
	)
	testutil.FailErr(t, "guard.EvaluateCoordinatorCloseoutGrounding failed", err)
	if strings.TrimSpace(verdict.Code) != "" {
		t.Fatalf("code=%q want pass", verdict.Code)
	}
	if verdict.Grounding == nil || !verdict.Grounding.Traced {
		t.Fatalf("grounding=%+v want traced", verdict.Grounding)
	}
}

func TestEvaluateCoordinatorCloseoutGrounding_requiresGroundingOnLaterNoToolTurn(t *testing.T) {
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
		{Role: api.MessageRoleAssistant, Content: `{"synthesis":"found foo","cited_evidence":[{"path":"internal/foo.go","line":1,"excerpt":"package foo"}]}`},
		{Role: api.MessageRoleUser, Content: "say more"},
	}
	ledger := investigateLedgerReader{root: root, sessionID: sess.ID, msgs: history[1:3]}
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Still about foo.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "phantom.go", Line: 1, Excerpt: "package phantom",
		}},
	}
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, toolcontract.SurfaceImplementInvestigate, report, nil, evidence.CitationRoots{ProjectDir: root},
	)
	testutil.FailErr(t, "guard.EvaluateCoordinatorCloseoutGrounding failed", err)
	if strings.TrimSpace(verdict.Code) == "" {
		t.Fatal("expected grounding reject on later no-tool turn with phantom citation")
	}
	if verdict.Grounding != nil && verdict.Grounding.Traced {
		t.Fatalf("grounding=%+v want not traced", verdict.Grounding)
	}
}
