package toolpolicy

import (
	"context"

	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/pkg/api"
)

// workflowfacts.ActiveWorkflowManifest holds runtime fields from the active workflow manifest.

// WorkflowView is the workflow dependency surface for tool policy.
type WorkflowView interface {
	CurrentPhase(ctx context.Context, sessionID string) string
	ActivePhaseHasReviewLoop(ctx context.Context, sessionID string) bool
	AllowedAgents(ctx context.Context, sessionID string) []string
	ActiveManifest(ctx context.Context, sessionID string) (workflowfacts.ActiveWorkflowManifest, bool)
	ScaffoldVarsForSession(ctx context.Context, sessionID string) (map[string]any, error)
	ActivePlan(ctx context.Context, sessionID string) (planID, content string, ok bool)
	GetActive(ctx context.Context, sessionID string) (*api.WorkflowRun, error)
}

// PostureRegistry resolves posture rule pack paths (profiles.PostureRegistry subset).
type PostureRegistry interface {
	RulesPaths(posture api.SessionPosture) ([]string, error)
}
