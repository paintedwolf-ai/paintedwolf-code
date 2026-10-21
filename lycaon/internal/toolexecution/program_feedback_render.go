package toolexecution

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

// RenderProgramFailure uses the actual submitted model envelope after receipt
// settlement. The entry keeps both frame and prepared feedback alive through
// delivery. This function renders guidance; it never executes a suggested action.
func (r *Rejections) RenderProgramFailure(ctx context.Context, frame ProgramFrame, tc tools.ToolContext, prepared *PreparedProgramFailure) (rendered error) {
	if r == nil || prepared == nil || ctx == nil {
		return fmt.Errorf("program rejection rendering requires rejection boundary, context and prepared feedback")
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.closed || prepared.Rejection == nil || prepared.memory == nil || prepared.identity != tc.Identity {
		return fmt.Errorf("program feedback is closed or belongs to another invocation")
	}
	end, err := frame.beginUse()
	if err != nil {
		return err
	}
	defer end()
	if err := validateFeedbackFrame(frame, prepared, tc); err != nil {
		return err
	}
	// Repeated delivery borrows the same frozen result; it cannot multiply OAR
	// occurrences or retained rendered copies under one prepared state lease.
	if prepared.rendered {
		return prepared.renderedError
	}
	defer func() { prepared.rendered, prepared.renderedError = true, rendered }()
	if prepared.policy != nil {
		var renderer *oar.Renderer
		if r.blockPlane != nil {
			renderer = r.blockPlane.Renderer
		}
		return prepared.policy.RenderSettled(ctx, renderer, prepared.Rejection)
	}
	submitted, release, err := programModelArguments(ctx, frame, prepared.memory, tc)
	if err != nil {
		return err
	}
	defer release()

	ctx = invocationContext(ctx, tc)
	if r.blockPlane != nil {
		if err := r.blockPlane.RejectNativeObservation(ctx, tc.Identity.Agent, submitted, prepared.operation, prepared.Rejection); err != nil {
			return err
		}
	}
	return r.renderReject(prepared.Rejection)
}

// Attribution is immutable feedback provenance, not a new execution attempt.
// Changing a root or branch may be the refusal itself and cannot hide its facts.
func validateFeedbackFrame(frame ProgramFrame, prepared *PreparedProgramFailure, tc tools.ToolContext) error {
	facts := frame.lifetime.facts // caller holds beginUse through rendering
	child := prepared.operation.Invocation()
	parent := facts.parent
	if tc.Identity != facts.identity || child.Identity.ProgramID != parent.Identity.ProgramID || child.ActorID != parent.ActorID || child.SessionID != parent.SessionID || child.CatalogDigest != parent.CatalogDigest || child.Scope.Kind != parent.Scope.Kind || child.Scope.ProjectID != parent.Scope.ProjectID {
		return fmt.Errorf("program feedback frame attribution changed")
	}
	if child.Scope.Kind != toolcontract.SessionOperationScope {
		root, ok := facts.roots[child.Scope.RootID]
		if !ok || child.Scope.BranchID != root.branch.String() {
			return fmt.Errorf("program feedback root attribution changed")
		}
	}
	return nil
}
