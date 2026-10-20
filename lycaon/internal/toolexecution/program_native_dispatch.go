package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/capabilityrequest"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsecrets"
)

// ProgramProgressFacts reads live state from the existing session/workflow owner.
type ProgramProgressFacts interface {
	ProgramProgressState(context.Context, string) (progress.PrerequisiteState, error)
}

// NativeInvocationResult preserves reached execution separately from mutations.
// An invoked read can report NoEffect; it still forbids claiming zero execution.
type NativeInvocationResult struct {
	OwnerInvoked      bool
	HostStopCode      string
	OutputPublishable bool
	Producer          toolcontract.ProducerResult
}

// NativeEffectStart commits the existing invocation owner's start fact before
// any typed owner executes. Failure prevents dispatch. Settlement remains with
// the recorder and cannot overwrite the returned domain outcome.
type NativeEffectStart interface {
	BeforeNativeOwner(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error
}

// PreparedNativeEffect binds actual reviewed/resolved inputs into one complete
// typed owner operation. Preparation parses/binds only; it performs no program
// mutation, external launch or egress. Embedded programs are classified here.
type PreparedNativeEffect struct {
	Operation   toolcontract.BoundOperation
	InvokeOwner func(context.Context, tools.ToolContext) (toolcontract.ProducerResult, error)
}

// NativeEffect binds one reached operation to its immutable review projection.
// PrepareReviewed decodes actual reviewed/resolved values into the existing typed
// operation. It must not reuse the adapter's stale pre-review input.
type NativeEffect struct {
	Operation       toolcontract.BoundOperation
	Stage           toolcontract.ExecutionStage
	Arguments       map[string]any
	PrepareReviewed func(context.Context, map[string]any, tools.ToolContext) (PreparedNativeEffect, error)
}

// InvokeNative is reached-effect admission, not an interpreter or JSON tool call.
// The introducing entry validates the complete syntax/progress envelope first.
// A held review keeps the same child identity; a fresh invocation uses a new one.
func (e *Executor) InvokeNative(ctx context.Context, frame ProgramFrame, surface ProgramSurface, admission ProgramStateAdmission, progressFacts ProgramProgressFacts, observer NativeEffectStart, effect NativeEffect, tc tools.ToolContext) (result NativeInvocationResult, err error) {
	bound := effect.Operation
	definition, invocation := bound.Definition(), bound.Invocation()
	facts := definition.Facts()
	boundaryStarted := false
	ownerStarted := false
	defer func() {
		completeNativeRejection(bound, err)
		if !ownerStarted {
			certainty := toolcontract.NoEffect
			if boundaryStarted {
				certainty = toolcontract.EffectUnknown
			}
			result.Producer = nativeAdmissionFailure(err, certainty)
		}
	}()
	if e == nil || observer == nil || effect.PrepareReviewed == nil || tc.Identity.Agent == "" || tc.Identity.Agent != surface.profile {
		return result, fmt.Errorf("native admission requires executor, owner and captured profile")
	}
	if err = surface.ValidateOperation(bound); err != nil {
		return result, err
	}
	if effect.Stage.ExecutionID != invocation.Identity.ExecutionID || effect.Stage.OperationID != facts.Identity || len(effect.Stage.Argv) == 0 || !nativeCommandPrefix(bound.Command(), effect.Stage.Argv) {
		return result, fmt.Errorf("native stage does not match bound operation")
	}
	tc, err = frame.ChildContext(ctx, definition, invocation, tc)
	if err != nil {
		return result, err
	}
	if !facts.Contract.AdmitsSession(tc.Identity.ParentSessionID != "") {
		return result, fmt.Errorf("native operation is outside its declared session scope")
	}
	arguments, err := prepareNativeArguments(ctx, admission, effect.Arguments)
	if err != nil {
		return result, err
	}
	defer arguments.close()
	stages, err := PrepareNativeStages(ctx, admission, []toolcontract.ExecutionStage{effect.Stage})
	if err != nil {
		return result, err
	}
	defer stages.Close()
	projection, err := stages.PolicyContext(platform.PolicyContext{})
	if err != nil {
		return result, err
	}
	tc.Invocation.NativeOperation, tc.Invocation.NativeStages = &bound, projection.BoundStages
	ctx = invocationContext(ctx, tc)
	if err = checkLiveProgramProgress(ctx, progressFacts, definition, invocation); err != nil {
		return result, err
	}
	if reject := capabilityrequest.ValidateCapabilityContract(facts.Contract, arguments.values); reject != nil {
		return result, reject
	}
	if reject := tools.ValidateCapabilityPathAuthority(arguments.values, tc.Host.SessionScratchDir); reject != nil {
		return result, reject
	}
	var schema map[string]any
	if err = json.Unmarshal(facts.Arguments, &schema); err != nil {
		return result, err
	}
	if reject := tools.ValidateCallArguments(facts.Identity, arguments.values, schema, tc); reject != nil {
		return result, reject
	}
	if err = validateNativeSecretSlots(facts.Contract, arguments.values); err != nil {
		return result, err
	}
	secretUse, err := parseSecretUse(facts.Contract, arguments.values)
	if err != nil {
		return result, err
	}
	ctx = withSecretUse(ctx, secretUse)
	defer func() { tc.Effects.Secrets.Finish(ctx) }()
	boundaryStarted = true
	canonical, tc, err := e.applyPreInvokeBoundary(ctx, facts.Identity, surface.profile, arguments.values, tc)
	if err != nil {
		return result, err
	}
	reviewed, err := freezeReviewedOperationInputs(canonical)
	if err != nil {
		return result, err
	}
	tc.Invocation.ArgsDigest = reviewed.ArgumentsDigest
	ctx, resolved, err := e.prepareOwnerDispatch(ctx, facts.Identity, surface.profile, canonical, &tc)
	if err != nil {
		return result, err
	}
	prepared, err := effect.PrepareReviewed(ctx, resolved, tc)
	if err != nil {
		return result, err
	}
	if prepared.InvokeOwner == nil || prepared.Operation.Definition().Digest() != definition.Digest() || prepared.Operation.Invocation() != invocation || prepared.Operation.Command() != bound.Command() {
		return result, fmt.Errorf("reviewed native binding changed operation attribution")
	}
	bound = prepared.Operation
	tc.Invocation.NativeOperation = &bound
	// Resume against current profile/root/workflow facts, while keeping the exact
	// prepared inputs and existing once permits. Complete owners recheck effects.
	current, err := e.Metadata.FreezeProgramSurface(ctx, platform.ToolFilter{ProfileID: surface.profile, SurfaceID: tc.Turn.TurnSurfaceID}, []toolcontract.OperationDefinition{definition})
	if err != nil {
		return result, err
	}
	if err = current.ValidateOperation(bound); err != nil {
		return result, err
	}
	if err = frame.validateScope(invocation.Scope, tc); err != nil {
		return result, err
	}
	if err = tools.ValidateWorkerBranch(ctx, tc); err != nil {
		return result, err
	}
	if err = checkLiveProgramProgress(ctx, progressFacts, definition, invocation); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = observer.BeforeNativeOwner(ctx, bound, tc.Identity.ToolCallID, reviewed); err != nil {
		return result, err
	}
	ownerStarted, result.OwnerInvoked = true, true
	result.Producer, err = prepared.InvokeOwner(ctx, tc)
	err = toolsecrets.ReferenceEchoesInError(tc.Effects.Secrets, err)
	if invalid := result.Producer.Validate(); invalid != nil {
		return result, errors.Join(err, invalid)
	}
	if e.Rejections.takeCapabilityDenial(tc.Identity.SessionID, tc.Identity.ToolCallID, tc.EffectActionID()) {
		result.HostStopCode = approvaloutcome.CodeApprovalDenied
		err = errors.Join(err, e.Approvals.approvalRefusal(approvaloutcome.CodeApprovalDenied))
	}
	if rejected := toolrejection.AsToolReject(err); rejected != nil {
		rejected = toolrejection.CompleteFailureMetadata(rejected, facts.Identity, facts.Contract.Owner)
		err = e.Rejections.settleReject(ctx, facts.Identity, surface.profile, canonical, rejected)
	}
	result.OutputPublishable = err == nil && result.HostStopCode == "" && result.Producer.PublicationEligible
	return result, err
}

func checkLiveProgramProgress(ctx context.Context, provider ProgramProgressFacts, definition toolcontract.OperationDefinition, invocation toolcontract.OperationInvocation) error {
	if provider == nil {
		return fmt.Errorf("native admission requires current progress facts")
	}
	state, err := provider.ProgramProgressState(ctx, invocation.SessionID)
	if err != nil {
		return err
	}
	return CheckOperationProgressState(definition, invocation, state)
}

func nativeAdmissionFailure(err error, certainty toolcontract.EffectCertainty) toolcontract.ProducerResult {
	result := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: certainty}
	if rejected := toolrejection.AsToolReject(err); rejected != nil {
		result.Terminal, result.StopCode = toolcontract.ProducerRefused, rejected.Code
	}
	if refusal, ok := guidance.RefusalFromError(err); ok {
		result.Terminal, result.StopCode = toolcontract.ProducerRefused, refusal.Code()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		result.Terminal = toolcontract.ProducerInterrupted
	}
	return result
}

// Command prefixes come from the immutable catalog, never parsed command prose.
func nativeCommandPrefix(command string, argv []string) bool {
	parts := strings.Split(command, " ")
	if len(argv) < len(parts) {
		return false
	}
	for i, part := range parts {
		if part == "" || argv[i] != part {
			return false
		}
	}
	return true
}
