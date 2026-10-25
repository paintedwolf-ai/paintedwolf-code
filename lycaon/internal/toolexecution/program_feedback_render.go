package toolexecution

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
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
	previousRelease := prepared.release
	prepared.release = func() {
		release()
		if previousRelease != nil {
			previousRelease()
		}
	}

	ctx = invocationContext(ctx, tc)
	if r.blockPlane != nil {
		var rejectErr error
		if prepared.progress {
			rejectErr = r.blockPlane.RejectObservation(ctx, "command", tc.Identity.Agent, submitted, prepared.Rejection)
		} else {
			rejectErr = r.blockPlane.RejectNativeObservation(ctx, tc.Identity.Agent, submitted, prepared.operation, prepared.Rejection)
		}
		if rejectErr != nil {
			return rejectErr
		}
	}
	return r.renderReject(prepared.Rejection)
}

// Attribution is immutable feedback provenance, not a new execution attempt.
// Changing a root or branch may be the refusal itself and cannot hide its facts.
func validateFeedbackFrame(frame ProgramFrame, prepared *PreparedProgramFailure, tc tools.ToolContext) error {
	facts := frame.lifetime.facts // caller holds beginUse through rendering
	parent := facts.parent
	if prepared.progress {
		if tc.Identity != facts.identity || prepared.Rejection.Data["program_id"] != parent.Identity.ProgramID {
			return fmt.Errorf("program feedback frame attribution changed")
		}
		if parent.Scope.Kind != toolcontract.SessionOperationScope {
			root, ok := facts.roots[parent.Scope.RootID]
			if !ok || parent.Scope.BranchID != root.branch.String() {
				return fmt.Errorf("program feedback root attribution changed")
			}
		}
		return nil
	}
	child := prepared.operation.Invocation()
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

const programFeedbackAllowance = 128 << 10

// ProgramRefusalDeliveryFailure names a bounded delivery failure without
// publishing a renderer's internal error or an unscreened diagnostic.
type ProgramRefusalDeliveryFailure struct {
	Kind     string                         `json:"kind"`
	Capacity *ProgramRefusalCapacityFailure `json:"capacity,omitempty"`
}

type ProgramRefusalCapacityFailure struct {
	LimitID      string `json:"limit_id"`
	ResourceUnit string `json:"resource_unit"`
	Maximum      int64  `json:"maximum"`
	Observed     int64  `json:"observed"`
}

type programRefusalMemo struct {
	cause    *toolrejection.ToolReject
	delivery *ProgramRefusalDelivery
	closed   bool
}

// ProgramRefusalDelivery keeps screened diagnostics and rendered feedback
// admitted until the command result has been serialized. Repeated observation
// borrows the same descriptor; Close ends that delivery for every borrower.
type ProgramRefusalDelivery struct {
	Code         string
	Feedback     error
	Details      map[string]any
	Failure      *ProgramRefusalDeliveryFailure
	failureCause error
	close        func()
	once         sync.Once
}

func (d *ProgramRefusalDelivery) Close() {
	if d != nil {
		d.once.Do(func() {
			d.Feedback, d.Details, d.Failure, d.failureCause = nil, nil, nil, nil
			if d.close != nil {
				d.close()
				d.close = nil
			}
		})
	}
}

// RenderProgramRefusal renders a reached evaluator refusal beneath its actual
// command call. It observes no native operation and grants no execution authority.
func (r *Rejections) RenderProgramRefusal(ctx context.Context, frame ProgramFrame, memory *toolcontract.ProgramMemory, screen ProgramDiagnosticScreen, tc tools.ToolContext, cause error, preparationFailure error) (delivery *ProgramRefusalDelivery, err error) {
	rejected := toolrejection.AsToolReject(cause)
	if r == nil || ctx == nil || memory == nil || screen == nil || rejected == nil || len(rejected.Code) > 1024 {
		return nil, fmt.Errorf("program refusal requires actual frame, typed cause, screening and admission")
	}
	if preparationFailure != nil {
		var capacity *toolcontract.ProgramMemoryLimitError
		if !errors.As(preparationFailure, &capacity) && !errors.Is(preparationFailure, context.Canceled) && !errors.Is(preparationFailure, context.DeadlineExceeded) {
			return nil, preparationFailure
		}
	}
	end, err := frame.beginUse()
	if err != nil {
		return nil, err
	}
	// Attribution is checked even when cancellation prevents rendering. This
	// detached context permits only feedback validation, never another effect.
	if memory != frame.lifetime.admission {
		end()
		return nil, fmt.Errorf("program refusal admission changed")
	}
	if err := frame.validateHostContext(context.WithoutCancel(ctx), tc); err != nil {
		end()
		return nil, err
	}
	if err := frame.validateScope(frame.lifetime.facts.parent.Scope, tc); err != nil {
		end()
		return nil, err
	}
	lifetime := frame.lifetime
	lifetime.feedbackMu.Lock()
	defer lifetime.feedbackMu.Unlock()
	if memo := lifetime.feedback; memo != nil {
		end()
		if memo.closed || memo.cause != rejected {
			return nil, fmt.Errorf("program refusal delivery is closed or its observation changed")
		}
		return memo.delivery, nil
	}
	if lifetime.feedbackTaken {
		end()
		return nil, fmt.Errorf("program feedback allowance is already borrowed")
	}
	lifetime.feedbackTaken = true
	memo := &programRefusalMemo{cause: rejected}
	delivery = &ProgramRefusalDelivery{Code: rejected.Code}
	memo.delivery, lifetime.feedback = delivery, memo
	var releases []func()
	releaseProjection := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
		releases = nil
	}
	delivery.close = func() {
		lifetime.feedbackMu.Lock()
		memo.closed, memo.cause, lifetime.feedbackTaken = true, nil, false
		lifetime.feedbackMu.Unlock()
		releaseProjection()
		end()
	}
	// The frame prepaid the fixed descriptor/render allowance before effects.
	// Failed preparation releases variable state immediately; the descriptor and
	// frame remain borrowed until the caller has serialized the partial result.
	defer func() {
		if err != nil {
			releaseProjection()
			delivery = memo.delivery
			delivery.failureCause = err
			delivery.Failure = programRefusalDeliveryFailure(err)
			delivery.Feedback, delivery.Details, err = nil, nil, nil
		}
	}()
	if preparationFailure != nil {
		return delivery, preparationFailure
	}
	submitted, release, err := programModelArguments(ctx, frame, memory, tc)
	if err != nil {
		return delivery, err
	}
	releases = append(releases, release)
	diagnostic, release, err := toolcontract.NewNativeValue(ctx, memory, toolcontract.DiagnosticInspectionDomain, rejected.Data)
	if err != nil {
		return delivery, err
	}
	releases = append(releases, release)
	screened, release, err := screen.ProjectOperationDiagnostic(ctx, memory, rejected.Data)
	if release != nil {
		releases = append(releases, release)
	}
	if err != nil {
		return delivery, err
	}
	if screened == nil || release == nil {
		return delivery, fmt.Errorf("program refusal screening returned no retained projection")
	}
	if int64(len(diagnostic.Canonical())) > math.MaxInt64/32 {
		return delivery, fmt.Errorf("program refusal feedback bound overflow")
	}
	release, err = memory.ReserveProgramState(ctx, 32*int64(len(diagnostic.Canonical())))
	if err != nil {
		return delivery, err
	}
	releases = append(releases, release)
	clone := *rejected
	clone.Data = screened
	clone.Data["tool"] = "command"
	delete(clone.Data, "native_operation")
	delete(clone.Data, "native_command")
	var rendered error
	ctx = invocationContext(ctx, tc)
	if r.blockPlane != nil {
		rendered = r.blockPlane.RejectObservation(ctx, "command", tc.Identity.Agent, submitted, &clone)
	}
	if rendered == nil {
		rendered = r.renderReject(&clone)
	}
	refusal, ok := guidance.RefusalFromError(rendered)
	if !ok {
		return delivery, fmt.Errorf("program refusal formatter returned no configured feedback")
	}
	rendered = refusal.WithCause(errors.Join(&clone, cause))
	delivery.Feedback, delivery.Details = rendered, clone.Data
	return delivery, nil
}

func programRefusalDeliveryFailure(cause error) *ProgramRefusalDeliveryFailure {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return &ProgramRefusalDeliveryFailure{Kind: "canceled"}
	}
	var memory *toolcontract.ProgramMemoryLimitError
	if errors.As(cause, &memory) {
		reject := toolrejection.AsToolReject(tools.CommandResourceRejection(memory))
		if reject != nil {
			id, _ := reject.Data["limit_id"].(string)
			unit, _ := reject.Data["resource_unit"].(string)
			return &ProgramRefusalDeliveryFailure{Kind: "capacity", Capacity: &ProgramRefusalCapacityFailure{LimitID: id, ResourceUnit: unit, Maximum: memory.Maximum, Observed: memory.Observed}}
		}
	}
	return &ProgramRefusalDeliveryFailure{Kind: "unavailable"}
}
