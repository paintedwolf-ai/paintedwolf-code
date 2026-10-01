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

// StaticDelegationStore returns one delegation for any session id.
type StaticDelegationStore struct {
	Delegation *api.Delegation
}

func (s StaticDelegationStore) DelegationBySessionID(_ string) (string, bool) {
	if s.Delegation == nil {
		return "", false
	}
	return s.Delegation.ID, true
}

func (s StaticDelegationStore) Get(_ context.Context, delegationID string) (*api.Delegation, error) {
	if s.Delegation != nil && s.Delegation.ID == delegationID {
		return s.Delegation, nil
	}
	return nil, nil
}

func (s StaticDelegationStore) ListLegs(_ context.Context, _ string) ([]api.Leg, error) {
	if s.Delegation == nil {
		return nil, nil
	}
	return s.Delegation.Legs, nil
}

// TestPlanContentWithTasks is canonical plan markdown for plan_stub_valid and downstream gate tests.
const TestPlanContentWithTasks = "---\ntitle: Ship it\nresearch_depth: none\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** small\n\n## Plan breaking changes\n\nNone.\n\n## Approach\n\nship it\n\n<!-- lycaon:tasks\n[{\"id\":\"t1\",\"title\":\"Implement\",\"files\":[],\"verify\":[]}]\n-->"

// TestPlanContentStubOnly satisfies plan_stub_valid without the lycaon:tasks decomposition marker.
const TestPlanContentStubOnly = "---\ntitle: Ship it\nresearch_depth: none\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** small\n\n## Plan breaking changes\n\nNone.\n\n## Approach\n\nship it"

// TestPlanContentFullSections adds later-phase sections for catalog gate tests.
const TestPlanContentFullSections = TestPlanContentWithTasks + "\n\n## Plan review depth\n\nstandard"

// TestRegistryDepsWithEvidence wires delegation closeout and verify evidence for compound gates.
func TestRegistryDepsWithEvidence() RegistryDeps {
	dep := &api.Delegation{
		ID: "test-dep",
		Legs: []api.Leg{{
			ID:     "leg-1",
			Status: api.LegStatusComplete,
		}},
	}
	return RegistryDeps{
		BlueprintGet: func(_ context.Context, blueprintPath string) (*api.Blueprint, error) {
			return &api.Blueprint{Path: blueprintPath, Content: TestPlanContentWithTasks}, nil
		},
		DelegationCloseout: func(context.Context, string) (bool, error) { return true, nil },
		Evidence:           AlwaysPassEvidence{},
		DelegationStore:    StaticDelegationStore{Delegation: dep},
	}
}
