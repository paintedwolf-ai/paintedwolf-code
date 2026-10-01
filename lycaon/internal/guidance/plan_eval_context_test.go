package guidance_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
)

const planPhase0Missing = `---
title: Ship it
research_depth: light
---
## Goal

x

## Assumptions

x
`

const planWithScopeBreaking = planPhase0Missing + `

## Plan implementation scope

**Size:** medium

## Plan breaking changes

none
`

const planWithApproach = planWithScopeBreaking + `

## Approach

Ship incrementally
`

const planWithReviewDepth = planWithApproach + `

## Plan review depth

dual
`

func TestComputePlanProgress_Phases1To6(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		markdown  string
		flags     guidance.PlanEvalFlags
		snap      guidance.DispatchSnapshot
		wantPhase int
		wantName  string
	}{
		{"empty markdown phase 1", "", guidance.PlanEvalFlags{}, guidance.DispatchSnapshot{}, 1, guidance.PlanPhaseNames()[1]},
		{"scope missing phase 0", planPhase0Missing, guidance.PlanEvalFlags{}, guidance.DispatchSnapshot{}, 0, guidance.PlanPhaseNames()[0]},
		{"stub valid scope phase 2 research gate", planWithApproach, guidance.PlanEvalFlags{}, guidance.DispatchSnapshot{}, 2, guidance.PlanPhaseNames()[2]},
		{"research skipped advances past 2", planWithApproach, guidance.PlanEvalFlags{ResearchSkipped: true}, guidance.DispatchSnapshot{}, 4, guidance.PlanPhaseNames()[4]},
		{"research dispatched closes phase 2", planWithApproach, guidance.PlanEvalFlags{}, guidance.DispatchSnapshot{ResearchDispatches: 1}, 4, guidance.PlanPhaseNames()[4]},
		{"incomplete stub stays phase 1", planWithScopeBreaking, guidance.PlanEvalFlags{ResearchSkipped: true}, guidance.DispatchSnapshot{}, 1, guidance.PlanPhaseNames()[1]},
		{"approach present phase 4", planWithApproach, guidance.PlanEvalFlags{ResearchSkipped: true}, guidance.DispatchSnapshot{}, 4, guidance.PlanPhaseNames()[4]},
		{"review depth present phase 5", planWithReviewDepth, guidance.PlanEvalFlags{ResearchSkipped: true}, guidance.DispatchSnapshot{}, 5, guidance.PlanPhaseNames()[5]},
		{"critics satisfied phase 6", planWithReviewDepth, guidance.PlanEvalFlags{ResearchSkipped: true}, guidance.DispatchSnapshot{CriticDispatches: 2}, 6, guidance.PlanPhaseNames()[6]},
		{"critics skipped phase 6", planWithReviewDepth, guidance.PlanEvalFlags{ResearchSkipped: true, ReviewSkipped: true}, guidance.DispatchSnapshot{}, 6, guidance.PlanPhaseNames()[6]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := guidance.ComputePlanProgress(tc.markdown, tc.flags, tc.snap)
			if got.PhaseInferred != tc.wantPhase {
				t.Fatalf("phase = %d want %d checklist:\n%s", got.PhaseInferred, tc.wantPhase, got.ProgressChecklist)
			}
			if got.PhaseInferredName != tc.wantName {
				t.Fatalf("name = %q want %q", got.PhaseInferredName, tc.wantName)
			}
			if got.ChecklistHash == "" {
				t.Fatal("expected non-empty checklist_hash")
			}
			if strings.TrimSpace(got.ProgressChecklist) == "" {
				t.Fatal("expected progress_checklist")
			}
		})
	}
}

func TestComputePlanProgress_FromVars(t *testing.T) {
	t.Parallel()
	vars := map[string]any{"phase_skipped": map[string]any{"research": true}}
	progress := guidance.ComputePlanProgress(planWithApproach, guidance.PlanEvalFlagsFromVars(vars), guidance.DispatchFromVars(vars))
	if progress.PhaseInferred != 4 {
		t.Fatalf("phase = %d want 4 (review depth missing)", progress.PhaseInferred)
	}
	if len(progress.SkippedPhases) == 0 || progress.SkippedPhases[0] != "2" {
		t.Fatalf("skipped = %v", progress.SkippedPhases)
	}
}

func TestPlanEvalFlagsFromVars_SkipPhases(t *testing.T) {
	t.Parallel()
	vars := map[string]any{"phase_skipped": map[string]any{"review": true}}
	flags := guidance.PlanEvalFlagsFromVars(vars)
	if !flags.ReviewSkipped {
		t.Fatal("expected review skipped")
	}
}
