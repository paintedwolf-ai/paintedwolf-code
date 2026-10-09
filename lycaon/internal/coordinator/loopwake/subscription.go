package loopwake

import (
	"fmt"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"strings"
)

// WaitTrigger names a host wake source an agent subscribed to in wait().
type WaitTrigger string

const (
	WaitTriggerTimer          WaitTrigger = "timer"
	WaitTriggerNextWorkerDone WaitTrigger = "next_worker_done"
	WaitTriggerAllWorkersIdle WaitTrigger = "all_workers_idle"
	WaitTriggerOverlayPromote WaitTrigger = "overlay_promote_pending"
	WaitTriggerScanDone       WaitTrigger = "scan_done"
	WaitTriggerProcessDone    WaitTrigger = "process_done"
	WaitTriggerHTTPReady      WaitTrigger = "http_ready"
	WaitTriggerPortReady      WaitTrigger = "port_ready"
)

// AllWaitTriggers is every trigger the host recognizes. Profile wait_conditions
// and the conditions.kind schema publish the role-specific subset.
func AllWaitTriggers() []WaitTrigger {
	return []WaitTrigger{
		WaitTriggerTimer,
		WaitTriggerNextWorkerDone,
		WaitTriggerAllWorkersIdle,
		WaitTriggerOverlayPromote,
		WaitTriggerScanDone,
		WaitTriggerProcessDone,
		WaitTriggerHTTPReady,
		WaitTriggerPortReady,
	}
}

// DefaultCoordinatorWaitTriggers selects implicit coordinator subscriptions.
func DefaultCoordinatorWaitTriggers(overlayPromoteDue bool) []WaitTrigger {
	out := []WaitTrigger{
		WaitTriggerTimer,
		WaitTriggerNextWorkerDone,
		WaitTriggerAllWorkersIdle,
	}
	if overlayPromoteDue {
		out = append(out, WaitTriggerOverlayPromote)
	}
	return out
}

// AwaitUserWaitTriggers parks until user input or an overlay is ready.
func AwaitUserWaitTriggers(overlayPromoteDue bool) []WaitTrigger {
	if overlayPromoteDue {
		return []WaitTrigger{WaitTriggerOverlayPromote}
	}
	return []WaitTrigger{}
}

// WorkflowObligationWaitTriggers bounds an unmet workflow obligation.
func WorkflowObligationWaitTriggers(overlayPromoteDue bool) []WaitTrigger {
	out := []WaitTrigger{WaitTriggerTimer}
	if overlayPromoteDue {
		out = append(out, WaitTriggerOverlayPromote)
	}
	return out
}

// HostObligationWaitTriggers waits for phase settlement without polling.
func HostObligationWaitTriggers(overlayPromoteDue bool) []WaitTrigger {
	if overlayPromoteDue {
		return []WaitTrigger{WaitTriggerOverlayPromote}
	}
	return []WaitTrigger{}
}

var validWaitTriggers = map[WaitTrigger]struct{}{
	WaitTriggerTimer:          {},
	WaitTriggerNextWorkerDone: {},
	WaitTriggerAllWorkersIdle: {},
	WaitTriggerOverlayPromote: {},
	WaitTriggerScanDone:       {},
	WaitTriggerProcessDone:    {},
	WaitTriggerHTTPReady:      {},
	WaitTriggerPortReady:      {},
}

func removeWaitTrigger(triggers []WaitTrigger, remove WaitTrigger) []WaitTrigger {
	out := make([]WaitTrigger, 0, len(triggers))
	for _, trigger := range triggers {
		if trigger != remove {
			out = append(out, trigger)
		}
	}
	return out
}

// ResolveProcessHandlesArgs normalizes the optional exact command selection for
// wait(process_done). Omitted or empty means any command job in the session.
func ResolveProcessHandlesArgs(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	var handles []string
	switch values := raw.(type) {
	case []string:
		handles = values
	case []any:
		for _, value := range values {
			handle, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("handles entries must be strings")
			}
			handles = append(handles, handle)
		}
	default:
		return nil, fmt.Errorf("handles must be an array of command handles")
	}
	return normalizeProcessHandles(handles), nil
}

func dedupeWaitTriggers(in []WaitTrigger) []WaitTrigger {
	if len(in) == 0 {
		return nil
	}
	seen := map[WaitTrigger]struct{}{}
	var out []WaitTrigger
	for _, t := range in {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func waitSubscribesAllWorkersIdle(triggers []WaitTrigger) bool {
	_, ok := waitTriggerSet(triggers)[WaitTriggerAllWorkersIdle]
	return ok
}

func waitSubscribesNextWorkerDone(triggers []WaitTrigger) bool {
	_, ok := waitTriggerSet(triggers)[WaitTriggerNextWorkerDone]
	return ok
}

func waitSubscribesScanDone(triggers []WaitTrigger) bool {
	_, ok := waitTriggerSet(triggers)[WaitTriggerScanDone]
	return ok
}

func waitSubscribesProcessDone(triggers []WaitTrigger) bool {
	_, ok := waitTriggerSet(triggers)[WaitTriggerProcessDone]
	return ok
}

// ensureTimerBackstop gives event waits a durable deadline.
func ensureTimerBackstop(triggers []WaitTrigger) []WaitTrigger {
	if len(triggers) == 0 {
		return triggers
	}
	set := waitTriggerSet(triggers)
	if _, hasTimer := set[WaitTriggerTimer]; hasTimer {
		return triggers
	}
	hasEvent := false
	for t := range set {
		if t != WaitTriggerTimer {
			hasEvent = true
			break
		}
	}
	if !hasEvent {
		return triggers
	}
	return dedupeWaitTriggers(append([]WaitTrigger{WaitTriggerTimer}, triggers...))
}

func waitTriggerSet(triggers []WaitTrigger) map[WaitTrigger]struct{} {
	set := make(map[WaitTrigger]struct{}, len(triggers))
	for _, t := range triggers {
		set[t] = struct{}{}
	}
	return set
}

type waitMatchInput struct {
	Wake            anchor.ID
	CompletingJobID string
	ProcessHandle   string
	ProcessHandles  []string
	// WorkerHandles narrows next_worker_done to the named task ids.
	WorkerHandles     []string
	CycleIdle         bool
	OverlayPromoteDue bool
	// NeedsDecision unblocks the coordinator before sibling work settles.
	NeedsDecision bool
}

func waitEventMatches(triggers []WaitTrigger, in waitMatchInput) bool {
	set := waitTriggerSet(triggers)
	switch in.Wake {
	case anchor.WaitTimerFired:
		_, ok := set[WaitTriggerTimer]
		return ok
	case anchor.ScanFinished:
		_, ok := set[WaitTriggerScanDone]
		return ok
	case anchor.ProcessFinished, anchor.ProcessRefused:
		_, ok := set[WaitTriggerProcessDone]
		return ok && handleMatches(in.ProcessHandles, in.ProcessHandle)
	case anchor.WorkerBudgetRequested:
		_, ok := workerWaitCondition(set)
		return ok
	case anchor.WorkerTaskFinished, anchor.LegFinished:
		if in.OverlayPromoteDue {
			if _, ok := set[WaitTriggerOverlayPromote]; ok {
				return true
			}
		}
		// Worker decisions bypass batch-idle parking.
		if in.NeedsDecision {
			return true
		}
		if strings.TrimSpace(in.CompletingJobID) != "" && handleMatches(in.WorkerHandles, in.CompletingJobID) {
			if _, ok := set[WaitTriggerNextWorkerDone]; ok {
				return true
			}
		}
		if in.CycleIdle {
			if _, ok := set[WaitTriggerAllWorkersIdle]; ok {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// workerWaitCondition finds the worker subscription a worker's own request
// satisfies. A budget request, like a decision, is a worker needing its
// coordinator, so any wait on workers wakes for it.
func workerWaitCondition(set map[WaitTrigger]struct{}) (WaitTrigger, bool) {
	for _, trigger := range []WaitTrigger{WaitTriggerNextWorkerDone, WaitTriggerAllWorkersIdle} {
		if _, ok := set[trigger]; ok {
			return trigger, true
		}
	}
	return "", false
}

func waitConditionForWake(triggers []WaitTrigger, in waitMatchInput) (awaitstore.Condition, bool) {
	set := waitTriggerSet(triggers)
	switch in.Wake {
	case anchor.ScanFinished:
		_, ok := set[WaitTriggerScanDone]
		return awaitstore.Condition{Kind: string(WaitTriggerScanDone)}, ok
	case anchor.ProcessFinished:
		_, ok := set[WaitTriggerProcessDone]
		ok = ok && handleMatches(in.ProcessHandles, in.ProcessHandle)
		return awaitstore.Condition{Kind: string(WaitTriggerProcessDone), Handles: []string{in.ProcessHandle}}, ok
	case anchor.ProcessRefused:
		// A refusal wakes the waiter while the job continues running.
		_, ok := set[WaitTriggerProcessDone]
		ok = ok && handleMatches(in.ProcessHandles, in.ProcessHandle)
		return awaitstore.Condition{Kind: string(WaitTriggerProcessDone), Handles: []string{in.ProcessHandle}, Outcome: "refused"}, ok
	case anchor.WorkerBudgetRequested:
		trigger, ok := workerWaitCondition(set)
		return awaitstore.Condition{Kind: string(trigger)}, ok
	case anchor.WorkerTaskFinished, anchor.LegFinished:
		if in.OverlayPromoteDue {
			if _, ok := set[WaitTriggerOverlayPromote]; ok {
				return awaitstore.Condition{Kind: string(WaitTriggerOverlayPromote)}, true
			}
		}
		if strings.TrimSpace(in.CompletingJobID) != "" && handleMatches(in.WorkerHandles, in.CompletingJobID) {
			if _, ok := set[WaitTriggerNextWorkerDone]; ok {
				return awaitstore.Condition{Kind: string(WaitTriggerNextWorkerDone), Handles: []string{strings.TrimSpace(in.CompletingJobID)}}, true
			}
		}
		if in.CycleIdle {
			if _, ok := set[WaitTriggerAllWorkersIdle]; ok {
				return awaitstore.Condition{Kind: string(WaitTriggerAllWorkersIdle)}, true
			}
		}
	default:
	}
	return awaitstore.Condition{}, false
}

func normalizeProcessHandles(handles []string) []string {
	seen := make(map[string]struct{}, len(handles))
	var out []string
	for _, handle := range handles {
		handle = strings.TrimSpace(handle)
		if handle == "" {
			continue
		}
		if _, ok := seen[handle]; ok {
			continue
		}
		seen[handle] = struct{}{}
		out = append(out, handle)
	}
	return out
}

// handleMatches reports whether completed is selected; no handles select all.
func handleMatches(wanted []string, completed string) bool {
	if len(wanted) == 0 {
		return true
	}
	completed = strings.TrimSpace(completed)
	for _, handle := range wanted {
		if handle == completed {
			return true
		}
	}
	return false
}

func shouldDeferForAllWorkersIdle(triggers []WaitTrigger, wake anchor.ID, completingJobID string) bool {
	if wake != anchor.WorkerTaskFinished && wake != anchor.LegFinished {
		return false
	}
	set := waitTriggerSet(triggers)
	if _, ok := set[WaitTriggerAllWorkersIdle]; !ok {
		return false
	}
	if _, ok := set[WaitTriggerNextWorkerDone]; ok {
		return false
	}
	return strings.TrimSpace(completingJobID) != "" || wake == anchor.WorkerTaskFinished
}

func isAlwaysWakeInform(inform anchor.ID) bool {
	switch inform {
	case anchor.GateBlocked, anchor.FeedbackPending, anchor.FeedbackReceived, anchor.ComposeDone, anchor.PhaseAdvanced:
		return true
	default:
		return false
	}
}

func isAlwaysWakeNudge(wake anchor.ID) bool {
	return wake == anchor.PhaseAdvanced
}

// Unarmed slots use host-driven sleep.

// HostWakeActionableInput carries loop wake context for skip-turn policy.
type HostWakeActionableInput struct {
	SessionID       string
	Wake            anchor.ID
	Inform          anchor.ID
	CompletingJobID string
	Env             anchor.Envelope
	// PostTurnDrain marks wakes released after a host prompt.
	PostTurnDrain bool
}
