package conditionstest

import (
	"context"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/pkg/api"
)

// AlwaysPassEvidence satisfies evidence_passed:* in unit and security tests.
type AlwaysPassEvidence struct{}

func (AlwaysPassEvidence) LatestEvidence(_ context.Context, _, _, _ string, _ evidence.GateType, _ inspector.EvidenceScope) (*evidence.Record, error) {
	return &evidence.Record{GateVerdict: string(evidence.GateVerdictApproved)}, nil
}

// PlanWithTasks includes a task decomposition.
const PlanWithTasks = "---\ntitle: Ship it\nresearch_depth: none\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** small\n\n## Plan breaking changes\n\nNone.\n\n## Approach\n\nship it\n\n<!-- lycaon:tasks\n[{\"id\":\"t1\",\"title\":\"Implement\",\"files\":[],\"verify\":[]}]\n-->"

// PlanStub omits task decomposition.
const PlanStub = "---\ntitle: Ship it\nresearch_depth: none\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** small\n\n## Plan breaking changes\n\nNone.\n\n## Approach\n\nship it"

// PlanWithReviewDepth includes the later plan review section.
const PlanWithReviewDepth = PlanWithTasks + "\n\n## Plan review depth\n\nstandard"

// DependenciesWithEvidence supplies approved evidence for compound gates.
func DependenciesWithEvidence() conditions.RegistryDeps {
	deps := Dependencies()
	deps.SessionWorkIDs = func(context.Context, string) ([]string, error) { return []string{"leg-1"}, nil }
	deps.Evidence = AlwaysPassEvidence{}
	return deps
}

// Dependencies supplies a plan and completed closeout.
func Dependencies() conditions.RegistryDeps {
	return conditions.RegistryDeps{
		BlueprintGet: func(_ context.Context, blueprintPath string) (*api.Blueprint, error) {
			return &api.Blueprint{Path: blueprintPath, Content: PlanWithTasks}, nil
		},
		PlanCloseout: func(context.Context, string) (bool, error) {
			return true, nil
		},
	}
}
