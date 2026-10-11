package workflowvalidate

import (
	"github.com/lycaon/lycaon/internal/scan"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
)

// shippedObligationSpecs validates the app's registered obligation kinds.
func shippedObligationSpecs() workflowvalidation.ObligationSpecs {
	return workflowvalidation.ObligationSpecs{
		scan.WorkflowObligationKind: scan.NewWorkflowObligationSpec(),
	}
}
