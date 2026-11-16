package conditions

import (
	"context"
	"github.com/lycaon/lycaon/internal/evidence"

	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/pkg/api"
)

// AlwaysPassEvidence satisfies evidence_passed:* in unit and security tests.
type AlwaysPassEvidence struct{}

func (AlwaysPassEvidence) LatestEvidence(_ context.Context, _, _, _ string, _ evidence.GateType, _ inspector.EvidenceScope) (*evidence.Record, error) {
	return &evidence.Record{GateVerdict: string(evidence.GateVerdictApproved)}, nil
}

// TestPlanContentWithTasks is canonical plan markdown for plan_stub_valid and downstream gate tests.
const TestPlanContentWithTasks = "---\ntitle: Ship it\nresearch_depth: none\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** small\n\n## Plan breaking changes\n\nNone.\n\n## Approach\n\nship it\n\n<!-- lycaon:tasks\n[{\"id\":\"t1\",\"title\":\"Implement\",\"files\":[],\"verify\":[]}]\n-->"

// TestPlanContentStubOnly satisfies plan_stub_valid without the lycaon:tasks decomposition marker.
const TestPlanContentStubOnly = "---\ntitle: Ship it\nresearch_depth: none\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** small\n\n## Plan breaking changes\n\nNone.\n\n## Approach\n\nship it"

// TestPlanContentFullSections adds later-phase sections for catalog gate tests.
const TestPlanContentFullSections = TestPlanContentWithTasks + "\n\n## Plan review depth\n\nstandard"

// TestRegistryDepsWithEvidence wires closeout and verify evidence for compound gates.
func TestRegistryDepsWithEvidence() RegistryDeps {
	return RegistryDeps{
		BlueprintGet: func(_ context.Context, blueprintPath string) (*api.Blueprint, error) {
			return &api.Blueprint{Path: blueprintPath, Content: TestPlanContentWithTasks}, nil
		},
		PlanCloseout:   func(context.Context, string) (bool, error) { return true, nil },
		SessionWorkIDs: func(context.Context, string) ([]string, error) { return []string{"leg-1"}, nil },
		Evidence:       AlwaysPassEvidence{},
	}
}
