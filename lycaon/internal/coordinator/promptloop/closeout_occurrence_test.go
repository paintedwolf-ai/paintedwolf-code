package promptloop

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCloseoutReportUsesOnePolicyOccurrence(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	root := testutil.CheckoutRoot(t)
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
	cfg := loadCoordinatorTestHintConfig(t)
	for _, code := range []string{guidance.PresentMarkdownEmbedCode, guidance.InvestNoNewEvidenceCode, guidance.SynthNoNewEvidenceCode} {
		rule, _ := rules.Get(code)
		rule.OnFire = []oar.OnFireAction{oar.OnFireIncrementCounter}
	}
	for _, tc := range []struct {
		name, surface, evidence, body, code string
		duplicate                           bool
	}{
		{"format precedes citation repair", "implement_investigate", "missing#1", "![image](90dcba36-946a-46c6-933d-e94207b897ab)", guidance.PresentMarkdownEmbedCode, false},
		{"investigate duplicate", "implement_investigate", "command#1", "Earlier result.", guidance.InvestNoNewEvidenceCode, true},
		{"synthesis duplicate", "implement_synthesis", "command#1", "Earlier result.", guidance.SynthNoNewEvidenceCode, true},
		{"new observation", "implement_investigate", "command#2", "New result.", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := &api.Session{ID: t.Name(), WorkspacePath: t.TempDir()}
			history := []api.Message{
				{ID: "user", Role: api.MessageRoleUser, Content: "Inspect the result.", Visibility: api.MessageVisibilityTranscript},
				{ID: "prior", Role: api.MessageRoleAssistant, Content: "Earlier result.", Visibility: api.MessageVisibilityTranscript, Grounding: &api.CitationGrounding{CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "command#1"}}}},
				{ID: "draft", Role: api.MessageRoleAssistant},
			}
			calls := 0
			var selected *oar.Decision
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Closeout: CloseoutDeps{
					HintConfig:     cfg,
					RejectFmt:      guidance.NewStaticRejectFormatter(cfg),
					EvidenceLedger: closeoutLedgerReader{ledger: evidence.AssembleLedger([]evidence.Record{{Handle: "command#1", Kind: "command", Shape: evidence.ShapeCommand, Body: []string{"prior"}}, {Handle: "command#2", Kind: "command", Shape: evidence.ShapeCommand, Body: []string{"new"}}})},
					EvaluateCloseoutBlock: func(ctx context.Context, sess *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
						calls++
						gc.Session.SessionID = sess.ID
						result, err := pipeline.EvaluateBlock(ctx, oar.AnchorCoordinatorCloseoutCheck, gc)
						if err != nil {
							return nil, err
						}
						selected = result.Decision
						return selected, nil
					},
				},
				Projection: ProjectionDeps{
					AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { return 1, nil },
				},
			})
			st := &promptLoopTurnState{draftSlotID: "draft", draftSlotAppended: true, turnTools: []string{"command"}}
			report := guidance.CoordinatorCompletionReport{Synthesis: tc.body, CitedEvidence: []guidance.CoordinatorCitedEvidence{{Evidence: tc.evidence}}}
			out, err := loop.Closeout.handleAcceptedCloseoutReport(t.Context(), sess, sess.ID, "", tc.surface, st, history, history[2], guidance.CloseoutRead{Report: report})
			testutil.FailErr(t, "check report", err)
			if calls != 1 {
				t.Fatalf("report generated %d policy occurrences", calls)
			}
			if tc.code == "" {
				if !out.committed {
					t.Fatalf("new evidence did not commit: %+v", out)
				}
				return
			}
			if selected == nil || selected.Code != tc.code || len(selected.Copy) != 5 {
				t.Fatalf("closeout did not retain evaluated decision: %+v", selected)
			}
			if out.committed || out.endWithoutAssemble != tc.duplicate || out.retry == tc.duplicate {
				t.Fatalf("closeout lifecycle changed: %+v", out)
			}
			rule, _ := rules.Get(tc.code)
			if count := pipeline.Counters().Get(sess.ID, rule.Qualified(), oar.CounterFire); count != 1 {
				t.Fatalf("rule fired %d times", count)
			}
		})
	}
}
