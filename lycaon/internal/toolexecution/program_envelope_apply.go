package toolexecution

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

// ProgramProgressStart records an envelope-owner attempt before applying it.
// It never attributes the payload to an invented shell source unit or span.
type ProgramProgressStart interface {
	BeforeProgressOwner(context.Context, toolcontract.ProgramProgressIdentity, string, toolcontract.ReviewedOperationInputs) error
}

// ApplyProgramProgress uses existing policy, screening and author-update owner.
// The caller records its result even when a later program operation is denied.
// An attempted update cannot be automatically replayed through this envelope.
func (e *Executor) ApplyProgramProgress(ctx context.Context, p *PreparedProgramEnvelope, store progress.Store, facts ProgramProgressFacts, observer ProgramProgressStart, tc tools.ToolContext) (result ProgramProgressResult, err error) {
	if e == nil {
		return result, fmt.Errorf("executor required")
	}
	if err = p.validate(ctx); err != nil {
		return result, err
	}
	end, err := p.frame.beginUse()
	if err != nil {
		return result, err
	}
	defer end()
	if p.update == nil {
		return result, nil
	}
	if p.attempted || store == nil || facts == nil || observer == nil {
		return result, fmt.Errorf("progress owner, facts, recorder and unattempted envelope required")
	}
	if err = p.frame.validateHostContext(ctx, tc); err != nil {
		return result, err
	}
	if err = p.frame.validateScope(p.identity.Scope, tc); err != nil {
		return result, err
	}
	definition, ok := e.definitionFor("update_progress")
	if !ok || definition.Contract.Digest() != p.contractDigest {
		return result, fmt.Errorf("progress owner unavailable")
	}
	arguments, err := prepareNativeArguments(ctx, p.admission, p.update.values)
	if err != nil {
		return result, err
	}
	defer arguments.close()
	tc = freshProgramOwnerContext("update_progress", definition.Contract, p.identity.ExecutionID, p.identity.Scope.ProjectID, p.identity.SessionID, tc)
	ctx = invocationContext(ctx, tc)
	ctx, observation, releaseObservation, err := prepareProgramPolicyObservation(ctx, p.frame, p.admission, tc, nil)
	if err != nil {
		return result, err
	}
	defer releaseObservation()
	if reject := e.validateInvocation(ctx, "update_progress", tc.Identity.Agent, arguments.values, &tc); reject != nil {
		return result, reject
	}
	use, err := parseSecretUse(tc.Invocation.Contract, arguments.values)
	if err != nil {
		return result, err
	}
	ctx = withSecretUse(ctx, use)
	defer func() { tc.Effects.Secrets.Finish(ctx) }()
	canonical, tc, err := e.applyPreInvokeBoundary(ctx, "update_progress", tc.Identity.Agent, arguments.values, tc)
	if err != nil {
		return result, err
	}
	reviewedArguments, reviewed, err := admitReviewedOperationInputs(ctx, p.admission, canonical)
	if err != nil {
		return result, err
	}
	defer reviewedArguments.close()
	canonical = reviewedArguments.values
	ctx, resolved, err := e.prepareOwnerDispatch(ctx, "update_progress", tc.Identity.Agent, canonical, &tc, observation)
	if err != nil {
		return result, err
	}
	// The final review may have waited. Re-enter the existing policy boundary
	// against current facts; a changed reviewed object requires fresh preparation.
	rechecked, err := e.applyPreInvokePolicy(ctx, "update_progress", tc.Identity.Agent, canonical, &tc, e.Boundary.actionConfineRequest(ctx, tc), tc.HasExecutionApproval())
	if err != nil {
		return result, err
	}
	currentArguments, currentReview, err := admitReviewedOperationInputs(ctx, p.admission, rechecked)
	if err != nil {
		return result, err
	}
	defer currentArguments.close()
	if currentReview != reviewed {
		return result, fmt.Errorf("reviewed progress changed during authority recheck")
	}
	content, _ := resolved["content"].(string)
	author, err := progress.PrepareAuthorUpdate(content)
	if err != nil {
		return result, progressOwnerError(err)
	}
	if err = p.validate(ctx); err != nil {
		return result, err
	}
	if err = p.frame.validateHostContext(ctx, tc); err != nil {
		return result, err
	}
	if err = p.frame.validateScope(p.identity.Scope, tc); err != nil {
		return result, err
	}
	current, err := facts.ProgramProgressState(ctx, p.identity.SessionID)
	if err != nil {
		return result, err
	}
	if current.SessionID != p.identity.SessionID || current.RootSessionID == "" {
		return result, fmt.Errorf("progress owner root identity unavailable")
	}
	result.Identity = p.identity
	result.Identity.RootSessionID = current.RootSessionID
	tc.Invocation.ArgsDigest = reviewed.ArgumentsDigest()
	p.attempted = true
	if err = observer.BeforeProgressOwner(ctx, result.Identity, tc.Identity.ToolCallID, reviewed); err != nil {
		return result, err
	}
	if err = p.validate(ctx); err != nil {
		return result, err
	}
	latest, err := facts.ProgramProgressState(ctx, p.identity.SessionID)
	if err != nil {
		return result, err
	}
	if latest.SessionID != current.SessionID || latest.RootSessionID != current.RootSessionID {
		return result, fmt.Errorf("progress root changed before owner application")
	}
	result.OwnerInvoked = true
	result.Outcome, err = author.Apply(ctx, store, current.RootSessionID)
	return result, err
}
