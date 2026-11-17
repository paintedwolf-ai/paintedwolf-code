package promptsource

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/turnnudges"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type nudgeSpendLimits struct{}

func (nudgeSpendLimits) Effective(context.Context, *api.Session) settings.SessionLimits {
	return settings.SessionLimits{SpendCeilingEnabled: true, SessionSpendCeilingUSD: 1}
}

type nudgeSpendCosts struct {
	t       *testing.T
	session string
	nanoUSD int64
}

func (c *nudgeSpendCosts) Summary(ctx context.Context, scope api.CostScope, id, project string) (api.CostSummary, error) {
	if ctx != c.t.Context() || scope != api.CostScopeSession || id != c.session || project != "" {
		c.t.Fatalf("spend query lost turn context or billing scope: %v %q %q", scope, id, project)
	}
	return api.CostSummary{EstimatedNanoUsd: c.nanoUSD, EstimateCoverage: api.CostEstimateComplete}, nil
}

func (*nudgeSpendCosts) ClaimSpendWarning(context.Context, string, float64) (bool, error) {
	return false, nil
}

func (*nudgeSpendCosts) ClearSpendWarning(context.Context, string) error { return nil }

func TestNudgesClassifiesActualSpendCeilingAndRetainsRunway(t *testing.T) {
	memory := store.NewMemory()
	session, err := memory.Create(t.Context(), api.CreateSessionRequest{}, "project-spend")
	testutil.FailErr(t, "create spend session", err)
	costs := &nudgeSpendCosts{t: t, session: session.ID, nanoUSD: 900_000_000}
	deps := (&Nudges{
		Spend:  spendguard.New(memory, nudgeSpendLimits{}, costs),
		Nudges: &turnnudges.Service{},
	}).Build()
	check, err := deps.CheckSpendCeiling(t.Context(), session.ID, session)
	testutil.FailErr(t, "check remaining spend runway", err)
	if !check.Runway.Low || check.Runway.CeilingUSD != 1 || deps.IsSpendCeiling(err) {
		t.Fatalf("remaining runway misclassified: %+v, error %v", check, err)
	}
	costs.nanoUSD = 1_250_000_000
	_, err = deps.CheckSpendCeiling(t.Context(), session.ID, session)
	var reached *spendguard.CeilingReached
	if !errors.As(err, &reached) || reached.SpentUSD != 1.25 || reached.CeilingUSD != 1 || reached.Coverage != api.CostEstimateComplete {
		t.Fatalf("ceiling refusal lost spend facts: %v (%+v)", err, reached)
	}
	if !deps.IsSpendCeiling(fmt.Errorf("turn refused: %w", err)) {
		t.Fatalf("wrapped actual ceiling refusal was not classified: %v", err)
	}
	if deps.IsSpendCeiling(errors.New(err.Error())) {
		t.Fatal("diagnostic prose was mistaken for a structured spend refusal")
	}
}

type stubCycleLedger struct {
	tasks map[string]*api.WorkerTask
}

func (s stubCycleLedger) ListBySession(context.Context, string, string, ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return nil, nil
}

func (s stubCycleLedger) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}

func (s stubCycleLedger) Get(id string) (*api.WorkerTask, bool) {
	task, ok := s.tasks[id]
	return task, ok
}

type stubRoundUsage struct {
	usage cost.SessionUsage
	err   error
	since time.Time
}

func (s *stubRoundUsage) SessionUsageSince(_ context.Context, _ string, since time.Time) (cost.SessionUsage, error) {
	s.since = since
	return s.usage, s.err
}

func TestWorkerRoundConsumptionReportsMeasuredAndFallbackFacts(t *testing.T) {
	created := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	started := created.Add(2 * time.Minute)
	ledger := stubCycleLedger{
		tasks: map[string]*api.WorkerTask{
			"job-started": {ID: "job-started", AgentType: "implementer", CreatedAt: created, StartedAt: &started},
			"job-created": {ID: "job-created", AgentType: "repo-researcher", CreatedAt: created},
		},
	}
	usage := &stubRoundUsage{
		usage: cost.SessionUsage{Tokens: 4200, SpendNanoUSD: 85_000_000, Priced: true},
	}
	var gotParent string
	var gotFacts []kick.WorkerRoundNoticeFacts
	nudges := &Nudges{
		Nudges:     &turnnudges.Service{},
		Workers:    ledger,
		RoundUsage: usage,
		RoundNotice: func(_ context.Context, parentID string, facts kick.WorkerRoundNoticeFacts) {
			gotParent = parentID
			gotFacts = append(gotFacts, facts)
		},
	}
	deps := nudges.Build()
	child := &api.Session{ID: "child-1", ParentSessionID: "parent-1"}

	deps.WorkerRoundConsumption(t.Context(), nil, "job-started", 25)
	deps.WorkerRoundConsumption(t.Context(), child, "missing-job", 25)
	if len(gotFacts) != 0 {
		t.Fatalf("unexpected notice for nil session or unknown job: %+v", gotFacts)
	}

	deps.WorkerRoundConsumption(t.Context(), child, "job-started", 25)
	if gotParent != "parent-1" || len(gotFacts) != 1 || !usage.since.Equal(started) {
		t.Fatalf("started job notice = %q %+v since=%v", gotParent, gotFacts, usage.since)
	}
	if gotFacts[0].Tokens != 4200 || gotFacts[0].SpendNanoUSD != 85_000_000 || !gotFacts[0].SpendPriced || gotFacts[0].ModelCalls != 25 {
		t.Fatalf("measured notice facts = %+v", gotFacts[0])
	}

	usage.err = errors.New("ledger busy")
	deps.WorkerRoundConsumption(t.Context(), child, "job-created", 50)
	if len(gotFacts) != 2 || !usage.since.Equal(created) || gotFacts[1].Tokens != 0 || gotFacts[1].ModelCalls != 50 {
		t.Fatalf("fallback notice facts = %+v since=%v", gotFacts, usage.since)
	}
}
