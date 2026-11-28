package pythonadmission

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

// PolicyDecision is returned by a PolicyEvaluator for a reached operation.
type PolicyDecision struct {
	Kind                pythonruntime.DecisionKind
	Condition           AdmissionCondition
	AuthoritativeTarget *pythonruntime.CanonicalTarget
	Reason              string
}

// PolicyEvaluator supplies domain-specific admission decisions (allow, deny
// with recoverable errno condition, hold for human review, or terminal stop).
type PolicyEvaluator func(ctx context.Context, op pythonruntime.ReachedOperation) PolicyDecision

// ObserverConfig configures an AdmissionObserver implementing
// pythonruntime.OperationObserver.
type ObserverConfig struct {
	ExecutionID         string
	CallID              string
	Actor               string
	View                string
	Surface             string
	ReadOnly            bool
	AllowedRoots        []string
	ProtectedPaths      []string
	MaxHostWork         uint64
	StopLatch           *pythonruntime.StopLatch
	Broker              *ApprovalBroker
	Clock               pythonruntime.Clock
	Policy              PolicyEvaluator
	Recheck             RecheckFunc
	ReservationProvider func(op pythonruntime.ReachedOperation) pythonruntime.ParkedGuestReservation
	Evidence            EvidenceSink
}

// AdmissionObserver implements pythonruntime.OperationObserver with pinned
// call/actor/view/surface identity, read-only and scope enforcement, host work
// accounting, internal held-approval suspension, and structured evidence
// recording.
type AdmissionObserver struct {
	cfg          ObserverConfig
	clock        pythonruntime.Clock
	evidence     EvidenceSink
	allowedRoots map[string]struct{}
	hostWork     atomic.Uint64
	opSeq        atomic.Uint64
}

var _ pythonruntime.OperationObserver = (*AdmissionObserver)(nil)

// NewAdmissionObserver constructs an AdmissionObserver from cfg.
func NewAdmissionObserver(cfg ObserverConfig) *AdmissionObserver {
	clk := cfg.Clock
	if clk == nil {
		clk = pythonruntime.NewSystemClock()
	}
	ev := cfg.Evidence
	if ev == nil {
		ev = NewMemoryEvidenceStore()
	}
	roots := make(map[string]struct{}, len(cfg.AllowedRoots))
	for _, r := range cfg.AllowedRoots {
		if r != "" {
			roots[r] = struct{}{}
		}
	}
	return &AdmissionObserver{
		cfg:          cfg,
		clock:        clk,
		evidence:     ev,
		allowedRoots: roots,
	}
}

// Evidence returns the configured EvidenceSink.
func (o *AdmissionObserver) Evidence() EvidenceSink {
	return o.evidence
}

// HostWorkUsed returns total host work units charged so far.
func (o *AdmissionObserver) HostWorkUsed() uint64 {
	return o.hostWork.Load()
}

// CallID returns the pinned initiating model call ID.
func (o *AdmissionObserver) CallID() string {
	return o.cfg.CallID
}

// Admit evaluates a reached host operation, internally suspending on held
// approvals, latching terminal stops, and recording structured evidence.
func (o *AdmissionObserver) Admit(ctx context.Context, op pythonruntime.ReachedOperation) (pythonruntime.OperationDecision, error) {
	if op.ExecutionID == "" {
		op.ExecutionID = o.cfg.ExecutionID
	}
	if op.OperationID == "" {
		op.OperationID = fmt.Sprintf("op-%s-%d", op.ExecutionID, o.opSeq.Add(1))
	}

	// If a terminal stop is already latched, refuse immediately so try/finally
	// cleanup writes and subsequent loop iterations never execute.
	if o.cfg.StopLatch != nil && o.cfg.StopLatch.Stopped() {
		latched := o.cfg.StopLatch.Stop()
		dec := pythonruntime.OperationDecision{
			Kind:   pythonruntime.DecisionStop,
			Stop:   latched,
			Reason: "execution already stopped by latched terminal stop",
		}
		o.recordAdmission(op, op.Target, dec)
		return dec, nil
	}

	if err := ctx.Err(); err != nil {
		dec := o.triggerConditionStop(op, ConditionApprovalCancelled, err.Error())
		o.recordAdmission(op, op.Target, dec)
		return dec, nil
	}

	if op.HostWorkCost > 0 {
		if total := o.hostWork.Add(op.HostWorkCost); o.cfg.MaxHostWork > 0 && total > o.cfg.MaxHostWork {
			reason := fmt.Sprintf("host work %d exceeded limit %d", total, o.cfg.MaxHostWork)
			dec := o.triggerConditionStop(op, ConditionWorkLimitExceeded, reason)
			o.recordAdmission(op, op.Target, dec)
			return dec, nil
		}
	}

	if cond, reason, violated := o.checkWorkspaceInvariants(op); violated {
		dec := o.triggerConditionStop(op, cond, reason)
		o.recordAdmission(op, op.Target, dec)
		return dec, nil
	}

	pol := PolicyDecision{Kind: pythonruntime.DecisionAllow}
	if o.cfg.Policy != nil {
		pol = o.cfg.Policy(ctx, op)
		if pol.Kind == "" {
			pol.Kind = pythonruntime.DecisionAllow
		}
	}

	authTarget := op.Target
	if pol.AuthoritativeTarget != nil {
		authTarget = *pol.AuthoritativeTarget
	}

	switch pol.Kind {
	case pythonruntime.DecisionAllow:
		dec := pythonruntime.OperationDecision{
			Kind:   pythonruntime.DecisionAllow,
			Reason: pol.Reason,
		}
		o.recordAdmission(op, authTarget, dec)
		return dec, nil

	case pythonruntime.DecisionDeny:
		cond := pol.Condition
		if cond == "" {
			cond = ConditionMissingFile
		}
		dec := DecisionForCondition(cond, op.OperationID, pol.Reason, o.clock.Now())
		if dec.Kind == pythonruntime.DecisionStop {
			o.latchStop(dec.Stop)
		}
		o.recordAdmission(op, authTarget, dec)
		return dec, nil

	case pythonruntime.DecisionStop:
		cond := pol.Condition
		if cond == "" {
			cond = ConditionEffectDenied
		}
		dec := o.triggerConditionStop(op, cond, pol.Reason)
		o.recordAdmission(op, authTarget, dec)
		return dec, nil

	case pythonruntime.DecisionHold:
		return o.handleHold(ctx, op, authTarget, pol.Reason)

	default:
		dec := o.triggerConditionStop(op, ConditionEffectDenied, fmt.Sprintf("unsupported policy decision %q", pol.Kind))
		o.recordAdmission(op, authTarget, dec)
		return dec, nil
	}
}

// RecordOutcome records the observed execution outcome for an admitted
// operation and charges any post-execution host work.
func (o *AdmissionObserver) RecordOutcome(_ context.Context, opID string, outcome pythonruntime.OperationOutcome) {
	if outcome.HostWorkCharged > 0 {
		if total := o.hostWork.Add(outcome.HostWorkCharged); o.cfg.MaxHostWork > 0 && total > o.cfg.MaxHostWork {
			o.latchStop(&pythonruntime.TerminalStop{
				Code:        pythonruntime.StopWorkLimitExceeded,
				Reason:      fmt.Sprintf("host work %d exceeded limit %d", total, o.cfg.MaxHostWork),
				OperationID: opID,
				LatchedAt:   o.clock.Now().UTC(),
			})
		}
	}
	o.evidence.UpdateAdmissionOutcome(opID, outcome)
}

func (o *AdmissionObserver) checkWorkspaceInvariants(op pythonruntime.ReachedOperation) (AdmissionCondition, string, bool) {
	// Immutable runtime and bundled packages namespaces are strictly read-only.
	if op.Family == pythonruntime.EffectFilesystemWrite &&
		(op.Target.Namespace == pythonruntime.NamespaceRuntime || op.Target.Namespace == pythonruntime.NamespacePackages) {
		return ConditionProtectedSink, fmt.Sprintf("writes to immutable %s namespace are forbidden", op.Target.Namespace), true
	}

	if op.Target.Namespace != pythonruntime.NamespaceWorkspace {
		return "", "", false
	}

	if hasPathEscape(op.Target.RelPath) {
		return ConditionOutOfScopePath, fmt.Sprintf("path %q escapes workspace root", op.Target.RelPath), true
	}

	if len(o.allowedRoots) > 0 {
		if _, ok := o.allowedRoots[op.Target.RootID]; !ok {
			return ConditionOutOfScopePath, fmt.Sprintf("root %q is outside allowed workspace roots", op.Target.RootID), true
		}
	}

	if o.isProtectedPath(op.Target.RelPath) {
		return ConditionProtectedSink, fmt.Sprintf("path %q is a protected sink", op.Target.RelPath), true
	}

	if o.cfg.ReadOnly && op.Family == pythonruntime.EffectFilesystemWrite {
		return ConditionReadOnlyWrite, fmt.Sprintf("workspace write %q blocked in read-only mode", op.Target.RelPath), true
	}

	return "", "", false
}

func (o *AdmissionObserver) handleHold(
	ctx context.Context,
	op pythonruntime.ReachedOperation,
	authTarget pythonruntime.CanonicalTarget,
	reason string,
) (pythonruntime.OperationDecision, error) {
	if o.cfg.Broker == nil {
		dec := o.triggerConditionStop(op, ConditionEffectDenied, "held approval requested without configured approval broker")
		o.recordAdmission(op, authTarget, dec)
		return dec, nil
	}

	var reservation pythonruntime.ParkedGuestReservation
	if o.cfg.ReservationProvider != nil {
		reservation = o.cfg.ReservationProvider(op)
	}
	if reservation.ExecutionID == "" {
		reservation.ExecutionID = op.ExecutionID
	}

	res, err := o.cfg.Broker.WaitForApproval(ctx, HoldRequest{
		ExecutionID:         op.ExecutionID,
		OperationID:         op.OperationID,
		CallID:              o.cfg.CallID,
		Actor:               o.cfg.Actor,
		View:                o.cfg.View,
		Surface:             o.cfg.Surface,
		Family:              op.Family,
		Operation:           op.Operation,
		Target:              op.Target,
		AuthoritativeTarget: authTarget,
		Args:                op.Args,
		Reason:              reason,
		Reservation:         reservation,
		StopLatch:           o.cfg.StopLatch,
		Recheck:             o.cfg.Recheck,
	})
	if err != nil {
		cond := ConditionEffectDenied
		if errors.Is(err, pythonruntime.ErrParkedBudgetExceeded) {
			cond = ConditionParkedBudgetExceeded
		}
		dec := o.triggerConditionStop(op, cond, err.Error())
		dec.ApprovalLeaseID = res.LeaseID
		o.recordAdmission(op, authTarget, dec)
		return dec, nil
	}

	switch res.State {
	case LeaseConsumed:
		dec := pythonruntime.OperationDecision{
			Kind:            pythonruntime.DecisionAllow,
			ApprovalLeaseID: res.LeaseID,
			Reason:          res.Reason,
		}
		o.recordAdmission(op, res.AuthoritativeTarget, dec)
		return dec, nil

	case LeaseDeclined:
		dec := o.triggerConditionStop(op, ConditionApprovalDeclined, res.Reason)
		dec.ApprovalLeaseID = res.LeaseID
		o.recordAdmission(op, res.AuthoritativeTarget, dec)
		return dec, nil

	case LeaseLostOnRestart:
		dec := o.triggerConditionStop(op, ConditionHostRestart, res.Reason)
		dec.ApprovalLeaseID = res.LeaseID
		o.recordAdmission(op, res.AuthoritativeTarget, dec)
		return dec, nil

	default:
		dec := o.triggerConditionStop(op, ConditionApprovalCancelled, res.Reason)
		dec.ApprovalLeaseID = res.LeaseID
		o.recordAdmission(op, res.AuthoritativeTarget, dec)
		return dec, nil
	}
}

func (o *AdmissionObserver) triggerConditionStop(op pythonruntime.ReachedOperation, cond AdmissionCondition, reason string) pythonruntime.OperationDecision {
	dec := DecisionForCondition(cond, op.OperationID, reason, o.clock.Now())
	if dec.Kind != pythonruntime.DecisionStop {
		dec = pythonruntime.OperationDecision{
			Kind:   pythonruntime.DecisionStop,
			Reason: reason,
			Stop: &pythonruntime.TerminalStop{
				Code:        pythonruntime.StopEffectNotAllowed,
				Reason:      reason,
				OperationID: op.OperationID,
				LatchedAt:   o.clock.Now().UTC(),
			},
		}
	}
	o.latchStop(dec.Stop)
	return dec
}

func (o *AdmissionObserver) latchStop(stop *pythonruntime.TerminalStop) {
	if o.cfg.StopLatch != nil && stop != nil {
		o.cfg.StopLatch.Trigger(*stop)
	}
}

func (o *AdmissionObserver) recordAdmission(op pythonruntime.ReachedOperation, authTarget pythonruntime.CanonicalTarget, dec pythonruntime.OperationDecision) {
	var stopCode pythonruntime.StopCode
	if dec.Stop != nil {
		stopCode = dec.Stop.Code
	}
	o.evidence.RecordAdmission(AdmissionRecord{
		ExecutionID:         op.ExecutionID,
		OperationID:         op.OperationID,
		CallID:              o.cfg.CallID,
		Actor:               o.cfg.Actor,
		View:                o.cfg.View,
		Surface:             o.cfg.Surface,
		Family:              op.Family,
		Operation:           op.Operation,
		Target:              op.Target,
		AuthoritativeTarget: authTarget,
		Decision:            dec.Kind,
		Errno:               dec.Errno,
		StopCode:            stopCode,
		ApprovalLeaseID:     dec.ApprovalLeaseID,
		DeclaredFrame:       op.DeclaredFrame,
		Reason:              dec.Reason,
		RecordedAt:          o.clock.Now().UTC(),
	})
}

func (o *AdmissionObserver) isProtectedPath(relPath string) bool {
	cleaned := strings.TrimPrefix(path.Clean("/"+relPath), "/")
	for _, p := range o.cfg.ProtectedPaths {
		prot := strings.TrimPrefix(path.Clean("/"+p), "/")
		if prot == "" {
			continue
		}
		if cleaned == prot || strings.HasPrefix(cleaned, prot+"/") {
			return true
		}
	}
	return false
}

func hasPathEscape(relPath string) bool {
	if relPath == "" {
		return false
	}
	if strings.HasPrefix(relPath, "/") {
		return true
	}
	for _, seg := range strings.Split(relPath, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}
