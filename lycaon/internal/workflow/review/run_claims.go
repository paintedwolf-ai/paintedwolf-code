package review

import (
	"context"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

func (m *Verdicts) VerdictRulesFor(ctx context.Context, run *api.WorkflowRun) (workflowvalidation.VerdictRules, error) {
	if m == nil || run == nil {
		return workflowvalidation.VerdictRules{}, nil
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return workflowvalidation.VerdictRules{}, err
	}
	rules := workflowvalidation.VerdictRules{KnownClaims: map[string]bool{}, Brief: manifest.ReportBrief()}
	for _, v := range workflowpresentation.ReviewVerdicts(ctx, m, run, manifest) {
		byField, err := workflowvalidation.ParseVerdictClaims(v.Def, workflowpresentation.VerdictMembers(v.Record.Artifacts))
		if err != nil {
			continue
		}
		for _, claims := range byField {
			for _, c := range claims {
				rules.KnownClaims[strings.TrimSpace(c.ID)] = true
			}
		}
	}
	return rules, nil
}

// RunClaims reconciles a run's claims from its recorded review verdicts.
func (m *Verdicts) RunClaims(ctx context.Context, run *api.WorkflowRun) ([]workflowpresentation.RunClaim, error) {
	if m == nil || run == nil {
		return nil, nil
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	return workflowpresentation.ReconcileClaims(workflowpresentation.ReviewVerdicts(ctx, m, run, manifest)), nil
}
