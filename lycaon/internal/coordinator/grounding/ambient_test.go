package coordgrounding

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func groundingTestPipeline(t *testing.T) *oar.GuardPipeline {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	root := testutil.CheckoutRoot(t)
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load grounding policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
	pipeline.EnableAnchor(oar.AnchorCoordinatorPostTurn)
	return pipeline
}

func TestAmbientGroundingCheckTurnCases(t *testing.T) {
	gate := NewSimpleAmbientGroundingGate(grounding.DefaultGroundingConfig())
	cases := []struct {
		name string
		in   AmbientGroundingInput
		want bool
		code string
	}{
		{"no jobs grounded", AmbientGroundingInput{}, true, ""},
		{"complete job without summary", AmbientGroundingInput{Jobs: []api.WorkerTask{{ID: "job-1", Status: api.WorkerStatusComplete}}}, false, grounding.AmbientUngroundedCompletionCode},
		{"task exempt", AmbientGroundingInput{Jobs: []api.WorkerTask{{ID: "job-1", Status: api.WorkerStatusComplete}}, LastTurnTools: []string{"task"}}, true, ""},
		{"instruct_worker exempt", AmbientGroundingInput{Jobs: []api.WorkerTask{{ID: "job-1", Status: api.WorkerStatusComplete}}, LastTurnTools: []string{"instruct_worker"}}, true, ""},
		{"wait exempt", AmbientGroundingInput{Jobs: []api.WorkerTask{{ID: "job-1", Status: api.WorkerStatusComplete}}, LastTurnTools: []string{"wait"}}, true, ""},
		{"running job grounded", AmbientGroundingInput{Jobs: []api.WorkerTask{{ID: "job-1", Status: api.WorkerStatusRunning}}}, true, ""},
		{"summary tag grounded", AmbientGroundingInput{Jobs: []api.WorkerTask{{ID: "job-1", Status: api.WorkerStatusComplete}}, SummaryTags: []grounding.WorkerSummaryTag{{JobID: "job-1"}}}, true, ""},
		{"no worker ungrounded audit", AmbientGroundingInput{LastAuditUngrounded: true, LastAuditCode: "SYNTH_HANDLE_NOT_IN_LEGS"}, false, "SYNTH_HANDLE_NOT_IN_LEGS"},
		{"host assembled turn", AmbientGroundingInput{LastAuditUngrounded: true, LastAuditHostAssembled: true}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := gate.CheckTurn(context.Background(), tc.in)
			if v.OK != tc.want {
				t.Fatalf("verdict = %+v, want OK=%v", v, tc.want)
			}
			if tc.code != "" && v.Code != tc.code {
				t.Fatalf("code = %q, want %q", v.Code, tc.code)
			}
		})
	}
}

func TestLastAssistantAuditSkipsAgentNotes(t *testing.T) {
	answer := &api.CitationGrounding{Traced: true}
	got := lastAssistantAudit([]api.Message{
		{Role: api.MessageRoleAssistant, Grounding: answer},
		{Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Grounding: &api.CitationGrounding{Traced: true, EvidenceOptional: true}},
	})
	if got != answer {
		t.Fatalf("audit = %+v", got)
	}
	if got := lastAssistantAudit([]api.Message{{Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Grounding: &api.CitationGrounding{}}}); got != nil {
		t.Fatalf("a note alone produced audit %+v", got)
	}
}

func TestAmbientGroundingDeliversOnceWithoutBlockingAndResetsCounters(t *testing.T) {
	cfg := grounding.DefaultGroundingConfig()
	coord := NewAmbientGroundingCoordinator(nil, nil, cfg, nil, nil)
	coord.Pipeline = groundingTestPipeline(t)
	const sessionID = "ambient-lifecycle"
	var notices []*oar.Decision
	coord.Pipeline.SetAdvisorySink(func(_ context.Context, _ string, anchor string, d *oar.Decision) error {
		if anchor != oar.AnchorCoordinatorPostTurn || d.Effect != oar.EffectNudge {
			t.Fatalf("ambient decision changed boundary or effect: %s %+v", anchor, d)
		}
		notices = append(notices, d)
		return nil
	})
	in := AmbientGroundingInput{SessionID: sessionID, Jobs: []api.WorkerTask{{ID: "finished-job", Status: api.WorkerStatusComplete}}}
	for range 8 {
		testutil.FailErr(t, "ambient advisory", coord.applyOARAmbientGrounding(t.Context(), sessionID, ambientStateKey(sessionID), in, grounding.GroundingVerdict{Code: grounding.AmbientUngroundedCompletionCode}))
	}
	if len(notices) != 8 || coord.IsEscalated(sessionID) {
		t.Fatalf("ambient delivery count=%d blocked=%v", len(notices), coord.IsEscalated(sessionID))
	}
	testutil.FailErr(t, "task resets counters", coord.AfterPrompt(t.Context(), sessionID, []string{"task"}))
	if coord.Pipeline.Counters().Get(sessionID, grounding.AmbientUngroundedCompletionCode, oar.CounterBreaker) != 0 || grounding.Flagged(coord.State.Get(ambientStateKey(sessionID)), cfg) {
		t.Fatal("grounded task retained advisory counters")
	}
}

func TestCloseoutBreakerEscalatesInBlockModeAndResetsOnInstruct(t *testing.T) {
	cfg := grounding.DefaultGroundingConfig()
	cfg.CircuitBreaker.EscalateMode = "block"
	coord := NewAmbientGroundingCoordinator(nil, nil, cfg, nil, nil)
	coord.Pipeline = groundingTestPipeline(t)
	const sessionID = "closeout-escalation"
	var notices []*oar.Decision
	coord.Pipeline.SetAdvisorySink(func(_ context.Context, _ string, _ string, d *oar.Decision) error {
		notices = append(notices, d)
		return nil
	})
	coord.Pipeline.Counters().Increment(sessionID, grounding.CoordinatorUngroundedClaimCode, oar.CounterBreaker, int64(cfg.CircuitBreaker.MaxConsecutiveUngrounded))
	err := coord.AfterPrompt(t.Context(), sessionID, nil)
	if !errors.Is(err, guidance.ErrGroundingEscalated) || len(notices) != 1 || notices[0].Code != grounding.CoordinatorEscalatedCode {
		t.Fatalf("expected escalation notice: err=%v notices=%+v", err, notices)
	}
	testutil.FailErr(t, "instruct_worker resets escalation", coord.AfterPrompt(t.Context(), sessionID, []string{"instruct_worker"}))
	if coord.IsEscalated(sessionID) {
		t.Fatal("instruct_worker must clear escalated state")
	}
}
