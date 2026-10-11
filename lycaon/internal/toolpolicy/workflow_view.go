package toolpolicy

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// WorkflowSnapshot contains policy facts from one active workflow revision.
type WorkflowSnapshot struct {
	Phase            string
	AllowedAgents    []string
	ManifestRules    []string
	ReviewLoopActive bool
	RunID            string
	WorkflowID       string
	RunStatus        api.WorkflowRunStatus
	BlueprintPath    string
	PlanContent      string
	Vars             map[string]any
}

// WorkflowSource distinguishes an absent run from unavailable workflow state.
type WorkflowSource func(context.Context, string) (WorkflowSnapshot, error)

// PostureRegistry resolves posture rule pack paths (session.PostureRegistry subset).
type PostureRegistry interface {
	RulesPaths(posture api.SessionPosture) ([]string, error)
}
