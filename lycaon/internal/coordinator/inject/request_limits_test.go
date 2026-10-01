package inject

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowRuntimePreservesRequestLimitsAcrossPhaseKinds(t *testing.T) {
	for _, kind := range []string{"review_loop", "proof", "human_approval", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			request := "Review local files only. Do not browse the web or change files."
			block := renderTestActiveWorkflow(t, api.CoordinatorRunContext{
				WorkflowID: "review", CurrentPhase: "work", RunStatus: "running",
				Request: &api.WorkflowRequestState{Status: "resolved", Text: request, Source: "explicit"},
			}, WorkflowRuntimeSnapshot{
				Phases:    []WorkflowPhaseRow{{ID: "work"}},
				PhaseExit: &PhaseExitView{Kind: kind, ReviewAgents: []string{"skeptic", "web-researcher"}},
			}, nil, nil)
			if !strings.Contains(block, request) {
				t.Fatalf("phase %s lost the user's original request: %q", kind, block)
			}
		})
	}
}
