package workflow

import workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"

func reviewLoopTestManifest() workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "rltest",
		Version: "1.0.0",
		// Host auto-advances on gate satisfaction so RecordReviewLoopVerdict's TryAutoAdvance lands.
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "judge",
				CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates:        []string{"evidence_passed:rl_key"},
				Next:         "done",
				ReviewLoop: &workflowdef.ReviewLoopDef{
					EvidenceKey:  "rl_key",
					IterationCap: 2,
					VerdictSchema: map[string]string{
						"verdict": "SELECTED|NEEDS_REVISION",
						"winner":  "string",
					},
				},
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
}

func ifSpawnableReviewManifest() workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:       "rlspawn",
		Version:  "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "challenge",
				CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates:        []string{"evidence_passed:survey_challenged"},
				Next:         "done",
				ReviewLoop: &workflowdef.ReviewLoopDef{
					EvidenceKey:    "survey_challenged",
					IterationCap:   1,
					RequiredAgents: []string{"skeptic"},
					IfSpawnable:    []string{"web-researcher"},
					VerdictSchema:  map[string]string{"verdict": "CHALLENGED"},
				},
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
}
