package session

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type countingCostTracker struct {
	inner   cost.CostTracker
	summary int
}

type summaryErrorTracker struct {
	cost.CostTracker
	err error
}

func (t summaryErrorTracker) Summary(context.Context, api.CostScope, string, string) (api.CostSummary, error) {
	return api.CostSummary{}, t.err
}

func (c *countingCostTracker) RecordUsage(ctx context.Context, evt cost.UsageEvent) error {
	return c.inner.RecordUsage(ctx, evt)
}

func (c *countingCostTracker) Estimate(ctx context.Context, providerID, model string, usage cost.TokenUsage) (cost.CostEstimate, error) {
	return c.inner.Estimate(ctx, providerID, model, usage)
}

func (c *countingCostTracker) Summary(ctx context.Context, scope api.CostScope, sessionID, projectID string) (api.CostSummary, error) {
	c.summary++
	return c.inner.Summary(ctx, scope, sessionID, projectID)
}

func (c *countingCostTracker) ProjectReport(ctx context.Context, projectID string, query cost.ReportQuery) (api.ProjectCostReport, error) {
	return c.inner.ProjectReport(ctx, projectID, query)
}

func (c *countingCostTracker) ClaimSpendWarning(ctx context.Context, sessionID string, ceilingUSD float64) (bool, error) {
	return c.inner.ClaimSpendWarning(ctx, sessionID, ceilingUSD)
}

func (c *countingCostTracker) ClearSpendWarning(ctx context.Context, sessionID string) error {
	return c.inner.ClearSpendWarning(ctx, sessionID)
}

func spendRunwayMgr(t *testing.T, tracker cost.CostTracker, ceilingUSD float64, enabled bool) *Host {
	t.Helper()
	lim := settings.DefaultSessionLimits()
	lim.SpendCeilingEnabled = enabled
	lim.SessionSpendCeilingUSD = ceilingUSD
	mgr := NewHost(store.NewMemory(), Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: lim, Cost: tracker}, tools.NewStubRegistry())
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	mgr.SetWorkflowHints(hints, nil)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	return mgr
}

func recordSessionSpend(t *testing.T, tracker cost.CostTracker, sessionID string, usd float64) {
	t.Helper()
	testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(context.Background(), cost.UsageEvent{
		SessionID: sessionID, Caller: cost.CallerCoordinator, PromptTokens: 10, EstimatedNanoUSD: costtest.NanoUSD(t, usd),
	}))
}

func TestSpendCeilingStateOneSummaryCall(t *testing.T) {
	ctx := context.Background()
	inner := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
	recordSessionSpend(t, inner, "s1", 4.1)
	counter := &countingCostTracker{inner: inner}
	mgr := spendRunwayMgr(t, counter, 5, true)
	sess := &api.Session{ID: "s1", ProjectID: "p1"}

	before := counter.summary
	st, err := mgr.Runner.Spend.State(ctx, "s1", sess)
	testutil.FailErr(t, "spendCeilingState", err)
	if counter.summary-before != 1 {
		t.Fatalf("Summary calls = %d want 1", counter.summary-before)
	}
	if !st.Low || st.Reached || st.CeilingUSD != 5 {
		t.Fatalf("state = %+v want low runway below $5 ceiling", st)
	}

	before = counter.summary
	testutil.FailErr(t, "checkSpendCeiling", mgr.Runner.Spend.Check(ctx, "s1", sess))
	if counter.summary-before != 1 {
		t.Fatalf("checkSpendCeiling Summary calls = %d want 1", counter.summary-before)
	}
}

func TestPromptLoopSpendCheckFailsOpenWhenAccountingIsUnavailable(t *testing.T) {
	base := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
	mgr := spendRunwayMgr(t, summaryErrorTracker{
		CostTracker: base,
		err:         errors.New("cost database unavailable"),
	}, 5, true)
	check, err := mgr.Coordinator.RuntimeDependencies().LoopDeps().Nudges.CheckSpendCeiling(
		t.Context(), "s1", &api.Session{ID: "s1", ProjectID: "p1"},
	)
	testutil.FailErr(t, "CheckSpendCeiling", err)
	if check != (promptloop.SpendCeilingCheck{}) {
		t.Fatalf("check = %+v want no accounting-derived gate", check)
	}
}

func TestSpendRunwayNudgeFiresOnce(t *testing.T) {
	ctx := context.Background()
	tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
	recordSessionSpend(t, tracker, "s1", 4.1)
	mgr := spendRunwayMgr(t, tracker, 5, true)
	sess := &api.Session{ID: "s1", ProjectID: "p1"}

	st, err := mgr.Runner.Spend.State(ctx, "s1", sess)
	testutil.FailErr(t, "spendCeilingState", err)
	if !st.Low {
		t.Fatalf("want Low at 82%% of ceiling, got %+v", st)
	}
	first := mgr.Coordinator.Nudges.SpendRunway(ctx, sess, st.CeilingUSD)
	if first.Empty() {
		t.Fatal("first crossing must fire")
	}
	for i := 0; i < 5; i++ {
		if got := mgr.Coordinator.Nudges.SpendRunway(ctx, sess, st.CeilingUSD); !got.Empty() {
			t.Fatalf("iteration %d re-fired: %q", i, got.Content)
		}
	}
}

func TestSpendRunwayUsesConfiguredWarningRatio(t *testing.T) {
	ctx := context.Background()
	tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
	recordSessionSpend(t, tracker, "s1", 2.6)
	mgr := spendRunwayMgr(t, tracker, 5, true)
	defaults := mgr.Limits.Defaults()
	defaults.SpendWarningRatio = 0.5
	mgr.Limits.SetDefaults(defaults)
	sess := &api.Session{ID: "s1", ProjectID: "p1"}

	st, err := mgr.Runner.Spend.State(ctx, "s1", sess)
	testutil.FailErr(t, "spendCeilingState", err)
	if !st.Low || st.Reached {
		t.Fatalf("state = %+v want warning at configured 50%% ratio", st)
	}
}

func TestSpendRunwayNudgeRearmsOnChangedCeiling(t *testing.T) {
	ctx := context.Background()
	tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
	recordSessionSpend(t, tracker, "s1", 4.1)
	mgr := spendRunwayMgr(t, tracker, 5, true)
	sess := &api.Session{ID: "s1", ProjectID: "p1"}

	if got := mgr.Coordinator.Nudges.SpendRunway(ctx, sess, 5); got.Empty() {
		t.Fatal("fire at $5")
	}
	if got := mgr.Coordinator.Nudges.SpendRunway(ctx, sess, 5); !got.Empty() {
		t.Fatalf("same ceiling re-armed: %q", got.Content)
	}

	recordSessionSpend(t, tracker, "s1", 12) // total ~16.1 against new $20 ceiling
	defaults := mgr.Limits.Defaults()
	defaults.SessionSpendCeilingUSD = 20
	mgr.Limits.SetDefaults(defaults)
	st, err := mgr.Runner.Spend.State(ctx, "s1", sess)
	testutil.FailErr(t, "spendCeilingState@$20", err)
	if !st.Low || st.CeilingUSD != 20 {
		t.Fatalf("state at $20 = %+v", st)
	}
	if got := mgr.Coordinator.Nudges.SpendRunway(ctx, sess, st.CeilingUSD); got.Empty() {
		t.Fatal("changed ceiling must fire once more")
	}
	if got := mgr.Coordinator.Nudges.SpendRunway(ctx, sess, st.CeilingUSD); !got.Empty() {
		t.Fatalf("second fire at $20: %q", got.Content)
	}

	// A lower ceiling uses a fresh session below the limit.
	tracker2 := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
	recordSessionSpend(t, tracker2, "s2", 2.5)
	mgr2 := spendRunwayMgr(t, tracker2, 5, true)
	sess2 := &api.Session{ID: "s2", ProjectID: "p1"}
	if got := mgr2.Nudges.SpendRunway(ctx, sess2, 5); got.Empty() {
		t.Fatal("fire at $5 on s2")
	}
	defaults2 := mgr2.Limits.Defaults()
	defaults2.SessionSpendCeilingUSD = 3
	mgr2.Limits.SetDefaults(defaults2)
	st, err = mgr2.Runner.Spend.State(ctx, "s2", sess2)
	testutil.FailErr(t, "spendCeilingState@$3", err)
	if !st.Low || st.Reached || st.CeilingUSD != 3 {
		t.Fatalf("state at lower ceiling = %+v", st)
	}
	if got := mgr2.Nudges.SpendRunway(ctx, sess2, st.CeilingUSD); got.Empty() {
		t.Fatal("lower positive ceiling must re-arm")
	}
}

func TestSpendRunwayNudgeNeverAtWall(t *testing.T) {
	ctx := context.Background()
	tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
	recordSessionSpend(t, tracker, "s1", 5.5)
	mgr := spendRunwayMgr(t, tracker, 5, true)
	sess := &api.Session{ID: "s1", ProjectID: "p1"}

	st, err := mgr.Runner.Spend.State(ctx, "s1", sess)
	testutil.FailErr(t, "spendCeilingState", err)
	if !st.Reached || st.Low {
		t.Fatalf("state = %+v want reached and not low", st)
	}
	if err := mgr.Runner.Spend.Check(ctx, "s1", sess); err == nil {
		t.Fatal("reached must surface as checkSpendCeiling error")
	}
}

func TestSpendRunwayNudgeNeverForWorker(t *testing.T) {
	ctx := context.Background()
	tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
	recordSessionSpend(t, tracker, "root", 4.1)
	mgr := spendRunwayMgr(t, tracker, 5, true)
	root := &api.Session{ID: "root", ProjectID: "p1"}
	worker := &api.Session{ID: "child", ProjectID: "p1", ParentSessionID: "root"}

	if got := mgr.Coordinator.Nudges.SpendRunway(ctx, worker, 5); !got.Empty() {
		t.Fatalf("worker nudge = %q", got.Content)
	}
	if got := mgr.Coordinator.Nudges.SpendRunway(ctx, root, 5); got.Empty() {
		t.Fatal("coordinator must still be able to arm after worker no-op")
	}
}

func TestSpendRunwayUnpricedAndDisabled(t *testing.T) {
	ctx := context.Background()

	t.Run("unpriced", func(t *testing.T) {
		tracker := costtest.NewTracker(t, stubSpendPricer{unpriced: true})
		testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(ctx, cost.UsageEvent{
			SessionID: "s1", Caller: cost.CallerCoordinator, PromptTokens: 100, Unpriced: true,
		}))
		mgr := spendRunwayMgr(t, tracker, 5, true)
		sess := &api.Session{ID: "s1", ProjectID: "p1"}
		st, err := mgr.Runner.Spend.State(ctx, "s1", sess)
		testutil.FailErr(t, "spendCeilingState", err)
		if st != (spendguard.CeilingState{}) {
			t.Fatalf("unpriced state = %+v", st)
		}
		if err := mgr.Runner.Spend.Check(ctx, "s1", sess); err != nil {
			t.Fatalf("unpriced checkSpendCeiling = %v", err)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
		recordSessionSpend(t, tracker, "s2", 9)
		mgr := spendRunwayMgr(t, tracker, 5, false)
		sess := &api.Session{ID: "s2", ProjectID: "p1"}
		st, err := mgr.Runner.Spend.State(ctx, "s2", sess)
		testutil.FailErr(t, "spendCeilingState", err)
		if st != (spendguard.CeilingState{}) {
			t.Fatalf("disabled state = %+v", st)
		}
		if err := mgr.Runner.Spend.Check(ctx, "s2", sess); err != nil {
			t.Fatalf("disabled checkSpendCeiling = %v", err)
		}
	})

	t.Run("zero_ceiling", func(t *testing.T) {
		tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
		recordSessionSpend(t, tracker, "s3", 9)
		mgr := spendRunwayMgr(t, tracker, 0, true)
		sess := &api.Session{ID: "s3", ProjectID: "p1"}
		st, err := mgr.Runner.Spend.State(ctx, "s3", sess)
		testutil.FailErr(t, "spendCeilingState", err)
		if st != (spendguard.CeilingState{}) {
			t.Fatalf("zero ceiling state = %+v", st)
		}
		if err := mgr.Runner.Spend.Check(ctx, "s3", sess); err != nil {
			t.Fatalf("zero ceiling checkSpendCeiling = %v", err)
		}
	})
}

func TestSpendRunwayRenderedTextHasNoFigure(t *testing.T) {
	ctx := context.Background()
	tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
	recordSessionSpend(t, tracker, "s1", 4.1)
	mgr := spendRunwayMgr(t, tracker, 5, true)
	sess := &api.Session{ID: "s1", ProjectID: "p1"}
	msg := mgr.Coordinator.Nudges.SpendRunway(ctx, sess, 5)
	if msg.Empty() {
		t.Fatal("expected rendered message")
	}
	if regexp.MustCompile(`[0-9$%]`).MatchString(msg.Content) {
		t.Fatalf("message must carry no digit, $, or %%: %q", msg.Content)
	}
}

func TestSpendRunwayLandingRegister(t *testing.T) {
	ctx := context.Background()

	banned := []string{
		"hurry", "quickly", "immediately", "right away", "running out", "time is short",
		"highest-value", "most important", "what matters most",
	}
	noStubNeedles := []string{"stub", "placeholder", "skipped test"}

	assertLanding := func(t *testing.T, name, text string) {
		t.Helper()
		lower := strings.ToLower(text)
		for _, b := range banned {
			if strings.Contains(lower, b) {
				t.Errorf("%s contains banned %q", name, b)
			}
		}
		for _, n := range noStubNeedles {
			if !strings.Contains(lower, n) {
				t.Errorf("%s missing no-stub clause needle %q", name, n)
			}
		}
	}

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	renderKick := func(name string, data map[string]any) string {
		text, err := engine.RenderKick(ctx, name, data)
		testutil.FailErr(t, "RenderKick "+name, err)
		return strings.TrimSpace(text)
	}
	assertLanding(t, "coordinator-spend-runway-low", renderKick("coordinator-spend-runway-low", nil))
	assertLanding(t, "coordinator-spend-soft-stop", renderKick("coordinator-spend-soft-stop", nil))
	assertLanding(t, "coordinator-iterations-low", renderKick("coordinator-iterations-low", map[string]any{
		"remaining": 10,
	}))

	render := func(ctx context.Context, kickID string, data map[string]any) (string, error) {
		return engine.RenderKick(ctx, kickID, data)
	}
	worker := workercloseout.RenderWorkerKick(ctx, render, anchor.InformRender(anchor.WorkerIterationsLow), map[string]any{
		"remaining": 10, "max_tool_loops": 40, "at_host_max": false, "request_open": false,
	})
	assertLanding(t, "worker-iterations-low", worker)
}

func TestSpendRunwayLatchReleasedOnForget(t *testing.T) {
	ctx := context.Background()
	tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
	recordSessionSpend(t, tracker, "s1", 4.1)
	mgr := spendRunwayMgr(t, tracker, 5, true)
	sess := &api.Session{ID: "s1", ProjectID: "p1"}

	if got := mgr.Coordinator.Nudges.SpendRunway(ctx, sess, 5); got.Empty() {
		t.Fatal("first fire")
	}
	mgr.Resources.Dispose(t.Context(), "s1")
	if got := mgr.Coordinator.Nudges.SpendRunway(ctx, sess, 5); got.Empty() {
		t.Fatal("re-created session with same ceiling must arm again")
	}
}
