package promptloop

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const contractArtifact = "90dcba36-946a-46c6-933d-e94207b897ab"

// retiredCloseoutWords never reach the model in a closeout refusal.
var retiredCloseoutWords = []string{"citations fence", "cited_evidence", "cited_urls", "artifact_ids", "trailing", "verification.method", "typed references", `"synthesis"`}

type closeoutContract struct {
	loop     *PromptLoop
	selected *oar.Decision
	fired    []string
	friction int
	accepted int
	noted    []string
	issues   []guidance.ReportDocumentIssue
	assemble func(forcedBy []string, draft guidance.CloseoutDraft, retries int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding)
	saved    api.Message
}

// newCloseoutContract runs the shipped policy, kicks and hint catalog over a ledger
// holding one read of a.go lines 1 to 5 and one fetched page.
func newCloseoutContract(t *testing.T) *closeoutContract {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	root := testutil.CheckoutRoot(t)
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	reg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "load anchor registry", err)
	t.Cleanup(anchor.SetAnchorsFor(func(context.Context, string) *anchor.Registry { return reg }))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
	hints := loadCoordinatorTestHintConfig(t)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ledger := evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", SourceTool: "read",
			Body:       []string{"1|one", "2|two", "3|three", "4|four", "5|five"},
			LineRanges: []evidence.LineRange{{Start: 1, End: 5}}},
		{Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "fetch_url", URL: "https://example.test/seen"},
	})
	c := &closeoutContract{}
	c.loop = NewPromptLoopForTest(PromptLoopDeps{
		Closeout: CloseoutDeps{
			HintConfig:     hints,
			RejectFmt:      guidance.NewStaticRejectFormatter(hints),
			EvidenceLedger: closeoutLedgerReader{ledger: ledger},
			EvaluateCloseoutBlock: func(ctx context.Context, sess *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
				gc.Session.SessionID = sess.ID
				result, err := pipeline.EvaluateBlock(ctx, oar.AnchorCoordinatorCloseoutCheck, gc)
				if err != nil || result.Decision == nil {
					return nil, err
				}
				c.fired = append(c.fired, result.Decision.Code)
				for _, advisory := range result.Decision.Advisories {
					c.fired = append(c.fired, advisory.Code)
				}
				if result.Decision.Effect != oar.EffectBlock {
					return nil, nil
				}
				c.selected = result.Decision
				return result.Decision, nil
			},
			CheckRunReportDocument: func(context.Context, string, guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error) {
				return c.issues, nil
			},
			OnGroundedSynthesisAccepted: func(context.Context, *api.Session, string) { c.accepted++ },
			RecordGroundingFriction: func(context.Context, string) guidance.GroundingFriction {
				c.friction++
				return guidance.GroundingFriction{Remaining: 5}
			},
			NoteCloseoutGroundingReject: func(_ context.Context, _, code, _ string, _ guidance.CloseoutDraft, _ []jsonshape.Issue) (int, string) {
				c.noted = append(c.noted, code)
				return 1, ""
			},
			RenderHostKick: func(ctx context.Context, id string, data map[string]any) (string, error) {
				return engine.RenderKick(ctx, id, data)
			},
			AssembleLedgerCloseout: func(_ context.Context, _, _ string, forcedBy []string, draft guidance.CloseoutDraft, retries int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
				return c.assemble(forcedBy, draft, retries)
			},
		},
		Projection: ProjectionDeps{
			AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { return 1, nil },
			AppendMessages:     func(context.Context, string, ...api.Message) error { return nil },
			UpdateMessage: func(_ context.Context, _, _ string, message api.Message) error {
				c.saved = message
				return nil
			},
		},
	})
	c.assemble = func(forcedBy []string, draft guidance.CloseoutDraft, retries int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
		return guidance.AssembleRetainedCloseout(evidence.CitationRoots{}, "implement_synthesis", guidance.CloseoutEvidence{Ledger: ledger}, draft, nil, forcedBy[0], retries)
	}
	return c
}

func contractHistory(content string) []api.Message {
	return []api.Message{
		{ID: "user", Role: api.MessageRoleUser, Content: "Inspect it.", Visibility: api.MessageVisibilityTranscript},
		{ID: "call", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "r1", Name: "read"}}},
		{ID: "result", Role: api.MessageRoleTool, EvidenceHandles: []string{"read#1"}, ToolResult: &api.ToolResult{ToolCallID: "r1", Tool: "read", Outcome: api.ToolResultOutcomeCompleted}},
		{ID: "slot", Role: api.MessageRoleAssistant, Content: content},
	}
}

// refusalBody is the host nudge the model reads next.
func refusalBody(t *testing.T, history []api.Message) string {
	t.Helper()
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == api.MessageRoleUser {
			return history[i].Content
		}
	}
	t.Fatal("no refusal in history")
	return ""
}

// drive is one tool-free response down the real closeout path.
func (c *closeoutContract) drive(t *testing.T, surface, content string, reportPhase bool, history []api.Message) (closeoutCommitOutcome, *promptLoopTurnState) {
	t.Helper()
	frame := testReportFrame()
	frame.Runtime.ReportDocumentEnabled = reportPhase
	st := &promptLoopTurnState{coordinatorFrame: frame, draftSlotID: "slot", draftSlotAppended: true, offeredToolNames: []string{"read"}, turnTools: []string{"read"}}
	sess := &api.Session{ID: "contract-" + t.Name(), WorkspacePath: t.TempDir()}
	assistant := api.Message{ID: "slot", Role: api.MessageRoleAssistant, Content: content}
	read, err := c.loop.Closeout.readCloseoutDraft(t.Context(), sess, surface, st, history, assistant)
	testutil.FailErr(t, "read the closeout", err)
	out, err := c.loop.Closeout.handleAcceptedCloseoutReport(t.Context(), sess, sess.ID, "", surface, st, history, assistant, read)
	testutil.FailErr(t, "handle the closeout", err)
	return out, st
}

// AC-COMPAT-09, D2: each rule fires on its Markdown trigger, on the budget it belongs
// to, and the refusal the model reads teaches no retired form.
func TestMarkdownCloseoutRulesFireOnTheirTriggers(t *testing.T) {
	for _, tc := range []struct {
		name, code, surface, content string
		reportPhase, document        bool
		issue                        string
	}{
		{name: "unobserved handle", code: guidance.SynthHandleNotInLegsCode, surface: "implement_synthesis", content: "Done [read#9:1]."},
		{name: "unobserved handle", code: guidance.InvestHandleNotObservedCode, surface: "implement_investigate", content: "Done [read#9:1]."},
		{name: "unobserved path", code: guidance.SynthHandleNotInLegsCode, surface: "implement_synthesis", content: "Done [phantom.go:1]."},
		{name: "unobserved artifact", code: guidance.SynthHandleNotInLegsCode, surface: "implement_synthesis", content: "Done [read#1:2].\n[artifact:" + contractArtifact + "]"},
		{name: "unobserved link", code: guidance.SynthURLNotObservedCode, surface: "implement_synthesis", content: "Per [a page](https://example.test/unseen) [read#1:2]."},
		{name: "unobserved link", code: guidance.InvestURLNotObservedCode, surface: "implement_investigate", content: "Per <https://example.test/unseen> [read#1:2]."},
		{name: "range past the observation", code: guidance.SynthCitationUnverifiableCode, surface: "implement_synthesis", content: "Done [read#1:4-9]."},
		{name: "line past the observation", code: guidance.InvestCitationUnverifiableCode, surface: "implement_investigate", content: "Done [read#1:99]."},
		{name: "image embed", code: guidance.PresentMarkdownEmbedCode, surface: "implement_synthesis", content: "Done [read#1:2].\n\n![image](" + contractArtifact + ")"},
		{name: "HTML image embed", code: guidance.PresentMarkdownEmbedCode, surface: "implement_investigate", content: `Done [read#1:2]. <img src="` + contractArtifact + `">`},
		{name: "repair with nothing to repair", code: guidance.CitationRepairInvalidCode, surface: "implement_synthesis", content: "Done [read#1:2].\n[cite: c1 => read#1:3]"},
		{name: "malformed node", code: guidance.CitationSyntaxInvalidCode, surface: "implement_investigate", content: "Done [read#1:2\nmore [read#1:3]."},
		{name: "invalid verification", code: guidance.CitationSyntaxInvalidCode, surface: "implement_synthesis", content: "Done [read#1:2].\n[verify: thoroughly - Looked]"},
		{name: "misplaced fence member", code: guidance.ReportFenceUnreadableCode, surface: "implement_synthesis", reportPhase: true, document: true,
			content: "Done [read#1:2].\n\n```json\n{\"findings\":[{\"id\":\"f\",\"title\":\"T\",\"ask\":{\"do\":\"x\"}}]}\n```"},
		{name: "document invalid", code: guidance.ReportDocumentInvalidCode, surface: "implement_synthesis", reportPhase: true, document: true, content: "Done [read#1:2].", issue: guidance.ReportDocumentInvalidCode},
		{name: "claim unreported", code: guidance.ReportClaimUnreportedCode, surface: "implement_synthesis", reportPhase: true, document: true, content: "Done [read#1:2].", issue: guidance.ReportClaimUnreportedCode},
		{name: "inventory unaccounted", code: guidance.ReportInventoryUnaccountedCode, surface: "implement_synthesis", reportPhase: true, document: true, content: "Done [read#1:2].", issue: guidance.ReportInventoryUnaccountedCode},
	} {
		t.Run(tc.code+"/"+tc.name, func(t *testing.T) {
			c := newCloseoutContract(t)
			if tc.issue != "" {
				c.issues = []guidance.ReportDocumentIssue{{Code: tc.issue, Reason: "a named requirement", Offenders: []string{"f1"}, Count: 1}}
			}
			out, st := c.drive(t, tc.surface, tc.content, tc.reportPhase, contractHistory(tc.content))
			if c.selected == nil || c.selected.Code != tc.code {
				t.Fatalf("selected = %+v, want %s", c.selected, tc.code)
			}
			if !out.retry || out.committed || out.exhausted {
				t.Fatalf("outcome = %+v, want a retry", out)
			}
			if len(c.noted) != 1 || c.noted[0] != tc.code {
				t.Fatalf("noted = %v", c.noted)
			}
			if tc.document {
				if st.closeoutRetry.documentAttempt != 1 || st.closeoutRetry.attempt != 0 || c.friction != 0 {
					t.Fatalf("a document refusal spent the citation budget: %+v friction=%d", st.closeoutRetry, c.friction)
				}
			} else if st.closeoutRetry.attempt != 1 || st.closeoutRetry.documentAttempt != 0 || c.friction != 1 {
				t.Fatalf("a citation refusal spent %+v friction=%d", st.closeoutRetry, c.friction)
			}
			body := refusalBody(t, out.history)
			if !strings.Contains(body, "Code: "+tc.code) {
				t.Fatalf("refusal lacks its code:\n%s", body)
			}
			for _, retired := range retiredCloseoutWords {
				if strings.Contains(body, retired) {
					t.Fatalf("refusal teaches retired %q:\n%s", retired, body)
				}
			}
		})
	}
}

// AC-CLO-13: a retired fence member is refused by name on the document budget, and
// the retained document the kick returns holds no retired member.
func TestRetiredFenceMembersSpendOnlyTheDocumentBudget(t *testing.T) {
	c := newCloseoutContract(t)
	content := "Done [read#1:2].\n\n```json\n{\"headline\":\"H\",\"cited_evidence\":[{\"evidence\":\"read#1\"}],\"verification\":{\"method\":\"targeted\",\"reason\":\"r\"}}\n```"
	out, st := c.drive(t, "implement_synthesis", content, true, contractHistory(content))
	if c.selected == nil || c.selected.Code != guidance.ReportFenceUnreadableCode || !out.retry {
		t.Fatalf("selected = %+v outcome = %+v", c.selected, out)
	}
	if st.closeoutRetry.documentAttempt != 1 || st.closeoutRetry.attempt != 0 || c.friction != 0 {
		t.Fatalf("budgets = %+v friction=%d", st.closeoutRetry, c.friction)
	}
	body := refusalBody(t, out.history)
	if !strings.Contains(body, "`cited_evidence`: written inline") || !strings.Contains(body, "`verification`: written inline") {
		t.Fatalf("refusal does not name the members:\n%s", body)
	}
	fence := body[strings.Index(body, "```json"):]
	fence = fence[:strings.Index(fence[3:], "```")+3]
	if strings.Contains(fence, "cited_evidence") || !strings.Contains(fence, `"headline":"H"`) {
		t.Fatalf("retained fence:\n%s", fence)
	}
	if !strings.Contains(body, "Allowed top-level report keys: ") {
		t.Fatalf("refusal lacks the allowed keys:\n%s", body)
	}
}

// AC-CLO-05: the requirement decision hands an uncited answer to host assembly with
// no model bounce, and says so.
func TestUncitedAnswerWithCurrentEvidenceHostAssembles(t *testing.T) {
	for _, tc := range []struct{ surface, code string }{
		{"implement_synthesis", guidance.SynthCitationsRequiredCode},
		{"implement_investigate", guidance.InvestCitationsRequiredCode},
	} {
		t.Run(tc.code, func(t *testing.T) {
			c := newCloseoutContract(t)
			const content = "An uncited answer.\n[verify: inspection - Read only]"
			out, st := c.drive(t, tc.surface, content, false, contractHistory(content))
			if !out.committed || c.selected != nil || st.closeoutRetry.attempt != 0 || c.friction != 0 {
				t.Fatalf("outcome = %+v selected = %+v retry = %+v", out, c.selected, st.closeoutRetry)
			}
			if len(c.fired) == 0 || c.fired[0] != tc.code {
				t.Fatalf("fired = %v, want the warning", c.fired)
			}
			// D6: host attachment is no grounded synthesis.
			if c.accepted != 0 {
				t.Fatalf("batch latch fired on a host-assembled report")
			}
			g := out.assistantMsg.Grounding
			if g == nil || !g.HostAssembled || g.Traced || g.HintCode != tc.code || g.RetryCount != 0 {
				t.Fatalf("grounding = %+v", g)
			}
			if out.assistantMsg.Content != "An uncited answer." {
				t.Fatalf("display = %q", out.assistantMsg.Content)
			}
			if g.Verification == nil || g.Verification.Method != "inspection" {
				t.Fatalf("verification = %+v", g.Verification)
			}
		})
	}
}

// AC-CLO-16: a repeated closeout ends the turn without assembling a duplicate, and
// notes are no prior closeout. Comparison is on resolved citations, so citing the
// handle again repeats what was stored as a path.
func TestRepeatedCloseoutEndsWithoutAssembly(t *testing.T) {
	prior := api.Message{ID: "prior", Role: api.MessageRoleAssistant, Content: "Earlier.", Visibility: api.MessageVisibilityTranscript,
		Grounding: &api.CitationGrounding{Traced: true, CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "read#1", Path: "a.go", Line: 2}}}}
	note := prior
	note.ID, note.Kind = "note", api.MessageKindAgentNote
	for _, tc := range []struct {
		name, code, surface string
		row                 api.Message
		repeated            bool
	}{
		{"synthesis", guidance.SynthNoNewEvidenceCode, "implement_synthesis", prior, true},
		{"investigate", guidance.InvestNoNewEvidenceCode, "implement_investigate", prior, true},
		{"a note is no closeout", "", "implement_synthesis", note, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCloseoutContract(t)
			const content = "Earlier again [read#1:2]."
			history := contractHistory(content)
			history = append(history[:3:3], tc.row, history[3])
			out, _ := c.drive(t, tc.surface, content, false, history)
			if tc.repeated {
				if c.selected == nil || c.selected.Code != tc.code || !out.endWithoutAssemble || out.committed || out.retry {
					t.Fatalf("selected = %+v outcome = %+v", c.selected, out)
				}
				return
			}
			if !out.committed || c.selected != nil {
				t.Fatalf("selected = %+v outcome = %+v", c.selected, out)
			}
		})
	}
}

// AC-CLO-18: repeating an offender ends the cycle before the budget does, whatever
// the repair ID it arrives under.
func TestRepeatedOffenderExhausts(t *testing.T) {
	c := newCloseoutContract(t)
	const content = "Done [read#9:1]."
	frame := testReportFrame()
	frame.Runtime.ReportDocumentEnabled = false
	st := &promptLoopTurnState{coordinatorFrame: frame, draftSlotID: "slot", draftSlotAppended: true, offeredToolNames: []string{"read"}, turnTools: []string{"read"}}
	sess := &api.Session{ID: "contract-repeat", WorkspacePath: t.TempDir()}
	assistant := api.Message{ID: "slot", Role: api.MessageRoleAssistant, Content: content}
	read, err := c.loop.Closeout.readCloseoutDraft(t.Context(), sess, "implement_synthesis", st, contractHistory(content), assistant)
	testutil.FailErr(t, "read", err)
	first, err := c.loop.Closeout.handleAcceptedCloseoutReport(t.Context(), sess, sess.ID, "", "implement_synthesis", st, contractHistory(content), assistant, read)
	testutil.FailErr(t, "first refusal", err)
	if !first.retry {
		t.Fatalf("first = %+v", first)
	}
	second, err := c.loop.Closeout.handleAcceptedCloseoutReport(t.Context(), sess, sess.ID, "", "implement_synthesis", st, contractHistory(content), assistant, read)
	testutil.FailErr(t, "second refusal", err)
	if !second.exhausted || second.retry {
		t.Fatalf("second = %+v, want the repeat to exhaust", second)
	}
}

// AC-CLO-01, AC-COMPAT-09: rules stay silent on what is not their trigger, and a valid
// closeout commits a completion report whose display has no protocol node.
func TestValidMarkdownCloseoutCommitsWithoutFalseChecks(t *testing.T) {
	for _, tc := range []struct {
		name, content, want string
		reportPhase         bool
	}{
		{name: "AC-CLO-01 citations", content: "The timeout moved [read#1:2].", want: "The timeout moved [a.go:2](a.go#L2)."},
		{name: "a range", content: "The block [read#1:2-4].", want: "The block [a.go:2-4](a.go#L2-L4)."},
		{name: "an observed link", content: "Per [the page](https://example.test/seen) [read#1:2].", want: "Per [the page](https://example.test/seen) [a.go:2](a.go#L2)."},
		{name: "directives leave the display", content: "Done [read#1:2].\n[verify: targeted - Ran it]", want: "Done [a.go:2](a.go#L2)."},
		{name: "D15 a fence-led closeout", content: "```\nnot a citation [read#9:1]\n```\n\nDone [read#1:2].", want: "```\nnot a citation [read#9:1]\n```\n\nDone [a.go:2](a.go#L2)."},
		{name: "a literal embed example", content: "Never `![x](" + contractArtifact + ")`, per [read#1:2].", want: "Never `![x](" + contractArtifact + ")`, per [a.go:2](a.go#L2)."},
		{name: "AC-CLO-15 a trailing JSON example", content: "Done [read#1:2].\n\n```json\n{\"cited_evidence\":[]}\n```", want: "Done [a.go:2](a.go#L2).\n\n```json\n{\"cited_evidence\":[]}\n```"},
		{name: "literal code stays exempt from the echo", content: "Never paste `<task job_id=\"j\">` [read#1:2].", want: "Never paste `<task job_id=\"j\">` [a.go:2](a.go#L2)."},
		{name: "a report phase document", reportPhase: true, content: "Done [read#1:2].\n\n```json\n{\"headline\":\"H\"}\n```", want: "Done [a.go:2](a.go#L2)."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCloseoutContract(t)
			out, _ := c.drive(t, "implement_synthesis", tc.content, tc.reportPhase, contractHistory(tc.content))
			if !out.committed || c.selected != nil || len(c.fired) != 0 {
				t.Fatalf("outcome = %+v selected = %+v fired = %v", out, c.selected, c.fired)
			}
			msg := out.assistantMsg
			if msg.Content != tc.want {
				t.Fatalf("display = %q, want %q", msg.Content, tc.want)
			}
			if msg.Kind != api.MessageKindCompletionReport || msg.CompletionReport == nil || msg.Visibility != api.MessageVisibilityTranscript {
				t.Fatalf("message = %+v", msg)
			}
			g := msg.Grounding
			if g == nil || !g.Traced || g.EvidenceOptional || g.HostAssembled {
				t.Fatalf("grounding = %+v", g)
			}
			// D6: the one-report-per-batch latch closes on the grounded report.
			if c.accepted != 1 {
				t.Fatalf("batch latch fired %d times", c.accepted)
			}
			// D7: no closeout check claims a citation sat in prose.
			if g.ProseLeakCount != 0 || g.ProseAdvisoryCount != 0 {
				t.Fatalf("prose counts: %+v", g)
			}
			for _, check := range g.Checks {
				if check.ID == "prose_duplication" || check.ID == "prose_advisories" || strings.Contains(check.Summary, "typed") {
					t.Fatalf("false check: %+v", check)
				}
			}
		})
	}
}

// D6: an exempt reply is grounded, so it closes the batch too.
func TestEvidenceOptionalReplyClosesTheBatch(t *testing.T) {
	c := newCloseoutContract(t)
	const content = "You're welcome."
	history := []api.Message{{ID: "user", Role: api.MessageRoleUser, Content: "Thanks."}, {ID: "slot", Role: api.MessageRoleAssistant, Content: content}}
	out, _ := c.drive(t, "implement_synthesis", content, false, history)
	if !out.committed || c.accepted != 1 || !out.assistantMsg.Grounding.EvidenceOptional {
		t.Fatalf("outcome = %+v accepted = %d", out, c.accepted)
	}
}

// AC-CLO-17: declaring a verification reports the assessment and manufactures no
// receipt: nothing but the citation is evidence.
func TestVerificationDeclarationIsAnAssessmentOnly(t *testing.T) {
	c := newCloseoutContract(t)
	const content = "Done [read#1:2].\n[verify: project - All green]"
	out, _ := c.drive(t, "implement_synthesis", content, false, contractHistory(content))
	g := out.assistantMsg.Grounding
	if g == nil || g.Verification == nil || g.Verification.Method != "project" || g.Verification.Reason != "All green" {
		t.Fatalf("grounding = %+v", g)
	}
	if len(g.CitedEvidence) != 1 || g.CitedEvidence[0].Handle != "read#1" {
		t.Fatalf("cited = %+v", g.CitedEvidence)
	}
}
