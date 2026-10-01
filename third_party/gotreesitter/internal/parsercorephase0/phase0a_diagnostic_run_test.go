//go:build gts_parsercorephase0 && gts_parsercorephase0a

package parsercorephase0

import "testing"

func TestPhase0ADiagnosticRunLifecycleResetAndMultipleRuns(t *testing.T) {
	if err := ResetPhase0ADiagnosticRunReceipts(); err != nil {
		t.Fatal(err)
	}
	if err := BeginPhase0AObserverSession(Phase0AObserverLimits{MaxCores: 2, MaxRecords: 32, MaxBytes: 4096}); err != nil {
		t.Fatal(err)
	}
	sessionActive := true
	defer func() {
		if sessionActive {
			phase0AObservers.Lock()
			for _, observer := range phase0AObservers.byCore {
				observer.active = false
			}
			phase0AObservers.Unlock()
			phase0ADiagnosticRuns.Lock()
			clear(phase0ADiagnosticRuns.active)
			phase0ADiagnosticRuns.Unlock()
			_ = EndPhase0AObserverSession()
		}
	}()

	firstCore := phase0ATestCore(t)
	first, err := BeginPhase0ADiagnosticRun(firstCore)
	if err != nil {
		t.Fatal(err)
	}
	if !first.active || !Phase0ADiagnosticRunManaged(firstCore) {
		t.Fatal("first diagnostic run was not managed")
	}
	if err := ResetPhase0ADiagnosticRunReceipts(); !isPhase0AError(err, Phase0AErrorRunActive, "") {
		t.Fatalf("reset while active err=%v, want run-active", err)
	}
	if err := EndPhase0ADiagnosticRun(first); err != nil {
		t.Fatal(err)
	}
	if Phase0ADiagnosticRunManaged(firstCore) {
		t.Fatal("first diagnostic run remained managed after end")
	}

	secondCore := phase0ATestCore(t)
	second, err := BeginPhase0ADiagnosticRun(secondCore)
	if err != nil {
		t.Fatal(err)
	}
	if err := EndPhase0ADiagnosticRun(second); err != nil {
		t.Fatal(err)
	}

	firstReceipt, ok := TakePhase0ADiagnosticRunReceipt()
	if !ok || firstReceipt.Namespace != first.namespace || firstReceipt.Proof.Namespace != first.namespace {
		t.Fatalf("first receipt=%+v ok=%v", firstReceipt, ok)
	}
	secondReceipt, ok := TakePhase0ADiagnosticRunReceipt()
	if !ok || secondReceipt.Namespace != second.namespace || secondReceipt.Proof.Namespace != second.namespace {
		t.Fatalf("second receipt=%+v ok=%v", secondReceipt, ok)
	}
	if firstReceipt.Namespace == secondReceipt.Namespace {
		t.Fatal("multiple diagnostic runs reused one namespace")
	}
	if _, ok := TakePhase0ADiagnosticRunReceipt(); ok {
		t.Fatal("completed receipt queue did not drain")
	}
	if err := ResetPhase0ADiagnosticRunReceipts(); err != nil {
		t.Fatal(err)
	}
	if err := EndPhase0AObserverSession(); err != nil {
		t.Fatal(err)
	}
	sessionActive = false
}

func TestPhase0ADiagnosticRunFeatureInactive(t *testing.T) {
	core := phase0ATestCore(t)
	run, err := BeginPhase0ADiagnosticRun(core)
	if err != nil {
		t.Fatal(err)
	}
	if run.active || Phase0ADiagnosticRunManaged(core) {
		t.Fatal("diagnostic run activated without an explicit observer session")
	}
	if err := EndPhase0ADiagnosticRun(run); err != nil {
		t.Fatal(err)
	}
	if _, ok := TakePhase0ADiagnosticRunReceipt(); ok {
		t.Fatal("inactive diagnostic run produced a receipt")
	}
}

func TestPhase0ADiagnosticRunDoubleEndDoesNotDuplicateReceipt(t *testing.T) {
	endSession := phase0ABeginDiagnosticTestSession(t, 1)
	core := phase0ATestCore(t)
	run, err := BeginPhase0ADiagnosticRun(core)
	if err != nil {
		t.Fatal(err)
	}
	if err := EndPhase0ADiagnosticRun(run); err != nil {
		t.Fatal(err)
	}
	if err := EndPhase0ADiagnosticRun(run); !isPhase0AError(err, Phase0AErrorStaleNamespace, "") {
		t.Fatalf("double end err=%v, want stale namespace", err)
	}
	if receipt, ok := TakePhase0ADiagnosticRunReceipt(); !ok || receipt.Namespace != run.namespace {
		t.Fatalf("first receipt=%+v ok=%v", receipt, ok)
	}
	if _, ok := TakePhase0ADiagnosticRunReceipt(); ok {
		t.Fatal("double end enqueued a duplicate receipt")
	}
	endSession()
}

func TestPhase0ADiagnosticRunStaleHandleCannotEndLaterRun(t *testing.T) {
	endSession := phase0ABeginDiagnosticTestSession(t, 1)
	core := phase0ATestCore(t)
	first, err := BeginPhase0ADiagnosticRun(core)
	if err != nil {
		t.Fatal(err)
	}
	if err := EndPhase0ADiagnosticRun(first); err != nil {
		t.Fatal(err)
	}
	if _, ok := TakePhase0ADiagnosticRunReceipt(); !ok {
		t.Fatal("first run produced no receipt")
	}
	second, err := BeginPhase0ADiagnosticRun(core)
	if err != nil {
		t.Fatal(err)
	}
	if first.namespace == second.namespace {
		t.Fatal("later run reused stale namespace")
	}
	if err := EndPhase0ADiagnosticRun(first); !isPhase0AError(err, Phase0AErrorStaleNamespace, "") {
		t.Fatalf("stale first handle err=%v, want stale namespace", err)
	}
	if !Phase0ADiagnosticRunManaged(core) {
		t.Fatal("stale first handle deleted the later active owner")
	}
	if _, ok := TakePhase0ADiagnosticRunReceipt(); ok {
		t.Fatal("stale first handle enqueued a receipt")
	}
	if err := EndPhase0ADiagnosticRun(second); err != nil {
		t.Fatal(err)
	}
	if receipt, ok := TakePhase0ADiagnosticRunReceipt(); !ok || receipt.Namespace != second.namespace {
		t.Fatalf("second receipt=%+v ok=%v", receipt, ok)
	}
	endSession()
}

func TestPhase0ADiagnosticRunStickyFailureWithActiveTransactionStaysManaged(t *testing.T) {
	endSession := phase0ABeginDiagnosticTestSession(t, 1)
	core := phase0ATestCore(t)
	run, err := BeginPhase0ADiagnosticRun(core)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.ApplyAtomic(func() error {
		phase0AObservers.Lock()
		observer := phase0AObservers.byCore[core]
		phase0AStickyLocked(observer, &Phase0AError{Kind: Phase0AErrorUnsupportedProof, Namespace: run.namespace, Detail: "test sticky proof failure"})
		phase0AObservers.Unlock()
		if err := EndPhase0ADiagnosticRun(run); !isPhase0AError(err, Phase0AErrorUnsupportedProof, "") {
			t.Fatalf("end with active transaction err=%v, want sticky proof failure", err)
		}
		if !Phase0ADiagnosticRunManaged(core) {
			t.Fatal("failed EndRun released active management")
		}
		if _, ok := TakePhase0ADiagnosticRunReceipt(); ok {
			t.Fatal("failed EndRun published a bogus completed receipt")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := EndPhase0ADiagnosticRun(run); err != nil {
		t.Fatalf("cleanup end after transaction closed: %v", err)
	}
	if Phase0ADiagnosticRunManaged(core) {
		t.Fatal("successful cleanup retained active management")
	}
	receipt, ok := TakePhase0ADiagnosticRunReceipt()
	if !ok || receipt.Namespace != run.namespace || receipt.Proof.Failure == nil || receipt.Proof.Failure.Kind != Phase0AErrorUnsupportedProof {
		t.Fatalf("cleanup receipt=%+v ok=%v", receipt, ok)
	}
	if _, ok := TakePhase0ADiagnosticRunReceipt(); ok {
		t.Fatal("cleanup published more than one completed receipt")
	}
	endSession()
}

func TestPhase0ADiagnosticRunEndWaitsForClaimedAcceptedRootRecord(t *testing.T) {
	endSession := phase0ABeginDiagnosticTestSession(t, 1)
	core := phase0ATestCore(t)
	leaf, err := core.appendSubtree(subtreeRecord{symbol: 9, endByte: 1, terminal: true}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := BeginPhase0ADiagnosticRun(core)
	if err != nil {
		t.Fatal(err)
	}
	recordClaimed := make(chan struct{})
	allowRecordFinish := make(chan struct{})
	recordResult := make(chan error, 1)
	go func() {
		recordResult <- recordPhase0ADiagnosticAcceptedRoots(core, []SubtreeID{leaf}, func() {
			close(recordClaimed)
			<-allowRecordFinish
		})
	}()
	<-recordClaimed

	endClaimed := make(chan struct{})
	endResult := make(chan error, 1)
	go func() {
		endResult <- endPhase0ADiagnosticRun(run, func() { close(endClaimed) })
	}()
	<-endClaimed
	select {
	case err := <-endResult:
		close(allowRecordFinish)
		t.Fatalf("end completed before claimed record published: %v", err)
	default:
	}
	close(allowRecordFinish)
	if err := <-recordResult; err != nil {
		t.Fatal(err)
	}
	if err := <-endResult; err != nil {
		t.Fatal(err)
	}
	receipt, ok := TakePhase0ADiagnosticRunReceipt()
	if !ok || receipt.Namespace != run.namespace || receipt.RawSelected != (RawSelectedCensus{Nodes: 1, Leaves: 1}) || receipt.RawSelectedError != "" {
		t.Fatalf("completed receipt=%+v ok=%v", receipt, ok)
	}
	if _, ok := TakePhase0ADiagnosticRunReceipt(); ok {
		t.Fatal("record/end interleave published more than one receipt")
	}
	endSession()
}

func TestPhase0ADiagnosticRunRecordAfterEndClaimIsRejectedBeforeWork(t *testing.T) {
	endSession := phase0ABeginDiagnosticTestSession(t, 1)
	core := phase0ATestCore(t)
	run, err := BeginPhase0ADiagnosticRun(core)
	if err != nil {
		t.Fatal(err)
	}
	endClaimed := make(chan struct{})
	allowEnd := make(chan struct{})
	endResult := make(chan error, 1)
	go func() {
		endResult <- endPhase0ADiagnosticRun(run, func() {
			close(endClaimed)
			<-allowEnd
		})
	}()
	<-endClaimed
	recordWorkStarted := false
	recordErr := recordPhase0ADiagnosticAcceptedRoots(core, []SubtreeID{99999}, func() { recordWorkStarted = true })
	if !isPhase0AError(recordErr, Phase0AErrorRunActive, "") || recordWorkStarted {
		close(allowEnd)
		t.Fatalf("record after end claim err=%v work_started=%v", recordErr, recordWorkStarted)
	}
	close(allowEnd)
	if err := <-endResult; err != nil {
		t.Fatal(err)
	}
	receipt, ok := TakePhase0ADiagnosticRunReceipt()
	if !ok || receipt.Namespace != run.namespace || receipt.RawSelected != (RawSelectedCensus{}) || receipt.RawSelectedError != "" {
		t.Fatalf("completed receipt=%+v ok=%v", receipt, ok)
	}
	if _, ok := TakePhase0ADiagnosticRunReceipt(); ok {
		t.Fatal("end-first interleave published more than one receipt")
	}
	endSession()
}

func phase0ABeginDiagnosticTestSession(t *testing.T, maxCores uint64) func() {
	t.Helper()
	if err := ResetPhase0ADiagnosticRunReceipts(); err != nil {
		t.Fatal(err)
	}
	if err := BeginPhase0AObserverSession(Phase0AObserverLimits{MaxCores: maxCores, MaxRecords: 64, MaxBytes: 8192, MaxFrames: 8}); err != nil {
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
		phase0ADiagnosticRuns.Lock()
		clear(phase0ADiagnosticRuns.active)
		phase0ADiagnosticRuns.Unlock()
		_ = EndPhase0AObserverSession()
	})
	return func() {
		t.Helper()
		if err := EndPhase0AObserverSession(); err != nil {
			t.Fatal(err)
		}
		ended = true
	}
}
