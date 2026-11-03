package toolexecution

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

// ProgramOutcomeLedger retains the aggregate snapshot through feedback delivery.
type ProgramOutcomeLedger interface {
	ReadProgramOutcomeFeedback(context.Context, string, string, int, invocation.ProgramFeedbackAdmission) (*invocation.ProgramFeedbackLease, error)
}

func evaluatorOutcomeFacts(parent toolcontract.OperationInvocation, tc tools.ToolContext, projection invocation.ProgramProjection) (map[string]any, error) {
	if projection.ParentTool != "command" || projection.ParentToolCallID != tc.Identity.ToolCallID || projection.ParentSessionID != parent.SessionID || projection.Program.ID != parent.Identity.ProgramID || projection.Program.CatalogDigest != parent.CatalogDigest {
		return nil, fmt.Errorf("evaluator feedback does not match its invocation")
	}
	return programOutcomeFacts(projection)
}
