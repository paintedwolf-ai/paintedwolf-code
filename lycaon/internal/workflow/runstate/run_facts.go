package runstate

import (
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// IsAmbientRun identifies an ambient root from its persisted attachment policy.
func IsAmbientRun(run *api.WorkflowRun) bool {
	if run == nil || workflowdef.RunHasParent(run) {
		return false
	}
	return strings.TrimSpace(run.AttachPolicy) == string(workflowdef.AttachPolicySessionCreate)
}

const ExitReasonSupersededByWorkflowStart = "superseded_by_workflow_start"
