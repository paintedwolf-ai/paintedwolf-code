package toolpolicy

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// ActiveWorkflowManifest holds runtime fields from the active workflow manifest.
type ActiveWorkflowManifest struct {
	CoordinatorProfile string
	Rules              []string
	HostPhaseAdvance   bool
}

// WorkflowView is the workflow dependency surface for tool policy.
type WorkflowView interface {
	CurrentPhase(ctx context.Context, sessionID string) string
	ActivePhaseHasReviewLoop(ctx context.Context, sessionID string) bool
	AllowedAgents(ctx context.Context, sessionID string) []string
	ActiveManifest(ctx context.Context, sessionID string) (ActiveWorkflowManifest, bool)
	ScaffoldVarsForSession(ctx context.Context, sessionID string) (map[string]any, error)
	ActivePlan(ctx context.Context, sessionID string) (planID, content string, ok bool)
	GetActive(ctx context.Context, sessionID string) (*api.WorkflowRun, error)
}

// PostureRegistry resolves posture rule pack paths (session.PostureRegistry subset).
type PostureRegistry interface {
	RulesPaths(posture api.SessionPosture) ([]string, error)
}
