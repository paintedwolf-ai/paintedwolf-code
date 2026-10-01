//go:build gts_parsercorephase0 && gts_parsercorephase0a

package parsercorephase0

import (
	"errors"
	"sync"
)

// Phase0ADiagnosticRun is an opaque driver-owned observer lifecycle. It is
// inactive when no explicit Phase0A observer session is running.
type Phase0ADiagnosticRun struct {
	core      *Core
	namespace CoreRunNamespace
	active    bool
}

// Phase0ADiagnosticRunReceipt is captured before EndRun releases the observed
// run. Proof owns independent snapshots and remains valid after later runs.
type Phase0ADiagnosticRunReceipt struct {
	Namespace              CoreRunNamespace
	Proof                  Phase0AProofSnapshot
	RawSelected            RawSelectedCensus
	RawSelectedError       string
	RawSelectedDigest      [32]byte
	RawSelectedDigestError string
}

type phase0ADiagnosticActiveRun struct {
	namespace              CoreRunNamespace
	rawSelected            RawSelectedCensus
	rawSelectedError       string
	rawSelectedDigest      [32]byte
	rawSelectedDigestError string
	recording              bool
	recordDone             chan struct{}
	ending                 bool
}

var phase0ADiagnosticRuns = struct {
	sync.Mutex
	active    map[*Core]phase0ADiagnosticActiveRun
	completed []Phase0ADiagnosticRunReceipt
}{active: make(map[*Core]phase0ADiagnosticActiveRun)}

// BeginPhase0ADiagnosticRun joins an explicitly active observer session. It
// never starts a process-global session implicitly.
func BeginPhase0ADiagnosticRun(core *Core) (Phase0ADiagnosticRun, error) {
	phase0AObservers.Lock()
	active := phase0AObservers.active
	phase0AObservers.Unlock()
	if !active {
		return Phase0ADiagnosticRun{}, nil
	}
	phase0ADiagnosticRuns.Lock()
	owned, exists := phase0ADiagnosticRuns.active[core]
	phase0ADiagnosticRuns.Unlock()
	if exists {
		return Phase0ADiagnosticRun{}, &Phase0AError{Kind: Phase0AErrorRunActive, Namespace: owned.namespace, Detail: "diagnostic driver already owns this core"}
	}
	namespace, err := core.BeginRun()
	if err != nil {
		return Phase0ADiagnosticRun{}, err
	}
	phase0ADiagnosticRuns.Lock()
	if _, exists := phase0ADiagnosticRuns.active[core]; exists {
		phase0ADiagnosticRuns.Unlock()
		_ = core.EndRun(namespace)
		return Phase0ADiagnosticRun{}, &Phase0AError{Kind: Phase0AErrorRunActive, Namespace: namespace, Detail: "diagnostic driver already owns this core"}
	}
	phase0ADiagnosticRuns.active[core] = phase0ADiagnosticActiveRun{namespace: namespace}
	phase0ADiagnosticRuns.Unlock()
	return Phase0ADiagnosticRun{core: core, namespace: namespace, active: true}, nil
}

// RecordPhase0ADiagnosticAcceptedRoots captures the independently selected
// compact payload roots while the parser still owns the exact derivation. It
// is census-only: failures are retained in the receipt and never change parser
// acceptance or materialization.
func RecordPhase0ADiagnosticAcceptedRoots(core *Core, roots []SubtreeID) error {
	return recordPhase0ADiagnosticAcceptedRoots(core, roots, nil)
}

func recordPhase0ADiagnosticAcceptedRoots(core *Core, roots []SubtreeID, afterClaim func()) error {
	phase0ADiagnosticRuns.Lock()
	run, ok := phase0ADiagnosticRuns.active[core]
	if !ok {
		phase0ADiagnosticRuns.Unlock()
		return nil
	}
	if run.ending || run.recording {
		phase0ADiagnosticRuns.Unlock()
		return &Phase0AError{Kind: Phase0AErrorRunActive, Namespace: run.namespace, Detail: "diagnostic accepted-root record conflicts with active lifecycle work"}
	}
	run.recording = true
	run.recordDone = make(chan struct{})
	phase0ADiagnosticRuns.active[core] = run
	phase0ADiagnosticRuns.Unlock()
	if afterClaim != nil {
		afterClaim()
	}
	var census RawSelectedCensus
	var err error
	if len(roots) != 0 {
		census, err = core.RawSelectedSubtreeCensus(roots)
	}
	digest, digestErr := phase0ARawSelectedCompactDigest(core, roots)
	phase0ADiagnosticRuns.Lock()
	current, currentOK := phase0ADiagnosticRuns.active[core]
	if !currentOK || current.namespace != run.namespace || !current.recording || current.recordDone == nil {
		close(run.recordDone)
		phase0ADiagnosticRuns.Unlock()
		return &Phase0AError{Kind: Phase0AErrorStaleNamespace, Namespace: run.namespace, Detail: "diagnostic accepted-root owner changed during record"}
	}
	if err != nil {
		current.rawSelectedError = err.Error()
	} else {
		current.rawSelected = census
	}
	if digestErr != nil {
		current.rawSelectedDigestError = digestErr.Error()
	} else {
		current.rawSelectedDigest = digest
	}
	current.recording = false
	close(current.recordDone)
	current.recordDone = nil
	phase0ADiagnosticRuns.active[core] = current
	phase0ADiagnosticRuns.Unlock()
	return nil
}

// Phase0ADiagnosticRunManaged reports whether acceptance errors belong to a
// census run. Managed observer failures are retained in the proof rather than
// changing scheduler acceptance or materialization.
func Phase0ADiagnosticRunManaged(core *Core) bool {
	phase0ADiagnosticRuns.Lock()
	_, ok := phase0ADiagnosticRuns.active[core]
	phase0ADiagnosticRuns.Unlock()
	return ok
}

// EndPhase0ADiagnosticRun snapshots proof first, then ends the run. A sticky
// observer proof failure is census data and is suppressed after the exact
// snapshot is retained; lifecycle failures still fail closed.
func EndPhase0ADiagnosticRun(run Phase0ADiagnosticRun) error {
	return endPhase0ADiagnosticRun(run, nil)
}

func endPhase0ADiagnosticRun(run Phase0ADiagnosticRun, afterClaim func()) error {
	if !run.active {
		return nil
	}
	if run.core == nil {
		return &Phase0AError{Kind: Phase0AErrorUnregisteredCore, Namespace: run.namespace, Detail: "diagnostic run has no core"}
	}
	recordDone, err := phase0AClaimDiagnosticRunEnd(run)
	if err != nil {
		return err
	}
	if afterClaim != nil {
		afterClaim()
	}
	if recordDone != nil {
		<-recordDone
	}
	if _, err := phase0AFinalDiagnosticRunEndClaim(run); err != nil {
		return err
	}
	observerActive, stateErr := phase0ADiagnosticObserverRunActive(run.core, run.namespace)
	if stateErr != nil || !observerActive {
		phase0AReleaseDiagnosticRunEnd(run)
		if stateErr != nil {
			return stateErr
		}
		return &Phase0AError{Kind: Phase0AErrorStaleNamespace, Namespace: run.namespace, Detail: "diagnostic observer run is not active"}
	}
	proof, proofErr := Phase0AObserverProof(run.core, run.namespace)
	endErr := run.core.EndRun(run.namespace)
	observerActive, stateErr = phase0ADiagnosticObserverRunActive(run.core, run.namespace)
	if stateErr != nil || observerActive {
		phase0AReleaseDiagnosticRunEnd(run)
		if stateErr != nil {
			return stateErr
		}
		if endErr != nil {
			return endErr
		}
		return &Phase0AError{Kind: Phase0AErrorTransactionProof, Namespace: run.namespace, Detail: "diagnostic observer EndRun did not deactivate"}
	}
	phase0ADiagnosticRuns.Lock()
	current, owned := phase0ADiagnosticRuns.active[run.core]
	if !owned || current.namespace != run.namespace || !current.ending || current.recording {
		phase0ADiagnosticRuns.Unlock()
		return &Phase0AError{Kind: Phase0AErrorStaleNamespace, Namespace: run.namespace, Detail: "diagnostic run ownership changed during end"}
	}
	delete(phase0ADiagnosticRuns.active, run.core)
	phase0ADiagnosticRuns.completed = append(phase0ADiagnosticRuns.completed, Phase0ADiagnosticRunReceipt{
		Namespace: run.namespace, Proof: proof, RawSelected: current.rawSelected, RawSelectedError: current.rawSelectedError,
		RawSelectedDigest: current.rawSelectedDigest, RawSelectedDigestError: current.rawSelectedDigestError,
	})
	phase0ADiagnosticRuns.Unlock()
	if proofErr != nil {
		if endErr == nil || phase0ASameTypedError(endErr, proofErr) {
			return nil
		}
		return endErr
	}
	return endErr
}

func phase0AClaimDiagnosticRunEnd(run Phase0ADiagnosticRun) (<-chan struct{}, error) {
	phase0ADiagnosticRuns.Lock()
	defer phase0ADiagnosticRuns.Unlock()
	active, ok := phase0ADiagnosticRuns.active[run.core]
	if !ok || active.namespace != run.namespace {
		return nil, &Phase0AError{Kind: Phase0AErrorStaleNamespace, Namespace: run.namespace, Detail: "diagnostic run is not the active owner"}
	}
	if active.ending {
		return nil, &Phase0AError{Kind: Phase0AErrorRunActive, Namespace: run.namespace, Detail: "diagnostic run end is already in progress"}
	}
	active.ending = true
	phase0ADiagnosticRuns.active[run.core] = active
	return active.recordDone, nil
}

func phase0AFinalDiagnosticRunEndClaim(run Phase0ADiagnosticRun) (phase0ADiagnosticActiveRun, error) {
	phase0ADiagnosticRuns.Lock()
	defer phase0ADiagnosticRuns.Unlock()
	active, ok := phase0ADiagnosticRuns.active[run.core]
	if !ok || active.namespace != run.namespace || !active.ending {
		return phase0ADiagnosticActiveRun{}, &Phase0AError{Kind: Phase0AErrorStaleNamespace, Namespace: run.namespace, Detail: "diagnostic run lost end ownership while waiting"}
	}
	if active.recording || active.recordDone != nil {
		return phase0ADiagnosticActiveRun{}, &Phase0AError{Kind: Phase0AErrorRunActive, Namespace: run.namespace, Detail: "diagnostic accepted-root record did not finish"}
	}
	return active, nil
}

func phase0AReleaseDiagnosticRunEnd(run Phase0ADiagnosticRun) {
	phase0ADiagnosticRuns.Lock()
	defer phase0ADiagnosticRuns.Unlock()
	active, ok := phase0ADiagnosticRuns.active[run.core]
	if ok && active.namespace == run.namespace {
		active.ending = false
		phase0ADiagnosticRuns.active[run.core] = active
	}
}

func phase0ADiagnosticObserverRunActive(core *Core, namespace CoreRunNamespace) (bool, error) {
	phase0AObservers.Lock()
	defer phase0AObservers.Unlock()
	observer := phase0AObservers.byCore[core]
	if observer == nil {
		return false, &Phase0AError{Kind: Phase0AErrorUnregisteredCore, Namespace: namespace}
	}
	if observer.run != namespace {
		return false, &Phase0AError{Kind: Phase0AErrorStaleNamespace, Namespace: namespace}
	}
	return observer.active, nil
}

func phase0ASameTypedError(left, right error) bool {
	var leftPhase0A, rightPhase0A *Phase0AError
	if !errors.As(left, &leftPhase0A) || !errors.As(right, &rightPhase0A) {
		return false
	}
	return leftPhase0A.Kind == rightPhase0A.Kind &&
		leftPhase0A.Counter == rightPhase0A.Counter &&
		leftPhase0A.Namespace == rightPhase0A.Namespace &&
		leftPhase0A.Detail == rightPhase0A.Detail
}

// TakePhase0ADiagnosticRunReceipt consumes the oldest completed driver run.
func TakePhase0ADiagnosticRunReceipt() (Phase0ADiagnosticRunReceipt, bool) {
	phase0ADiagnosticRuns.Lock()
	defer phase0ADiagnosticRuns.Unlock()
	if len(phase0ADiagnosticRuns.completed) == 0 {
		return Phase0ADiagnosticRunReceipt{}, false
	}
	receipt := phase0ADiagnosticRuns.completed[0]
	copy(phase0ADiagnosticRuns.completed, phase0ADiagnosticRuns.completed[1:])
	last := len(phase0ADiagnosticRuns.completed) - 1
	phase0ADiagnosticRuns.completed[last] = Phase0ADiagnosticRunReceipt{}
	phase0ADiagnosticRuns.completed = phase0ADiagnosticRuns.completed[:last]
	return receipt, true
}

// ResetPhase0ADiagnosticRunReceipts clears completed snapshots only when no
// driver-owned run is active.
func ResetPhase0ADiagnosticRunReceipts() error {
	phase0ADiagnosticRuns.Lock()
	defer phase0ADiagnosticRuns.Unlock()
	if len(phase0ADiagnosticRuns.active) != 0 {
		return &Phase0AError{Kind: Phase0AErrorRunActive, Detail: "diagnostic run receipt reset while active"}
	}
	clear(phase0ADiagnosticRuns.completed)
	phase0ADiagnosticRuns.completed = nil
	return nil
}
