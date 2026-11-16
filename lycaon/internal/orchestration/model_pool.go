package orchestration

import (
	"context"

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
