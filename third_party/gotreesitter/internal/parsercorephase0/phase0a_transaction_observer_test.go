//go:build gts_parsercorephase0 && gts_parsercorephase0a

package parsercorephase0

import (
	"errors"
	"testing"
)

func phase0ATransactionFixture(t *testing.T, limits Phase0AObserverLimits) (*Core, CoreRunNamespace) {
	t.Helper()
	phase0ATestSession(t, limits)
	core := phase0ATestCore(t)
	run, err := core.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	return core, run
}

func phase0ATransactionLimits() Phase0AObserverLimits {
	return Phase0AObserverLimits{MaxCores: 2, MaxRecords: 128, MaxBytes: 16 << 10, MaxFrames: 8, MaxMutations: 64}
}

func TestPhase0ATransactionInnerCommitOuterRollback(t *testing.T) {
	core, run := phase0ATransactionFixture(t, phase0ATransactionLimits())
	sentinel := errors.New("outer rollback")
	err := core.ApplyAtomic(func() error {
		if _, err := phase0ANextConstructionEvent(core, run); err != nil {
			return err
		}
		if err := core.ApplyAtomic(func() error {
			_, err := phase0ANextConstructionEvent(core, run)
			return err
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("outer error = %v", err)
	}
	proof, err := Phase0AObserverProof(core, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(proof.Frames) != 0 || len(proof.Mutations) != 2 {
		t.Fatalf("proof frames=%+v mutations=%+v", proof.Frames, proof.Mutations)
	}
	for _, mutation := range proof.Mutations {
		if !mutation.RolledBack || mutation.RollbackCause != Phase0ARollbackReturnedError {
			t.Fatalf("outer rollback did not tombstone suffix: %+v", mutation)
		}
	}
	next, err := phase0ANextConstructionEvent(core, run)
	if err != nil {
		t.Fatal(err)
	}
	if next.Serial != 3 {
		t.Fatalf("event serial rewound after rollback: %d", next.Serial)
	}
}

func TestPhase0ATransactionInnerRollbackOuterCommit(t *testing.T) {
	core, run := phase0ATransactionFixture(t, phase0ATransactionLimits())
	innerFailure := errors.New("inner rollback")
	err := core.ApplyAtomic(func() error {
		outer, err := phase0ANextConstructionEvent(core, run)
		if err != nil {
			return err
		}
		if err := core.ApplyAtomic(func() error {
			if _, err := phase0ANextConstructionEvent(core, run); err != nil {
				return err
			}
			return innerFailure
		}); !errors.Is(err, innerFailure) {
			return err
		}
		_, err = phase0ANextScaffoldEdge(core, outer)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := Phase0AObserverProof(core, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(proof.Frames) != 0 || len(proof.Mutations) != 3 {
		t.Fatalf("proof frames=%+v mutations=%+v", proof.Frames, proof.Mutations)
	}
	if proof.Mutations[0].RolledBack || !proof.Mutations[1].RolledBack || proof.Mutations[2].RolledBack {
		t.Fatalf("inner rollback escaped its suffix: %+v", proof.Mutations)
	}
}

func TestPhase0ATransactionReturnedErrorAndPanicCauses(t *testing.T) {
	tests := []struct {
		name  string
		panic bool
		cause Phase0ARollbackCause
	}{
		{name: "returned-error", cause: Phase0ARollbackReturnedError},
		{name: "panic", panic: true, cause: Phase0ARollbackPanic},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			core, run := phase0ATransactionFixture(t, phase0ATransactionLimits())
			func() {
				defer func() { _ = recover() }()
				_ = core.ApplyAtomic(func() error {
					if _, err := phase0ANextConstructionEvent(core, run); err != nil {
						return err
					}
					if test.panic {
						panic("transaction panic")
					}
					return errors.New("transaction error")
				})
			}()
			proof, err := Phase0AObserverProof(core, run)
			if err != nil {
				t.Fatal(err)
			}
			if len(proof.Mutations) != 1 || !proof.Mutations[0].RolledBack || proof.Mutations[0].RollbackCause != test.cause {
				t.Fatalf("rollback proof = %+v", proof.Mutations)
			}
		})
	}
}

func TestPhase0ASchedulerLifecycleAndFirstPoison(t *testing.T) {
	tests := []struct {
		name       string
		operation  func(*Core, CoreRunNamespace, SchedulerTransactionToken) error
		wantCause  Phase0ARollbackCause
		wantPoison Phase0APoisonKind
		wantCommit bool
	}{
		{name: "commit", wantCommit: true, operation: func(c *Core, run CoreRunNamespace, _ SchedulerTransactionToken) error {
			_, err := phase0ANextConstructionEvent(c, run)
			return err
		}},
		{name: "returned-error", wantCause: Phase0ARollbackReturnedError, operation: func(c *Core, run CoreRunNamespace, _ SchedulerTransactionToken) error {
			_, _ = phase0ANextConstructionEvent(c, run)
			return errors.New("scheduler error")
		}},
		{name: "ignored-owned-error", wantCause: Phase0ARollbackSchedulerPoison, wantPoison: Phase0APoisonReturnedError, operation: func(c *Core, run CoreRunNamespace, token SchedulerTransactionToken) error {
			_, _ = phase0ANextConstructionEvent(c, run)
			_ = c.RunSchedulerOwned(token, func() error { return errors.New("owned error") })
			return nil
		}},
		{name: "nested-owner", wantCause: Phase0ARollbackSchedulerPoison, wantPoison: Phase0APoisonNestedScheduler, operation: func(c *Core, run CoreRunNamespace, _ SchedulerTransactionToken) error {
			_, _ = phase0ANextConstructionEvent(c, run)
			_ = c.ApplySchedulerAtomic(func(SchedulerTransactionToken) error { return nil })
			return nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			core, run := phase0ATransactionFixture(t, phase0ATransactionLimits())
			err := core.ApplySchedulerAtomic(func(token SchedulerTransactionToken) error { return test.operation(core, run, token) })
			if test.wantCommit && err != nil {
				t.Fatal(err)
			}
			if !test.wantCommit && err == nil {
				t.Fatal("scheduler rollback returned nil")
			}
			proof, proofErr := Phase0AObserverProof(core, run)
			if proofErr != nil {
				t.Fatal(proofErr)
			}
			if len(proof.Mutations) != 1 || proof.Mutations[0].RolledBack == test.wantCommit {
				t.Fatalf("scheduler mutation = %+v", proof.Mutations)
			}
			if !test.wantCommit && proof.Mutations[0].RollbackCause != test.wantCause {
				t.Fatalf("rollback cause = %v, want %v", proof.Mutations[0].RollbackCause, test.wantCause)
			}
			if test.wantPoison != 0 && (proof.FirstPoison == nil || proof.FirstPoison.Kind != test.wantPoison) {
				t.Fatalf("first poison = %+v, want %v", proof.FirstPoison, test.wantPoison)
			}
		})
	}
}

func TestPhase0ASchedulerPanicAndRecoveredOwnedPanic(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "scheduler-panic", true: "recovered-owned-panic"}[owned], func(t *testing.T) {
			core, run := phase0ATransactionFixture(t, phase0ATransactionLimits())
			func() {
				defer func() { _ = recover() }()
				_ = core.ApplySchedulerAtomic(func(token SchedulerTransactionToken) error {
					_, _ = phase0ANextConstructionEvent(core, run)
					if !owned {
						panic("scheduler panic")
					}
					func() {
						defer func() { _ = recover() }()
						_ = core.RunSchedulerOwned(token, func() error { panic("owned panic") })
					}()
					return nil
				})
			}()
			proof, err := Phase0AObserverProof(core, run)
			if err != nil {
				t.Fatal(err)
			}
			if len(proof.Mutations) != 1 || !proof.Mutations[0].RolledBack {
				t.Fatalf("panic mutation = %+v", proof.Mutations)
			}
			if owned {
				if proof.Mutations[0].RollbackCause != Phase0ARollbackSchedulerPoison || proof.FirstPoison == nil || proof.FirstPoison.Kind != Phase0APoisonPanic {
					t.Fatalf("owned panic proof = %+v", proof)
				}
			} else if proof.Mutations[0].RollbackCause != Phase0ARollbackPanic {
				t.Fatalf("scheduler panic cause = %v", proof.Mutations[0].RollbackCause)
			}
		})
	}
}

func TestPhase0ASchedulerWrongStaleAndNonTopTokensStayIsolated(t *testing.T) {
	t.Run("wrong-core", func(t *testing.T) {
		owner, ownerRun := phase0ATransactionFixture(t, phase0ATransactionLimits())
		called := phase0ATestCore(t)
		calledRun, err := called.BeginRun()
		if err != nil {
			t.Fatal(err)
		}
		err = owner.ApplySchedulerAtomic(func(ownerToken SchedulerTransactionToken) error {
			calledErr := called.ApplySchedulerAtomic(func(SchedulerTransactionToken) error {
				_ = called.RunSchedulerOwned(ownerToken, func() error { return nil })
				return nil
			})
			if calledErr == nil {
				t.Fatal("called core was not poisoned")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("token owner was poisoned: %v", err)
		}
		ownerProof, _ := Phase0AObserverProof(owner, ownerRun)
		calledProof, _ := Phase0AObserverProof(called, calledRun)
		if ownerProof.FirstPoison != nil || calledProof.FirstPoison == nil || calledProof.FirstPoison.Kind != Phase0APoisonWrongCore {
			t.Fatalf("wrong-core isolation owner=%+v called=%+v", ownerProof.FirstPoison, calledProof.FirstPoison)
		}
	})

	t.Run("stale", func(t *testing.T) {
		core, run := phase0ATransactionFixture(t, phase0ATransactionLimits())
		var stale SchedulerTransactionToken
		if err := core.ApplySchedulerAtomic(func(token SchedulerTransactionToken) error { stale = token; return nil }); err != nil {
			t.Fatal(err)
		}
		if err := core.ApplySchedulerAtomic(func(SchedulerTransactionToken) error {
			_ = core.RunSchedulerOwned(stale, func() error { return nil })
			return nil
		}); err == nil {
			t.Fatal("stale token did not poison current scheduler")
		}
		proof, _ := Phase0AObserverProof(core, run)
		if proof.FirstPoison == nil || proof.FirstPoison.Kind != Phase0APoisonStaleToken {
			t.Fatalf("stale poison = %+v", proof.FirstPoison)
		}
	})

	t.Run("non-top", func(t *testing.T) {
		core, run := phase0ATransactionFixture(t, phase0ATransactionLimits())
		if err := core.ApplySchedulerAtomic(func(token SchedulerTransactionToken) error {
			_ = core.ApplyAtomic(func() error {
				_ = core.RunSchedulerOwned(token, func() error { return nil })
				return nil
			})
			return nil
		}); err == nil {
			t.Fatal("non-top token did not poison scheduler")
		}
		proof, _ := Phase0AObserverProof(core, run)
		if proof.FirstPoison == nil || proof.FirstPoison.Kind != Phase0APoisonNonTopToken {
			t.Fatalf("non-top poison = %+v", proof.FirstPoison)
		}
	})
}

func TestPhase0AObserverFrameAndTransactionProofFailuresSticky(t *testing.T) {
	t.Run("frame-cap-does-not-change-parser-result", func(t *testing.T) {
		limits := phase0ATransactionLimits()
		limits.MaxFrames = 1
		core, run := phase0ATransactionFixture(t, limits)
		if err := core.ApplyAtomic(func() error { return core.ApplyAtomic(func() error { return nil }) }); err != nil {
			t.Fatalf("observer frame cap changed parser result: %v", err)
		}
		proof, err := Phase0AObserverProof(core, run)
		if !isPhase0AError(err, Phase0AErrorFrameCap, "") || proof.Failure == nil || proof.Failure.Kind != Phase0AErrorFrameCap {
			t.Fatalf("frame cap proof = %+v", proof.Failure)
		}
	})
	t.Run("transaction-mismatch", func(t *testing.T) {
		core, run := phase0ATransactionFixture(t, phase0ATransactionLimits())
		phase0AObserveCommit(core, 99)
		phase0AObserveRollback(core, 100, Phase0ARollbackUnknown)
		proof, err := Phase0AObserverProof(core, run)
		if !isPhase0AError(err, Phase0AErrorTransactionProof, "") || proof.Failure == nil || proof.Failure.Kind != Phase0AErrorTransactionProof || proof.Failure.Detail != "commit transaction mismatch" {
			t.Fatalf("first sticky proof = %+v", proof.Failure)
		}
	})
	t.Run("transaction-counter-mismatch", func(t *testing.T) {
		core, run := phase0ATransactionFixture(t, phase0ATransactionLimits())
		phase0AObserveMark(core, 0, 0)
		proof, err := Phase0AObserverProof(core, run)
		if !isPhase0AError(err, Phase0AErrorTransactionProof, "") || proof.Failure == nil || proof.Failure.Kind != Phase0AErrorTransactionProof || proof.Failure.Detail != "mark parent mismatch" {
			t.Fatalf("counter mismatch proof = %+v", proof.Failure)
		}
	})
	t.Run("mutation-cap-is-atomic-and-sticky", func(t *testing.T) {
		limits := phase0ATransactionLimits()
		limits.MaxMutations = 1
		core, run := phase0ATransactionFixture(t, limits)
		first, err := phase0ANextConstructionEvent(core, run)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := phase0ANextConstructionEvent(core, run); !isPhase0AError(err, Phase0AErrorMutationCap, "") {
			t.Fatalf("mutation cap = %v", err)
		}
		proof, err := Phase0AObserverProof(core, run)
		if !isPhase0AError(err, Phase0AErrorMutationCap, "") || len(proof.Mutations) != 1 || proof.Mutations[0].Event != first {
			t.Fatalf("mutation cap changed history: proof=%+v err=%v", proof, err)
		}
		phase0AObservers.Lock()
		next := phase0AObservers.byCore[core].nextEvent
		phase0AObservers.Unlock()
		if next != 1 {
			t.Fatalf("mutation cap advanced serial to %d", next)
		}
	})
}

func TestPhase0ARolledBackEventCannotBeReusedAndFailureIsAuthoritative(t *testing.T) {
	core, run := phase0ATransactionFixture(t, phase0ATransactionLimits())
	var rolledBack ConstructionEventKey
	sentinel := errors.New("rollback event")
	if err := core.ApplyAtomic(func() error {
		var err error
		rolledBack, err = phase0ANextConstructionEvent(core, run)
		if err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("rollback = %v", err)
	}
	phase0AObservers.Lock()
	beforeEvent := phase0AObservers.byCore[core].nextEvent
	beforeEdge := phase0AObservers.byCore[core].nextEdge
	phase0AObservers.Unlock()
	if _, err := phase0ANextScaffoldEdge(core, rolledBack); !isPhase0AError(err, Phase0AErrorInvalidEvent, "") {
		t.Fatalf("rolled-back event reuse = %v", err)
	}
	if _, err := phase0ANextConstructionEvent(core, run); !isPhase0AError(err, Phase0AErrorInvalidEvent, "") {
		t.Fatalf("later mutator did not return first failure: %v", err)
	}
	phase0AObservers.Lock()
	afterEvent := phase0AObservers.byCore[core].nextEvent
	afterEdge := phase0AObservers.byCore[core].nextEdge
	phase0AObservers.Unlock()
	if beforeEvent != afterEvent || beforeEdge != afterEdge {
		t.Fatalf("sticky failure advanced serials: events %d->%d edges %d->%d", beforeEvent, afterEvent, beforeEdge, afterEdge)
	}
	if err := core.EndRun(run); !isPhase0AError(err, Phase0AErrorInvalidEvent, "") {
		t.Fatalf("EndRun lost sticky failure: %v", err)
	}
	if _, err := phase0ANextConstructionEvent(core, run); !isPhase0AError(err, Phase0AErrorInvalidEvent, "") {
		t.Fatalf("post-EndRun mutator lost sticky failure: %v", err)
	}
	proof, err := Phase0AObserverProof(core, run)
	if !isPhase0AError(err, Phase0AErrorInvalidEvent, "") || len(proof.Mutations) != 1 || !proof.Mutations[0].RolledBack {
		t.Fatalf("rolled-back proof = %+v err=%v", proof, err)
	}
}

func TestPhase0AFrameCapReconcilesOuterAndSessionEnds(t *testing.T) {
	limits := phase0ATransactionLimits()
	limits.MaxFrames = 1
	if err := BeginPhase0AObserverSession(limits); err != nil {
		t.Fatal(err)
	}
	ended := false
	t.Cleanup(func() {
		if ended {
			return
		}
		phase0AObservers.Lock()
		for _, observer := range phase0AObservers.byCore {
			observer.active = false
		}
		phase0AObservers.Unlock()
		_ = EndPhase0AObserverSession()
	})
	core := phase0ATestCore(t)
	run, err := core.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	if err := core.ApplyAtomic(func() error { return core.ApplyAtomic(func() error { return nil }) }); err != nil {
		t.Fatalf("observer cap changed parser result: %v", err)
	}
	proof, err := Phase0AObserverProof(core, run)
	if !isPhase0AError(err, Phase0AErrorFrameCap, "") || len(proof.Frames) != 0 {
		t.Fatalf("outer frame not reconciled: %+v err=%v", proof.Frames, err)
	}
	if err := core.EndRun(run); !isPhase0AError(err, Phase0AErrorFrameCap, "") {
		t.Fatalf("EndRun = %v", err)
	}
	if err := EndPhase0AObserverSession(); err != nil {
		t.Fatalf("session end after reconciled failure = %v", err)
	}
	ended = true
}

func TestPhase0AMutationFailureStillTombstonesTrackedOuterRollback(t *testing.T) {
	limits := phase0ATransactionLimits()
	limits.MaxMutations = 1
	core, run := phase0ATransactionFixture(t, limits)
	err := core.ApplyAtomic(func() error {
		if _, err := phase0ANextConstructionEvent(core, run); err != nil {
			return err
		}
		_, err := phase0ANextConstructionEvent(core, run)
		return err
	})
	if !isPhase0AError(err, Phase0AErrorMutationCap, "") {
		t.Fatalf("outer parser error = %v", err)
	}
	proof, proofErr := Phase0AObserverProof(core, run)
	if !isPhase0AError(proofErr, Phase0AErrorMutationCap, "") || len(proof.Frames) != 0 || len(proof.Mutations) != 1 || !proof.Mutations[0].RolledBack {
		t.Fatalf("failed outer rollback proof = %+v err=%v", proof, proofErr)
	}
	if err := core.EndRun(run); !isPhase0AError(err, Phase0AErrorMutationCap, "") {
		t.Fatalf("EndRun = %v", err)
	}
}

func TestPhase0APendingRollbackDoesNotLeakAcrossRunOrReset(t *testing.T) {
	core, firstRun := phase0ATransactionFixture(t, phase0ATransactionLimits())
	phase0ASetRollbackCause(core, Phase0ARollbackPanic)
	if err := core.EndRun(firstRun); err != nil {
		t.Fatal(err)
	}
	secondRun, err := core.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	if err := core.ApplyAtomic(func() error {
		_, _ = phase0ANextConstructionEvent(core, secondRun)
		return errors.New("second run rollback")
	}); err == nil {
		t.Fatal("second run committed")
	}
	proof, err := Phase0AObserverProof(core, secondRun)
	if err != nil || len(proof.Mutations) != 1 || proof.Mutations[0].RollbackCause != Phase0ARollbackReturnedError {
		t.Fatalf("cross-run cause leaked: %+v err=%v", proof.Mutations, err)
	}
	phase0ASetRollbackCause(core, Phase0ARollbackPanic)
	if err := core.Reset(); err != nil {
		t.Fatal(err)
	}
	thirdRun, err := core.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	if err := core.ApplyAtomic(func() error {
		_, _ = phase0ANextConstructionEvent(core, thirdRun)
		return errors.New("post-reset rollback")
	}); err == nil {
		t.Fatal("post-reset transaction committed")
	}
	proof, err = Phase0AObserverProof(core, thirdRun)
	if err != nil || len(proof.Mutations) != 1 || proof.Mutations[0].RollbackCause != Phase0ARollbackReturnedError {
		t.Fatalf("reset cause leaked: %+v err=%v", proof.Mutations, err)
	}
}

func TestPhase0ARootFrameRejectsNonzeroParent(t *testing.T) {
	core, run := phase0ATransactionFixture(t, phase0ATransactionLimits())
	phase0AObserveMark(core, 77, 9)
	proof, err := Phase0AObserverProof(core, run)
	if !isPhase0AError(err, Phase0AErrorTransactionProof, "") || proof.Failure == nil || proof.Failure.Detail != "mark parent mismatch" || len(proof.Frames) != 0 {
		t.Fatalf("root-parent proof = %+v err=%v", proof, err)
	}
}

func TestPhase0AEndRunRefusesCapSuppressedActiveParserTransaction(t *testing.T) {
	limits := phase0ATransactionLimits()
	limits.MaxRecords = 0
	if err := BeginPhase0AObserverSession(limits); err != nil {
		t.Fatal(err)
	}
	ended := false
	t.Cleanup(func() {
		if ended {
			return
		}
		phase0AObservers.Lock()
		for _, observer := range phase0AObservers.byCore {
			observer.active = false
		}
		phase0AObservers.Unlock()
		_ = EndPhase0AObserverSession()
	})
	core := phase0ATestCore(t)
	run, err := core.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	if err := core.ApplyAtomic(func() error {
		if err := core.EndRun(run); !isPhase0AError(err, Phase0AErrorRecordCap, "") {
			t.Fatalf("EndRun during active parser transaction = %v", err)
		}
		phase0AObservers.Lock()
		active := phase0AObservers.byCore[core].active
		phase0AObservers.Unlock()
		if !active {
			t.Fatal("EndRun deactivated during active parser transaction")
		}
		if err := EndPhase0AObserverSession(); !isPhase0AError(err, Phase0AErrorSessionHasRuns, "") {
			t.Fatalf("session ended during active parser transaction: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("observer refusal changed parser result: %v", err)
	}
	if err := core.EndRun(run); !isPhase0AError(err, Phase0AErrorRecordCap, "") {
		t.Fatalf("EndRun after unwind lost first failure: %v", err)
	}
	if err := EndPhase0AObserverSession(); err != nil {
		t.Fatalf("session end after unwind = %v", err)
	}
	ended = true
}
