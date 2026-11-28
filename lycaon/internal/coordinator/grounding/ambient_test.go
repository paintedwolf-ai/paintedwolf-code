package coordgrounding

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
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

func TestIsRepairExhaustedAuditDistinguishesS0102FromExhaustion(t *testing.T) {
	cases := []struct {
		name  string
		audit *api.CitationGrounding
		want  bool
	}{
		{"nil audit", nil, false},
		{"clean grounded", &api.CitationGrounding{Traced: true}, false},
		{"s01_02 immediate host attach without repair", &api.CitationGrounding{HostAssembled: true, Traced: false, RetryCount: 0, HintCode: "INVEST_CITATIONS_REQUIRED"}, false},
		{"repaired grounded closeout", &api.CitationGrounding{HostAssembled: false, Traced: true, RetryCount: 1}, false},
		{"repair exhausted host assembled", &api.CitationGrounding{HostAssembled: true, Traced: false, RetryCount: 2, HintCode: "INVEST_HANDLE_NOT_OBSERVED"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRepairExhaustedAudit(tc.audit); got != tc.want {
				t.Fatalf("isRepairExhaustedAudit(%+v) = %v, want %v", tc.audit, got, tc.want)
			}
		})
	}
}

func TestCloseoutBreakerCountsRepairExhaustedCloseoutsInFlagAndBlockModes(t *testing.T) {
	exhaustedIn := AmbientGroundingInput{
		LastAuditUngrounded:      true,
		LastAuditHostAssembled:   true,
		LastAuditRepairExhausted: true,
		LastAuditCode:            "INVEST_HANDLE_NOT_OBSERVED",
	}
	cleanIn := AmbientGroundingInput{LastAuditTraced: true}
	s0102In := AmbientGroundingInput{
		LastAuditUngrounded:    true,
		LastAuditHostAssembled: true,
		LastAuditCode:          "INVEST_CITATIONS_REQUIRED",
	}

	t.Run("flag mode trips at 3 non-consecutive total (G04-03) without blocking", func(t *testing.T) {
		cfg := grounding.DefaultGroundingConfig()
		cfg.CircuitBreaker.EscalateMode = "flag"
		coord := NewAmbientGroundingCoordinator(nil, nil, cfg, nil, nil)
		coord.Pipeline = groundingTestPipeline(t)
		var notices []*oar.Decision
		coord.Pipeline.SetAdvisorySink(func(_ context.Context, _ string, _ string, d *oar.Decision) error {
			notices = append(notices, d)
			return nil
		})
		const sid = "g04-03-flag"
		testutil.FailErr(t, "turn 1 exhausted", coord.checkCloseoutEscalation(t.Context(), sid, exhaustedIn))
		testutil.FailErr(t, "turn 2 clean", coord.checkCloseoutEscalation(t.Context(), sid, cleanIn))
		testutil.FailErr(t, "turn 3 exhausted", coord.checkCloseoutEscalation(t.Context(), sid, exhaustedIn))
		testutil.FailErr(t, "turn 4 clean", coord.checkCloseoutEscalation(t.Context(), sid, cleanIn))
		if coord.IsTripped(sid) || coord.IsEscalated(sid) || len(notices) != 0 {
			t.Fatalf("tripped too early: tripped=%v escalated=%v notices=%d", coord.IsTripped(sid), coord.IsEscalated(sid), len(notices))
		}
		testutil.FailErr(t, "turn 5 exhausted trips", coord.checkCloseoutEscalation(t.Context(), sid, exhaustedIn))
		if !coord.IsTripped(sid) || coord.IsEscalated(sid) || len(notices) != 1 || notices[0].Code != grounding.CoordinatorEscalatedCode {
			t.Fatalf("expected flag trip without block at 3 total: tripped=%v escalated=%v notices=%+v", coord.IsTripped(sid), coord.IsEscalated(sid), notices)
		}
	})

	t.Run("flag mode trips at 2 consecutive, emits advisory once, stays tripped, and keeps accepting turns (G04-04/G04-05)", func(t *testing.T) {
		cfg := grounding.DefaultGroundingConfig()
		cfg.CircuitBreaker.EscalateMode = "flag"
		coord := NewAmbientGroundingCoordinator(nil, nil, cfg, nil, nil)
		coord.Pipeline = groundingTestPipeline(t)
		var notices []*oar.Decision
		coord.Pipeline.SetAdvisorySink(func(_ context.Context, _ string, _ string, d *oar.Decision) error {
			notices = append(notices, d)
			return nil
		})
		const sid = "g04-04-05-flag"
		testutil.FailErr(t, "turn 1 exhausted", coord.checkCloseoutEscalation(t.Context(), sid, exhaustedIn))
		if coord.IsTripped(sid) || coord.IsEscalated(sid) || len(notices) != 0 {
			t.Fatalf("tripped after 1 turn: tripped=%v escalated=%v notices=%d", coord.IsTripped(sid), coord.IsEscalated(sid), len(notices))
		}
		testutil.FailErr(t, "turn 2 exhausted trips", coord.checkCloseoutEscalation(t.Context(), sid, exhaustedIn))
		if !coord.IsTripped(sid) || coord.IsEscalated(sid) || len(notices) != 1 || notices[0].Code != grounding.CoordinatorEscalatedCode {
			t.Fatalf("expected flag trip without block at 2 consecutive: tripped=%v escalated=%v notices=%+v", coord.IsTripped(sid), coord.IsEscalated(sid), notices)
		}
		// Turn 3 clean must proceed (IsEscalated == false), stay tripped, and not re-emit advisory.
		testutil.FailErr(t, "turn 3 clean stays tripped", coord.checkCloseoutEscalation(t.Context(), sid, cleanIn))
		if !coord.IsTripped(sid) || coord.IsEscalated(sid) {
			t.Fatalf("expected sticky tripped without blocking after clean turn: tripped=%v escalated=%v", coord.IsTripped(sid), coord.IsEscalated(sid))
		}
		if len(notices) != 1 {
			t.Fatalf("expected advisory emitted once on crossing, got %d notices", len(notices))
		}
	})

	t.Run("block mode returns ErrGroundingEscalated and blocks subsequent turns at 2 consecutive", func(t *testing.T) {
		cfg := grounding.DefaultGroundingConfig()
		cfg.CircuitBreaker.EscalateMode = "block"
		coord := NewAmbientGroundingCoordinator(nil, nil, cfg, nil, nil)
		coord.Pipeline = groundingTestPipeline(t)
		var notices []*oar.Decision
		coord.Pipeline.SetAdvisorySink(func(_ context.Context, _ string, _ string, d *oar.Decision) error {
			notices = append(notices, d)
			return nil
		})
		const sid = "g04-block"
		testutil.FailErr(t, "turn 1 exhausted", coord.checkCloseoutEscalation(t.Context(), sid, exhaustedIn))
		err := coord.checkCloseoutEscalation(t.Context(), sid, exhaustedIn)
		if !errors.Is(err, guidance.ErrGroundingEscalated) || !coord.IsTripped(sid) || !coord.IsEscalated(sid) {
			t.Fatalf("expected ErrGroundingEscalated in block mode: err=%v tripped=%v escalated=%v", err, coord.IsTripped(sid), coord.IsEscalated(sid))
		}
		if len(notices) != 1 || notices[0].Code != grounding.CoordinatorEscalatedCode {
			t.Fatalf("expected 1 escalated advisory on crossing, got %+v", notices)
		}
	})

	t.Run("policy copy with escalate_mode block refuses at threshold", func(t *testing.T) {
		raw, err := config.Read(config.Grounding)
		testutil.FailErr(t, "read bundled grounding.yaml", err)
		if !strings.Contains(string(raw), "escalate_mode: flag") {
			t.Fatalf("expected bundled grounding.yaml to ship escalate_mode: flag")
		}
		blockPolicy := strings.Replace(string(raw), "escalate_mode: flag", "escalate_mode: block", 1)
		var cfg grounding.GroundingConfig
		testutil.FailErr(t, "decode block policy copy", config.DecodeYAML([]byte(blockPolicy), &cfg))
		if cfg.CircuitBreaker.EscalateMode != "block" {
			t.Fatalf("policy copy escalate_mode = %q, want block", cfg.CircuitBreaker.EscalateMode)
		}
		coord := NewAmbientGroundingCoordinator(nil, nil, cfg, nil, nil)
		coord.Pipeline = groundingTestPipeline(t)
		const sid = "g04-policy-copy-block"
		testutil.FailErr(t, "turn 1 exhausted", coord.checkCloseoutEscalation(t.Context(), sid, exhaustedIn))
		if coord.IsEscalated(sid) {
			t.Fatal("blocked before reaching policy threshold")
		}
		err = coord.checkCloseoutEscalation(t.Context(), sid, exhaustedIn)
		if !errors.Is(err, guidance.ErrGroundingEscalated) || !coord.IsEscalated(sid) {
			t.Fatalf("expected policy copy block mode refusal at threshold: err=%v escalated=%v", err, coord.IsEscalated(sid))
		}
	})

	t.Run("S01-02 immediate host attach without repair never trips breaker", func(t *testing.T) {
		cfg := grounding.DefaultGroundingConfig()
		cfg.CircuitBreaker.EscalateMode = "flag"
		coord := NewAmbientGroundingCoordinator(nil, nil, cfg, nil, nil)
		coord.Pipeline = groundingTestPipeline(t)
		var notices []*oar.Decision
		coord.Pipeline.SetAdvisorySink(func(_ context.Context, _ string, _ string, d *oar.Decision) error {
			notices = append(notices, d)
			return nil
		})
		const sid = "s01-02-zero-charge"
		for range 5 {
			testutil.FailErr(t, "s01-02 turn", coord.checkCloseoutEscalation(t.Context(), sid, s0102In))
		}
		if coord.IsTripped(sid) || coord.IsEscalated(sid) || len(notices) != 0 {
			t.Fatalf("S01-02 immediate host attach should not trip breaker: tripped=%v escalated=%v notices=%d", coord.IsTripped(sid), coord.IsEscalated(sid), len(notices))
		}
	})
}
