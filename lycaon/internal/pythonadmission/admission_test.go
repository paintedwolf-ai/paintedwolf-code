package pythonadmission

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestErrnoOutcomeTableDistinguishesRecoverableAndTerminal(t *testing.T) {
	table := DefaultErrnoOutcomeTable()
	if len(table) == 0 {
		t.Fatal("DefaultErrnoOutcomeTable returned empty table")
	}

	recoverable := map[AdmissionCondition]pythonruntime.Errno{
		ConditionMissingFile:         pythonruntime.ErrnoENOENT,
		ConditionMissingRuntimeProbe: pythonruntime.ErrnoENOENT,
		ConditionNameCollision:       pythonruntime.ErrnoEEXIST,
		ConditionIsDirectory:         pythonruntime.ErrnoEISDIR,
		ConditionNotDirectory:        pythonruntime.ErrnoENOTDIR,
		ConditionCrossDeviceRename:   pythonruntime.ErrnoEXDEV,
		ConditionInvalidArgument:     pythonruntime.ErrnoEINVAL,
	}
	for cond, wantErrno := range recoverable {
		entry, ok := LookupOutcome(cond)
		if !ok {
			t.Fatalf("missing table entry for %q", cond)
		}
		if entry.Disposition != DispositionRecoverableErrno || entry.Decision != pythonruntime.DecisionDeny || entry.Errno != wantErrno {
			t.Fatalf("condition %q = %+v, want recoverable DecisionDeny with errno %s", cond, entry, wantErrno)
		}
	}

	terminal := map[AdmissionCondition]pythonruntime.StopCode{
		ConditionReadOnlyWrite:         pythonruntime.StopEffectNotAllowed,
		ConditionOutOfScopePath:        pythonruntime.StopEffectNotAllowed,
		ConditionProtectedSink:         pythonruntime.StopEffectNotAllowed,
		ConditionEffectDenied:          pythonruntime.StopEffectNotAllowed,
		ConditionApprovalDeclined:      pythonruntime.StopEffectNotAllowed,
		ConditionApprovalCancelled:     pythonruntime.StopCancelled,
		ConditionHostRestart:           pythonruntime.StopCancelled,
		ConditionRevisionConflict:      pythonruntime.StopRevisionConflict,
		ConditionWorkLimitExceeded:     pythonruntime.StopWorkLimitExceeded,
		ConditionParkedBudgetExceeded:  pythonruntime.StopWorkLimitExceeded,
		ConditionUnsupportedSharedView: pythonruntime.StopEffectNotAllowed,
	}
	for cond, wantStop := range terminal {
		entry, ok := LookupOutcome(cond)
		if !ok {
			t.Fatalf("missing table entry for %q", cond)
		}
		if entry.Disposition != DispositionTerminalStop || entry.Decision != pythonruntime.DecisionStop || entry.StopCode != wantStop {
			t.Fatalf("condition %q = %+v, want terminal DecisionStop with stop %s", cond, entry, wantStop)
		}
	}
}

func TestReadOnlyRefusalLatchesStopAndSealsStreamsAndPipe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clk := pythonruntime.NewFixedClock(time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC))
	latch := pythonruntime.NewStopLatch(cancel)
	store := NewMemoryEvidenceStore()

	// Both a buffered stdout and a live pipe consumer must seal at the stop point.
	var stdoutBuf bytes.Buffer
	sealedStdout := pythonruntime.NewSealedWriter(&stdoutBuf, latch, 0)

	pipeR, pipeW := io.Pipe()
	sealedPipe := pythonruntime.NewSealedWriter(pipeW, latch, 0)
	latch.RegisterSealer(func() { _ = pipeW.Close() })

	pipeDone := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(pipeR)
		pipeDone <- data
	}()

	_, err := sealedStdout.Write([]byte("before-stop\n"))
	testutil.FailErr(t, "write stdout before stop", err)
	_, err = sealedPipe.Write([]byte("pipe-before-stop\n"))
	testutil.FailErr(t, "write pipe before stop", err)

	obs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-ro-1",
		CallID:      "call-abc",
		Actor:       "agent:coder",
		View:        "workspace:main",
		Surface:     "direct_python",
		ReadOnly:    true,
		StopLatch:   latch,
		Clock:       clk,
		Evidence:    store,
	})

	frame := &pythonruntime.DeclaredFrame{
		File:       "script.py",
		Line:       14,
		Function:   "write_output",
		Confidence: pythonruntime.FrameDeclared,
	}
	dec, err := obs.Admit(ctx, pythonruntime.ReachedOperation{
		OperationID: "op-ro-write",
		Family:      pythonruntime.EffectFilesystemWrite,
		Operation:   "open_write",
		Target: pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceWorkspace,
			RootID:    "root-1",
			RelPath:   "README.md",
		},
		DeclaredFrame: frame,
	})
	testutil.FailErr(t, "admit read-only write", err)

	if dec.Kind != pythonruntime.DecisionStop {
		t.Fatalf("decision kind = %s, want stop", dec.Kind)
	}
	if dec.Stop == nil || dec.Stop.Code != pythonruntime.StopEffectNotAllowed {
		t.Fatalf("stop = %+v, want COMMAND_EFFECT_NOT_ALLOWED", dec.Stop)
	}
	if !latch.Stopped() {
		t.Fatal("StopLatch was not latched on read-only refusal")
	}
	if ctx.Err() == nil {
		t.Fatal("execution context was not cancelled by StopLatch")
	}

	// Post-stop writes to stdout and live pipe must be rejected with zero bytes delivered.
	if n, wErr := sealedStdout.Write([]byte("post-stop\n")); !errors.Is(wErr, pythonruntime.ErrStreamSealed) || n != 0 {
		t.Fatalf("post-stop stdout write = (%d, %v), want (0, ErrStreamSealed)", n, wErr)
	}
	if n, wErr := sealedPipe.Write([]byte("pipe-post-stop\n")); !errors.Is(wErr, pythonruntime.ErrStreamSealed) || n != 0 {
		t.Fatalf("post-stop pipe write = (%d, %v), want (0, ErrStreamSealed)", n, wErr)
	}

	pipeBytes := <-pipeDone
	if string(pipeBytes) != "pipe-before-stop\n" {
		t.Fatalf("pipe consumer received %q, want %q", string(pipeBytes), "pipe-before-stop\n")
	}
	if stdoutBuf.String() != "before-stop\n" {
		t.Fatalf("stdout received %q, want %q", stdoutBuf.String(), "before-stop\n")
	}

	records := store.Admissions()
	if len(records) != 1 {
		t.Fatalf("recorded %d admissions, want 1", len(records))
	}
	rec := records[0]
	if rec.CallID != "call-abc" || rec.Actor != "agent:coder" || rec.View != "workspace:main" || rec.Surface != "direct_python" {
		t.Fatalf("pinned identity mismatch: %+v", rec)
	}
	if rec.StopCode != pythonruntime.StopEffectNotAllowed || rec.DeclaredFrame == nil || rec.DeclaredFrame.Line != 14 {
		t.Fatalf("unexpected admission record: %+v", rec)
	}
}

func TestOrdinaryMissingFileReturnsRecoverableErrnoWithoutLatchingStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	latch := pythonruntime.NewStopLatch(cancel)
	store := NewMemoryEvidenceStore()

	obs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-enoent",
		CallID:      "call-enoent",
		Actor:       "agent:coder",
		View:        "workspace:main",
		Surface:     "direct_python",
		StopLatch:   latch,
		Evidence:    store,
		Policy: func(_ context.Context, op pythonruntime.ReachedOperation) PolicyDecision {
			if op.Target.RelPath == "missing.txt" {
				return PolicyDecision{
					Kind:      pythonruntime.DecisionDeny,
					Condition: ConditionMissingFile,
					Reason:    "file does not exist",
				}
			}
			return PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
	})

	dec, err := obs.Admit(ctx, pythonruntime.ReachedOperation{
		OperationID: "op-missing",
		Family:      pythonruntime.EffectFilesystemRead,
		Operation:   "open_read",
		Target: pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceWorkspace,
			RelPath:   "missing.txt",
		},
	})
	testutil.FailErr(t, "admit missing file", err)

	if dec.Kind != pythonruntime.DecisionDeny || dec.Errno != pythonruntime.ErrnoENOENT {
		t.Fatalf("decision = %+v, want DecisionDeny with ErrnoENOENT", dec)
	}
	if dec.Stop != nil {
		t.Fatalf("ordinary ENOENT must not carry a TerminalStop: %+v", dec.Stop)
	}
	if latch.Stopped() {
		t.Fatal("ordinary ENOENT must not latch StopLatch")
	}

	// Subsequent operation in the same execution must still succeed cleanly.
	dec2, err := obs.Admit(ctx, pythonruntime.ReachedOperation{
		OperationID: "op-existing",
		Family:      pythonruntime.EffectFilesystemRead,
		Operation:   "open_read",
		Target: pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceWorkspace,
			RelPath:   "present.txt",
		},
	})
	testutil.FailErr(t, "admit follow-up read", err)
	if dec2.Kind != pythonruntime.DecisionAllow {
		t.Fatalf("follow-up decision = %s, want allow", dec2.Kind)
	}

	obs.RecordOutcome(ctx, "op-existing", pythonruntime.OperationOutcome{
		Succeeded:        true,
		BytesTransferred: 42,
	})
	recs := store.Admissions()
	if len(recs) != 2 || recs[1].Outcome == nil || recs[1].Outcome.BytesTransferred != 42 {
		t.Fatalf("outcome not attached to admission record: %+v", recs)
	}
}

func TestWorkspaceScopeProtectedSinkAndHostWorkLimit(t *testing.T) {
	t.Run("out of scope traversal", func(t *testing.T) {
		_, cancel := context.WithCancel(context.Background())
		defer cancel()
		latch := pythonruntime.NewStopLatch(cancel)
		obs := NewAdmissionObserver(ObserverConfig{
			ExecutionID:  "exec-scope",
			AllowedRoots: []string{"root-a"},
			StopLatch:    latch,
		})
		dec, err := obs.Admit(context.Background(), pythonruntime.ReachedOperation{
			Family:    pythonruntime.EffectFilesystemRead,
			Operation: "open_read",
			Target: pythonruntime.CanonicalTarget{
				Namespace: pythonruntime.NamespaceWorkspace,
				RootID:    "root-a",
				RelPath:   "../secrets.txt",
			},
		})
		testutil.FailErr(t, "admit traversal", err)
		if dec.Kind != pythonruntime.DecisionStop || dec.Stop == nil || dec.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("traversal decision = %+v, want COMMAND_EFFECT_NOT_ALLOWED", dec)
		}
	})

	t.Run("protected sink", func(t *testing.T) {
		_, cancel := context.WithCancel(context.Background())
		defer cancel()
		latch := pythonruntime.NewStopLatch(cancel)
		obs := NewAdmissionObserver(ObserverConfig{
			ExecutionID:    "exec-prot",
			ProtectedPaths: []string{".ssh", "credentials.json"},
			StopLatch:      latch,
		})
		dec, err := obs.Admit(context.Background(), pythonruntime.ReachedOperation{
			Family:    pythonruntime.EffectFilesystemWrite,
			Operation: "open_write",
			Target: pythonruntime.CanonicalTarget{
				Namespace: pythonruntime.NamespaceWorkspace,
				RelPath:   ".ssh/id_rsa",
			},
		})
		testutil.FailErr(t, "admit protected sink", err)
		if dec.Kind != pythonruntime.DecisionStop || dec.Stop == nil || dec.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("protected sink decision = %+v, want COMMAND_EFFECT_NOT_ALLOWED", dec)
		}
	})

	t.Run("host work limit exceeded", func(t *testing.T) {
		_, cancel := context.WithCancel(context.Background())
		defer cancel()
		latch := pythonruntime.NewStopLatch(cancel)
		obs := NewAdmissionObserver(ObserverConfig{
			ExecutionID: "exec-work",
			MaxHostWork: 100,
			StopLatch:   latch,
		})
		dec1, err := obs.Admit(context.Background(), pythonruntime.ReachedOperation{
			OperationID:  "op-1",
			Family:       pythonruntime.EffectHostWork,
			Operation:    "expand",
			HostWorkCost: 60,
		})
		testutil.FailErr(t, "admit work 1", err)
		if dec1.Kind != pythonruntime.DecisionAllow || obs.HostWorkUsed() != 60 {
			t.Fatalf("work 1 = (%+v, used=%d), want allow/60", dec1, obs.HostWorkUsed())
		}
		dec2, err := obs.Admit(context.Background(), pythonruntime.ReachedOperation{
			OperationID:  "op-2",
			Family:       pythonruntime.EffectHostWork,
			Operation:    "expand",
			HostWorkCost: 50,
		})
		testutil.FailErr(t, "admit work 2", err)
		if dec2.Kind != pythonruntime.DecisionStop || dec2.Stop == nil || dec2.Stop.Code != pythonruntime.StopWorkLimitExceeded {
			t.Fatalf("work 2 = %+v, want PYTHON_WORK_LIMIT_EXCEEDED", dec2)
		}
	})
}

type fakeSubprocessRunner struct {
	called int
	last   SubprocessRequest
}

func (f *fakeSubprocessRunner) Run(_ context.Context, req SubprocessRequest) (int, []byte, []byte, error) {
	f.called++
	f.last = req
	return 0, []byte("ok\n"), nil, nil
}

func TestSubprocessAdmissionBoundary(t *testing.T) {
	store := NewMemoryEvidenceStore()
	obs := NewAdmissionObserver(ObserverConfig{
		ExecutionID: "exec-sub",
		CallID:      "call-sub",
		Evidence:    store,
		Policy: func(_ context.Context, _ pythonruntime.ReachedOperation) PolicyDecision {
			return PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
	})
	runner := &fakeSubprocessRunner{}
	admitter := NewSubprocessAdmitter(SubprocessAdmitterConfig{
		ExecutionID: "exec-sub",
		CallID:      "call-sub",
		Observer:    obs,
		Runner:      runner,
		Evidence:    store,
	})

	// 1. Refuse unsupported shared-view combination BEFORE launch.
	_, err := admitter.Execute(context.Background(), SubprocessRequest{
		OperationID:           "sub-1",
		Argv:                  []string{"git", "diff", "file with spaces.py"},
		CWD:                   ".",
		NeedsUnpublishedBytes: true,
		HasMediatedView:       false,
		HasExplicitStream:     false,
	})
	if !errors.Is(err, ErrUnsupportedSharedView) {
		t.Fatalf("unsupported shared view err = %v, want ErrUnsupportedSharedView", err)
	}
	if runner.called != 0 {
		t.Fatalf("runner was invoked %d times for unsupported shared view, want 0", runner.called)
	}

	// 2. Allow argv-preserving subprocess with mediated view.
	argv := []string{"git", "status", "--porcelain", "a b/c.py"}
	res, err := admitter.Execute(context.Background(), SubprocessRequest{
		OperationID:           "sub-2",
		Argv:                  argv,
		CWD:                   "sub",
		NeedsUnpublishedBytes: true,
		HasMediatedView:       true,
	})
	testutil.FailErr(t, "execute allowed subprocess", err)
	if runner.called != 1 {
		t.Fatalf("runner called %d times, want 1", runner.called)
	}
	if len(runner.last.Argv) != len(argv) || runner.last.Argv[3] != "a b/c.py" {
		t.Fatalf("argv not preserved byte-for-byte: %#v", runner.last.Argv)
	}
	if res.Record.ObservationLimits != ExternalProcessObservationLimits {
		t.Fatalf("observation limits = %q, want %q", res.Record.ObservationLimits, ExternalProcessObservationLimits)
	}
	if len(store.Subprocesses()) != 2 {
		t.Fatalf("recorded %d subprocess records, want 2", len(store.Subprocesses()))
	}
}

func TestObserverFailsClosedWithoutPolicyAndEnforcesAllowedEffects(t *testing.T) {
	t.Run("nil policy fails closed for http subprocess and preview", func(t *testing.T) {
		for _, fam := range []pythonruntime.EffectFamily{
			pythonruntime.EffectHTTP,
			pythonruntime.EffectSubprocess,
			pythonruntime.EffectPreviewListener,
		} {
			_, cancel := context.WithCancel(context.Background())
			latch := pythonruntime.NewStopLatch(cancel)
			obs := NewAdmissionObserver(ObserverConfig{
				ExecutionID: "exec-nil-pol-" + string(fam),
				StopLatch:   latch,
			})
			dec, err := obs.Admit(context.Background(), pythonruntime.ReachedOperation{
				Family:    fam,
				Operation: "test",
			})
			cancel()
			testutil.FailErr(t, "admit "+string(fam), err)
			if dec.Kind != pythonruntime.DecisionStop || dec.Stop == nil || dec.Stop.Code != pythonruntime.StopEffectNotAllowed {
				t.Fatalf("family %s with nil policy = %+v, want COMMAND_EFFECT_NOT_ALLOWED", fam, dec)
			}
			if !latch.Stopped() {
				t.Fatalf("family %s with nil policy did not latch StopLatch", fam)
			}
		}
	})

	t.Run("allowed effects enforces mutation network_egress and external_exec", func(t *testing.T) {
		allowAll := func(_ context.Context, _ pythonruntime.ReachedOperation) PolicyDecision {
			return PolicyDecision{Kind: pythonruntime.DecisionAllow}
		}
		cases := []struct {
			name   string
			fam    pythonruntime.EffectFamily
			target pythonruntime.CanonicalTarget
			ae     AllowedEffects
		}{
			{
				name:   "mutation disallowed",
				fam:    pythonruntime.EffectFilesystemWrite,
				target: pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceWorkspace, RelPath: "a.txt"},
				ae:     AllowedEffects{AllowMutation: false, AllowNetworkEgress: true, AllowExternalExec: true},
			},
			{
				name:   "network_egress disallowed",
				fam:    pythonruntime.EffectHTTP,
				target: pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceHTTP, RootID: "example.com", RelPath: "/"},
				ae:     AllowedEffects{AllowMutation: true, AllowNetworkEgress: false, AllowExternalExec: true},
			},
			{
				name:   "external_exec disallowed",
				fam:    pythonruntime.EffectSubprocess,
				target: pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceProcess, RelPath: "git"},
				ae:     AllowedEffects{AllowMutation: true, AllowNetworkEgress: true, AllowExternalExec: false},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, cancel := context.WithCancel(context.Background())
				defer cancel()
				latch := pythonruntime.NewStopLatch(cancel)
				ae := tc.ae
				obs := NewAdmissionObserver(ObserverConfig{
					ExecutionID:    "exec-ae",
					AllowedEffects: &ae,
					Policy:         allowAll,
					StopLatch:      latch,
				})
				dec, err := obs.Admit(context.Background(), pythonruntime.ReachedOperation{
					Family:    tc.fam,
					Operation: "op",
					Target:    tc.target,
				})
				testutil.FailErr(t, "admit "+tc.name, err)
				if dec.Kind != pythonruntime.DecisionStop || dec.Stop == nil || dec.Stop.Code != pythonruntime.StopEffectNotAllowed {
					t.Fatalf("%s decision = %+v, want COMMAND_EFFECT_NOT_ALLOWED", tc.name, dec)
				}
			})
		}
	})
}
