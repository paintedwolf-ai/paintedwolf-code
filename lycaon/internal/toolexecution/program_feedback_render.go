package toolexecution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	model, err := frame.ModelInvocation(ctx)
	if err != nil {
		return err
	}
	if model.ToolName != "command" || model.ToolCallID != tc.Identity.ToolCallID {
		return fmt.Errorf("program feedback model call changed")
	}
	// JSON is an immutable frame snapshot. Charge decoded containers separately;
	// operation arguments never masquerade as this submitted argument object.
	release, err := prepared.memory.ReserveProgramState(ctx, int64(len(model.CanonicalArguments))*32+4096)
	if err != nil {
		return err
	}
	if release == nil {
		return fmt.Errorf("model feedback observation returned no state lease")
	}
	defer release()
	var submitted map[string]any
	decoder := json.NewDecoder(strings.NewReader(model.CanonicalArguments))
	decoder.UseNumber()
	if err := decoder.Decode(&submitted); err != nil || submitted == nil {
		return fmt.Errorf("captured model invocation is not an argument object")
	}
	ctx = invocationContext(ctx, tc)
	if e.Rejections != nil && e.Rejections.blockPlane != nil {
		if err := e.Rejections.blockPlane.RejectNativeObservation(ctx, tc.Identity.Agent, submitted, prepared.operation, prepared.Rejection); err != nil {
			return err
		}
	}
	return e.Rejections.renderReject(prepared.Rejection)
}
