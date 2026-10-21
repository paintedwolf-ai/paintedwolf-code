package toolexecution

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/tools"
)

// RenderProgramFailure uses the actual submitted model envelope after receipt
// settlement. The entry keeps both frame and prepared feedback alive through
// delivery. This function renders guidance; it never executes a suggested action.
func (e *Executor) RenderProgramFailure(ctx context.Context, frame ProgramFrame, tc tools.ToolContext, prepared *PreparedProgramFailure) error {
	if e == nil || prepared == nil || ctx == nil {
		return fmt.Errorf("program rejection rendering requires executor, context and prepared feedback")
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.closed || prepared.Rejection == nil || prepared.memory == nil || prepared.identity != tc.Identity {
		return fmt.Errorf("program feedback is closed or belongs to another invocation")
	}
	if _, err := frame.ChildContext(ctx, prepared.operation.Definition(), prepared.operation.Invocation(), tc); err != nil {
		return err
	}
	end, err := frame.beginUse()
	if err != nil {
		return err
	}
	defer end()
	submitted, release, err := programModelArguments(ctx, frame, prepared.memory, tc)
	if err != nil {
		return err
	}
	defer release()

	ctx = invocationContext(ctx, tc)
	if e.Rejections != nil && e.Rejections.blockPlane != nil {
		if err := e.Rejections.blockPlane.RejectNativeObservation(ctx, tc.Identity.Agent, submitted, prepared.operation, prepared.Rejection); err != nil {
			return err
		}
	}
	return e.Rejections.renderReject(prepared.Rejection)
}
