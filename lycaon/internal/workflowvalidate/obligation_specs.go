package workflowvalidate

import (
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/workflow"
)

// shippedObligationSpecs validates the app's registered obligation kinds.
func shippedObligationSpecs() workflow.ObligationSpecs {
	return workflow.ObligationSpecs{
		scan.WorkflowObligationKind: scan.NewWorkflowObligationSpec(),
	}
}
