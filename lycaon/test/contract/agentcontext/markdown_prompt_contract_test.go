package contract

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const (
	publicationRule = "Text alongside tool calls is shown to the user when valid"
	privateTextRule = "Text alongside tool calls on this step is private working context"
	finalAnswerRule = "Write your final answer as Markdown, citing where each claim is made"
	verifyRule      = "`[verify: targeted - reason]`"
	artifactRule    = "`[artifact:<uuid>]`"
	withheldNotice  = "[HOST: MESSAGE WITHHELD FROM USER]"
	withheldRepair  = "Publish a brief update with valid inline citations, or include the relevant explanation in your final reply."
)

// retiredProtocol never reaches any audience.
var retiredProtocol = []string{
	"surface_note", "SURFACE_NOTE_", "⟦D", "citations fence", "trailing metadata", "trailing citation",
	"trailing closeout JSON", "verification.method", "not shown to the user", "hidden from the user",
	"internal working note", "empty assistant text", `"synthesis"`,
}

// typedCitationFields stay only where a typed tool or a worker reads them. A word
// boundary leaves proof_json.visual_artifact_ids alone.
var typedCitationFields = regexp.MustCompile(`\b(cited_evidence|cited_urls|artifact_ids)\b|typed fields`)

// bareCoordinateForm is a placeholder coordinate in inline code, such as `path:line`.
// Inline code is never a citation, so teaching it yields citations that do not count.
// The bracket form starts with a bracket, and concrete code spans name no placeholder.
var bareCoordinateForm = regexp.MustCompile("`[\\w./@-]*(?:path|file)[\\w./@-]*(?::|#L)(?:line|N|\\d+)[^`]*`")

// AC-COMPAT-06: every rendered output teaches only the Markdown protocol. Exceptions
// are by audience, or a line naming the typed tool whose argument it describes.
func TestModelFacingOutputsTeachNoRetiredProtocol(t *testing.T) {
	outputs := modelFacingOutputs(t)
	if len(outputs) < 200 {
		t.Fatalf("corpus has %d outputs, the walk is broken", len(outputs))
	}
	for _, output := range outputs {
		for _, retired := range retiredProtocol {
			if strings.Contains(output.text, retired) {
				t.Errorf("%s teaches retired %q", output.name, retired)
			}
		}
		if output.standing && strings.Contains(output.text, "[cite:") {
			t.Errorf("%s teaches the repair form outside repair feedback", output.name)
		}
		if output.audience != audienceCoordinator {
			continue
		}
		for line := range strings.SplitSeq(output.text, "\n") {
			if field := typedCitationFields.FindString(line); field != "" && !namesTypedCitationTool(line) {
				t.Errorf("%s teaches %q to a coordinator outside a typed tool: %s", output.name, field, line)
			}
			if form := bareCoordinateForm.FindString(line); form != "" && !output.workflowVersioned && !namesTypedCitationTool(line) {
				t.Errorf("%s teaches %s, which is inline code and never a citation: %s", output.name, form, line)
			}
		}
	}
}

// AC-COMPAT-07: the rules cannot vanish behind a removed conditional. Every surface
// states what happens to its text and how to cite; workers read none of that.
func TestEverySurfaceRendersThePublicationAndCitationRules(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	root := contractcheck.RepoRoot(t)
	catalog, ids := coordinatorSurfaceIDs(t)
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			row, err := catalog.Surface(id)
			contractcheck.FailErr(t, "surface row", err)
			text := renderCoordinatorTripartiteForRunContext(t, root, api.CoordinatorRunContext{},
				&api.Session{Posture: api.SessionPostureBuild}, surface.HostLoopWakeSentinel, nil, id)
			want, refuse := publicationRule, privateTextRule
			if !row.PublishesMidTurnText {
				want, refuse = privateTextRule, publicationRule
			}
			for _, needed := range []string{want, finalAnswerRule, verifyRule, artifactRule, "cites a receipt inline"} {
				if !strings.Contains(text, needed) {
					t.Errorf("missing %q", needed)
				}
			}
			if strings.Contains(text, refuse) {
				t.Errorf("renders the rule for the other kind of surface: %q", refuse)
			}
		})
	}
}

// AC-COMPAT-07: worker personas keep their typed fields and hold no coordinator bytes.
func TestWorkerPersonasHoldNoCoordinatorCitationCopy(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	prompts.ResetPersonaContractCache()
	cfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "load persona contract", err)
	engine := contractPersonaEngine(t)
	for id := range cfg.Agents {
		text, err := prompts.RenderPersona(context.Background(), engine, id, map[string]any{"profile_has_write_tools": true, "profile_has_verify": true})
		contractcheck.FailErr(t, "render persona "+id, err)
		for _, coordinatorOnly := range []string{publicationRule, finalAnswerRule, "[verify:", "[artifact:", "[cite:", "cites a receipt inline"} {
			if strings.Contains(text, coordinatorOnly) {
				t.Errorf("worker %s holds coordinator copy %q", id, coordinatorOnly)
			}
		}
	}
}

// AC-COMPAT-07: each withheld reason says the text was withheld and how to recover.
func TestWithheldNoticesCarryTheRecoveryForm(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hints", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	formatter := guidance.NewStaticRejectFormatter(cfg)
	for _, code := range []string{
		guidance.MidTurnNoteUngroundedCode, guidance.MidTurnNoteHandleUngroundCode, guidance.MidTurnNoteURLOnlyCode,
		guidance.MidTurnNoteArtifactCode, guidance.MidTurnNoteSyntaxCode, guidance.MidTurnNoteEmbedCode,
	} {
		body, err := formatter.Format(code, guidance.OffenderHintData([]string{"read#9"}))
		contractcheck.FailErr(t, "render "+code, err)
		for _, needed := range []string{"Code: " + code, withheldNotice, withheldRepair} {
			if !strings.Contains(body, needed) {
				t.Errorf("%s lacks %q:\n%s", code, needed, body)
			}
		}
	}
}

// AC-COMPAT-07: the notice follows the same split publication makes.
func TestAuthorityNoticeFollowsTheAudience(t *testing.T) {
	publisher := transcript.ContentAuthorityNotice(transcript.AudiencePublisher)
	private := transcript.ContentAuthorityNotice(transcript.AudiencePrivate)
	if !strings.Contains(publisher, "shown directly to the user") || strings.Contains(publisher, "private working context") {
		t.Errorf("publisher notice: %s", publisher)
	}
	if !strings.Contains(private, "private working context") || strings.Contains(private, "shown directly to the user") {
		t.Errorf("private notice: %s", private)
	}
}

// AC-COMPAT-09: over Markdown, each turn rule fires on its trigger through the real
// guards and stock policy. The closeout rules are driven through the real commit path
// in promptloop's TestMarkdownCloseoutRulesFireOnTheirTriggers; the report-phase and
// source-evidence rules through the session's own finish path.
func TestMarkdownTurnRulesFireOnTheirTriggers(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hints", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	formatter := guidance.NewStaticRejectFormatter(cfg)
	sess := &api.Session{ID: "s1", AgentType: "coordinator", Posture: api.SessionPostureBuild}
	history := []api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Content: surface.HostLoopWakeSentinel}}
	for _, tc := range []struct {
		name, code, content, surfaceID string
		batch                          guard.BatchTurnGuard
	}{
		{name: "directives alone are no answer", code: guard.CoordinatorCloseoutNoAnswerCode, content: "[verify: targeted - Ran it]", surfaceID: spawn.SurfaceImplementSynthesis},
		{name: "bare JSON is no answer", code: guard.CoordinatorCloseoutNoAnswerCode, content: `{"synthesis":"Done."}`, surfaceID: spawn.SurfaceImplementSynthesis},
		{name: "a second report in the turn", code: guard.CoordinatorBatchAlreadyClosedCode, content: "Second report [read#1:2].", surfaceID: spawn.SurfaceImplementSynthesis, batch: guard.BatchTurnGuard{SynthesisAcceptedThisTurn: true}},
		{name: "an echoed task block", code: guard.CoordinatorTaskEnvelopeEchoCode, content: `It returned <task job_id="j1" state="complete"></task>.`, surfaceID: "implement_routing"},
	} {
		t.Run(tc.code+"/"+tc.name, func(t *testing.T) {
			reject, blocked := guard.FormatHostNoToolTurnReject(sess, history, tc.content, nil, tc.surfaceID, true,
				surface.ImplementSessionState{}, formatter, tc.batch)
			if !blocked || !strings.Contains(reject, "Code: "+tc.code) {
				t.Fatalf("blocked = %v, reject = %q", blocked, reject)
			}
			for _, retired := range retiredProtocol {
				if strings.Contains(reject, retired) {
					t.Fatalf("refusal teaches retired %q:\n%s", retired, reject)
				}
			}
		})
	}

	t.Run(guard.WaitHiddenTextCode+"/an uncited update before wait", func(t *testing.T) {
		carrier := api.Message{ID: "c1", Role: api.MessageRoleAssistant, Content: "Waiting on the worker.", ToolCalls: []api.ToolCall{{ID: "w1", Name: "wait"}}}
		gc := oar.NewGuardContext()
		guard.ObserveWaitVisibility(sess, []api.Message{carrier}, "wait", true, gc)
		if !gc.Session.UnpublishedToolTextPending {
			t.Fatal("the unpublished update owes nothing")
		}
		quiet := oar.NewGuardContext()
		guard.ObserveWaitVisibility(sess, []api.Message{carrier, {ID: api.MidTurnNoteID("c1"), Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: "Waiting on the worker [task#1]."}}, "wait", true, quiet)
		if quiet.Session.UnpublishedToolTextPending {
			t.Fatal("a published update still owes")
		}
	})
}
