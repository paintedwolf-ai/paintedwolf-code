package orchestration

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/llm"
)

// ModelAssignment records the pool model chosen for one parallel topology leg.
type ModelAssignment struct {
	ProviderID string
	Model      string
}

// ModelGroupSelector picks model pool assignments for a group of parallel legs.
type ModelGroupSelector interface {
	SelectGroup(ctx context.Context, projectDir string, n int) ([]ModelAssignment, error)
}

// StaticModelGroupSelector wraps StaticModelRouter for orchestrator pool runs.
type StaticModelGroupSelector struct {
	Router *llm.StaticModelRouter
}

// SelectGroup delegates to the agent pool SelectGroup API.
func (s *StaticModelGroupSelector) SelectGroup(ctx context.Context, projectDir string, n int) ([]ModelAssignment, error) {
	if s == nil || s.Router == nil {
		return nil, fmt.Errorf("model group selector not configured")
	}
	selections, err := s.Router.WithOverlayRoots([]string{projectDir}).SelectGroup(ctx, n)
	if err != nil {
		return nil, err
	}
	out := make([]ModelAssignment, len(selections))
	for i, sel := range selections {
		out[i] = ModelAssignment{ProviderID: sel.ProviderID, Model: sel.Model}
	}
	return out, nil
}
