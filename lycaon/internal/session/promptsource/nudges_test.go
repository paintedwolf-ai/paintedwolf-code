package promptsource

import (
	"context"
	"errors"
	"fmt"
	"testing"

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
