package loopwake

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// One-second waits support short local readiness checks.
const (
	MinWaitSeconds     = 1
	DefaultWaitSeconds = 60
	MaxWaitSeconds     = 1800
)

// WaitToolResult is returned by the cross-role wait tool.
type WaitToolResult struct {
	UntilComplete bool                   `json:"until_complete,omitempty"`
	Status        string                 `json:"status"`
	LeaseID       string                 `json:"lease_id,omitempty"`
	WakeAt        string                 `json:"wake_at,omitempty"`
	Reason        string                 `json:"reason,omitempty"`
	TimeoutMS     int                    `json:"timeout_ms,omitempty"`
	Resumed       bool                   `json:"resumed,omitempty"`
	Conditions    []awaitstore.Condition `json:"conditions,omitempty"`
}

type WaitToolDeps struct {
	Store             *awaitstore.Store
	ProfileConditions map[string]map[string]bool
	SecretMatcher     *secretmatch.Matcher
	RuntimeContext    context.Context
}

type waitSubscription struct {
	Conditions         []awaitstore.Condition
	Triggers           []WaitTrigger
	ProcessHandles     []string
	WorkerHandles      []string
	ExplicitConditions bool
	UntilComplete      bool
	Bounded            bool
}

type waitRequest struct {
	waitSubscription
	Resume         bool
	TimeoutMS      int
	Reason         string
	ResumeDeadline time.Time
	// ResumeBounded carries whether the resumed wait had a deadline backstop.
	ResumeBounded bool
	ExplicitMode  bool
}

// RegisterWaitTool registers durable agent waits.
func RegisterWaitTool(reg *tools.DefaultRegistry, loop *LoopEngine, deps WaitToolDeps) error {
	if reg == nil || loop == nil {
		return fmt.Errorf("registry and loop engine required")
	}
	loop.SetWaitStore(deps.Store)
	return reg.Register("wait", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		request, err := prepareWaitRequest(ctx, loop, deps, args, tctx)
		if err != nil {
			return "", err
		}
		if request.UntilComplete {
			winner, ready := loop.processConditionOutcome(tctx.Identity.SessionID, awaitstore.Condition{
				Kind: "process_done", Handles: request.ProcessHandles,
			})
			if ready {
				out, err := surveyjson.Marshal(WaitToolResult{Status: "process_done", Conditions: []awaitstore.Condition{winner}})
				return string(out), err
			}
		}
		resume := request.Resume
		timeoutMS := request.TimeoutMS
		reason := request.Reason
		conditions := request.Conditions
		triggers := request.Triggers
		processHandles := request.ProcessHandles
		explicitConditions := request.ExplicitConditions
		triggers = ensureTimerBackstop(triggers)
		hasDeadline := args["timeout_ms"] != nil || (resume && request.ResumeBounded)
		if request.UntilComplete && !hasDeadline {
			triggers = removeWaitTrigger(triggers, WaitTriggerTimer)
		}
		triggerNames := make([]string, len(triggers))
		for i, t := range triggers {
			triggerNames[i] = string(t)
		}
		// These states require a real wait even when workers are already idle.
		batchPhase := loop.coordinatorBatchState(ctx, tctx.Identity.SessionID).Phase
		batchClosed := batchPhase == batch.PhaseClosed
		soloDurationWait := !explicitConditions && batchPhase == batch.PhasePreDispatch
		pendingUserInput := loop.sessionHasPendingUserInput(ctx, tctx.Identity.SessionID)
		if !batchClosed && !pendingUserInput && batchPhase != batch.PhasePreDispatch &&
			waitSubscribesNextWorkerDone(triggers) && loop.WorkerCycleIsIdle(ctx, tctx.Identity.SessionID) {
			return waitAlreadySatisfied(
				"next_worker_done",
				"The dispatched workers have finished.",
				triggerNames,
			)
		}
		if !batchClosed && !soloDurationWait && !pendingUserInput && waitSubscribesAllWorkersIdle(triggers) && loop.WorkerCycleIsIdle(ctx, tctx.Identity.SessionID) {
			return waitAlreadySatisfied(
				"all_workers_idle",
				"No workers are pending or running.",
				triggerNames,
			)
		}
		if !batchClosed && !pendingUserInput && waitSubscribesScanDone(triggers) && !loop.scanCycleOpen(ctx, tctx.Identity.SessionID) {
			return waitAlreadySatisfied(
				"scan_done",
				"No security scans are pending or running for this project.",
				triggerNames,
			)
		}
		if !request.UntilComplete && !batchClosed && !pendingUserInput && waitSubscribesProcessDone(triggers) && !loop.processCycleOpen(tctx.Identity.SessionID, processHandles) {
			return waitAlreadySatisfied(
				"process_done",
				"No selected commands are running for this session.",
				triggerNames,
			)
		}
		until, resumed := loop.ResolveWaitUntil(ctx, tctx.Identity.SessionID, resume && !request.ExplicitMode, time.Duration(timeoutMS)*time.Millisecond)
		if request.ResumeDeadline.After(time.Now().UTC()) {
			until, resumed = request.ResumeDeadline, true
		}
		if pendingUserInput && args["timeout_ms"] == nil {
			if pendingUntil := loop.pendingUserInputWaitDeadline(ctx, tctx.Identity.SessionID); pendingUntil.After(until) {
				until = pendingUntil
			}
		}
		if request.UntilComplete && !hasDeadline {
			until = time.Time{}
			resumed = resume
		}
		// Coordinator bounded waits keep a timer backstop.
		armedTriggers := triggers
		if strings.TrimSpace(tctx.Identity.WorkerJobID) != "" {
			// The durable worker queue schedules its own deadline.
			armedTriggers = removeWaitTrigger(armedTriggers, WaitTriggerTimer)
		}
		var leaseID string
		var lease awaitstore.Lease
		if deps.Store != nil {
			lease, err = deps.Store.Arm(ctx, awaitstore.Lease{
				SessionID: tctx.Identity.SessionID, RootSessionID: rootSessionID(tctx), ProjectID: tctx.Identity.ProjectID,
				ProjectDir: tctx.ActiveRootPath(), ToolCallID: tctx.Identity.ToolCallID, WorkerJobID: tctx.Identity.WorkerJobID,
				ProfileID: tctx.Identity.Agent, Deadline: until, UntilComplete: request.UntilComplete, Conditions: conditions,
				LoopbackPorts: append([]uint16(nil), tctx.Local.LoopbackConnectPorts...), Reason: reason,
			})
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(tctx.Identity.WorkerJobID) == "" {
				// Keep a result that settled this lease during registration.
				if winner, ok := loop.waitWinner(tctx.Identity.SessionID); ok && winner.LeaseID != lease.ID {
					loop.waitWinners.CompareAndDelete(strings.TrimSpace(tctx.Identity.SessionID), winner)
				}
			}
			leaseID = lease.ID
		}
		loop.enterSleep(ctx, tctx.Identity.SessionID, sleepArm{
			until: until, untilComplete: request.UntilComplete, reason: reason,
			triggers: armedTriggers, processHandles: processHandles, workerHandles: request.WorkerHandles, mover: SleepMoverHost,
		})
		loop.MarkWaitCalled(tctx.Identity.SessionID)
		if deps.Store != nil {
			monitorCtx := deps.RuntimeContext
			if monitorCtx == nil {
				monitorCtx = context.Background()
			}
			startConditionMonitor(monitorCtx, loop, deps.Store, lease) //nolint:contextcheck // Monitor follows application lifetime.
		}
		if tctx.Effects.Out != nil {
			tctx.Effects.Out.OwnerRef = leaseID
			tctx.Effects.Out.Completion = &api.ToolCompletion{Operation: "wait", State: "parked", ResourceKind: "wait", ResourceID: leaseID}
		}
		result := WaitToolResult{
			Status: "parked", LeaseID: leaseID, Reason: reason, UntilComplete: request.UntilComplete,
			Resumed: resumed, Conditions: conditions,
		}
		if !until.IsZero() {
			result.WakeAt = until.Format(time.RFC3339)
		}
		if !resumed && (!request.UntilComplete || hasDeadline) {
			result.TimeoutMS = timeoutMS
		}
		out, err := surveyjson.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(out), nil
	})
}

func waitRequestReject(err error) error {
	if toolrejection.AsToolReject(err) != nil {
		return err
	}
	return &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{
		"tool": "wait", "reason": err.Error(),
	}}
}

func prepareWaitRequest(ctx context.Context, loop *LoopEngine, deps WaitToolDeps, args map[string]any, tctx tools.ToolContext) (waitRequest, error) {
	request, err := parseWaitRequest(args)
	if err != nil {
		return waitRequest{}, waitRequestReject(err)
	}
	if request.Resume {
		if err := restoreWaitRequest(ctx, loop, deps.Store, tctx, &request); err != nil {
			return waitRequest{}, err
		}
	}
	if err := validateProfileConditions(tctx.Identity.Agent, request.Conditions, deps.ProfileConditions); err != nil {
		return waitRequest{}, err
	}
	if err := screenWaitURLs(ctx, deps, tctx, request.Conditions); err != nil {
		return waitRequest{}, err
	}
	if err := validateConditionAuthority(request.Conditions, tctx); err != nil {
		return waitRequest{}, err
	}
	if request.UntilComplete {
		if err := validateCompletionWait(loop, tctx.Identity.SessionID, request.Conditions); err != nil {
			return waitRequest{}, waitRequestReject(err)
		}
		if deps.Store == nil {
			return waitRequest{}, waitRequestReject(fmt.Errorf("completion waits require a durable wait store"))
		}
	}
	return request, nil
}
