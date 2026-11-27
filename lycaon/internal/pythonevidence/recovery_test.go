package pythonevidence

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestGroundedFindingRequiresNativeRangeAndRevalidatesOnRevisionBump(t *testing.T) {
	ledger := NewExecutionLedger(ExecutionRecord{
		ExecutionID:    "exec-gf-1",
		CallID:         "call-gf-1",
		SessionID:      "sess-1",
		Actor:          "coordinator",
		View:           "main",
		Surface:        "python",
		RuntimeImageID: "cpython-wasi-3.12@sha256:abc",
	}, nil, fixedClock())

	target := pythonruntime.CanonicalTarget{
		Namespace: pythonruntime.NamespaceWorkspace,
		RootID:    "root-1",
		RelPath:   "internal/auth/check.go",
	}
	rev1 := pythondocuments.DocumentRevision{DocumentID: "doc-auth", DocumentRevision: "rev-1", DiskSHA256: "sha256:aaa"}
	rev2 := pythondocuments.DocumentRevision{DocumentID: "doc-auth", DocumentRevision: "rev-2", DiskSHA256: "sha256:bbb"}

	err := ledger.RecordDocumentRead(pythondocuments.DocumentReadRecord{
		ExecutionID: "exec-gf-1", OperationID: "op-read-auth", Target: target, Revision: rev1, BytesDeliveredToGuest: 512,
	}, "op-sys-read", 1, nil, nil)
	testutil.FailErr(t, "RecordDocumentRead", err)
	ledger.RecordScreenedModelOutput("internal/auth/check.go:40-48: missing nil check")

	hint := DiscoveryHint{ExecutionID: "exec-gf-1", Target: target, StartLine: 40, EndLine: 48, Symbol: "VerifyToken"}

	res1 := CheckGroundedFinding(hint, ledger, rev1)
	if res1.Grounded || res1.NeedsRevalidation {
		t.Fatalf("expected ungrounded before native range delivery, got %+v", res1)
	}

	err = ledger.RecordNativeSourceRange(NativeSourceRangeFact{
		Target: target, Revision: rev1, StartLine: 35, EndLine: 55, HostHandle: "handle-read-001",
	})
	testutil.FailErr(t, "RecordNativeSourceRange", err)

	res2 := CheckGroundedFinding(hint, ledger, rev1)
	if !res2.Grounded || res2.NeedsRevalidation || res2.Delivery == nil || res2.Delivery.HostHandle != "handle-read-001" {
		t.Fatalf("expected grounded finding at matching revision, got %+v", res2)
	}

	res3 := CheckGroundedFinding(hint, ledger, rev2)
	if res3.Grounded || !res3.NeedsRevalidation {
		t.Fatalf("expected Grounded=false and NeedsRevalidation=true after revision bump, got %+v", res3)
	}
}

func TestReachableSurfaceFromRequestDerivesFromFrozenToolReach(t *testing.T) {
	reqOffered := toolreach.Offered([]string{"python", "command", "read"})
	sOffered := ReachableSurfaceFromRequest(reqOffered, false, "external -- python3 -m pytest")
	if !sOffered.DirectPythonOffered || !sOffered.CommandOffered || !sOffered.NativeReadOffered || !sOffered.ProjectRunnerAvailable {
		t.Fatalf("unexpected offered surface: %+v", sOffered)
	}

	reqLoadable := toolreach.Freeze(
		[]string{"command", "request_tools"},
		toolsurface.Compile([]string{"command", "request_tools"}, []string{"python"}),
	)
	sLoadable := ReachableSurfaceFromRequest(reqLoadable, false, "")
	if sLoadable.DirectPythonOffered || !sLoadable.RequestToolsAvailable || !slices.Equal(sLoadable.RequestableTools, []string{"python"}) {
		t.Fatalf("unexpected loadable surface: %+v", sLoadable)
	}

	// Read-only posture suppresses external project runner even when command is offered.
	sReadOnly := ReachableSurfaceFromRequest(reqOffered, true, "external -- python3 -m pytest")
	if !sReadOnly.ReadOnly || sReadOnly.ProjectRunnerAvailable || sReadOnly.ProjectRunnerCommand != "" {
		t.Fatalf("read-only surface must suppress project runner: %+v", sReadOnly)
	}
}

func TestOARRecoveryFactsSchemaAndReachability(t *testing.T) {
	stopCtx := RecoveryStopContext{
		PolicyCode:            StopPythonToolRequired,
		InitiatingTool:        "command",
		StoppedOperationID:    "op-cmd-1",
		StoppedOperation:      "python3 script.py",
		PriorCompletedEffects: 2,
	}

	// 1. Direct Python offered + admitted project runner.
	f1 := BuildOARRecoveryFacts(ReachableSurface{
		DirectPythonOffered:    true,
		CommandOffered:         true,
		ProjectRunnerAvailable: true,
		ProjectRunnerCommand:   "external -- python3 -m pytest",
	}, stopCtx)
	if f1.NoRecoveryRoute || f1.ReachableDirectPython == nil || f1.AdmittedExternalRunner == nil {
		t.Fatalf("expected direct python + admitted external runner, got %+v", f1)
	}
	if !slices.Equal(f1.ReachableDirectPython.ModeArgs, []string{"code", "script", "module"}) ||
		!slices.Equal(f1.ReachableDirectPython.OptionalArgs, []string{"cwd", "args"}) ||
		strings.Contains(f1.ReachableDirectPython.CallableForm, "mode=") {
		t.Fatalf("python call form must match real schema (code/script/module, cwd, args), got %+v", f1.ReachableDirectPython)
	}

	// 2. Direct Python NOT offered, requestable via request_tools(need="python").
	f2 := BuildOARRecoveryFacts(ReachableSurface{
		DirectPythonOffered:    false,
		CommandOffered:         true,
		ProjectRunnerAvailable: true,
		ProjectRunnerCommand:   "",
		RequestToolsAvailable:  true,
		RequestableTools:       []string{"python"},
	}, stopCtx)
	if f2.NoRecoveryRoute || f2.ReachableDirectPython != nil || f2.RequestableNeed == nil {
		t.Fatalf("expected requestable_need only, got %+v", f2)
	}
	if f2.RequestableNeed.Tool != "request_tools" || f2.RequestableNeed.Need != "python" ||
		f2.RequestableNeed.CallableForm != `request_tools(need="python")` {
		t.Fatalf("request_tools must use exact `need` schema arg, got %+v", f2.RequestableNeed)
	}
	if f2.AdmittedExternalRunner != nil {
		t.Fatalf("must never emit an external runner when no command is admitted, got %+v", f2.AdmittedExternalRunner)
	}

	// 3. Completely unavailable surface -> NoRecoveryRoute=true, preserves prior effects.
	f3 := BuildOARRecoveryFacts(ReachableSurface{}, stopCtx)
	if !f3.NoRecoveryRoute || f3.PriorEffects != 2 || f3.ReachableDirectPython != nil || f3.RequestableNeed != nil || f3.AdmittedExternalRunner != nil {
		t.Fatalf("expected NoRecoveryRoute with prior effects preserved, got %+v", f3)
	}

	// 4. Read-only surface must NEVER emit external runner or mutating python retry.
	f4 := BuildOARRecoveryFacts(ReachableSurface{
		DirectPythonOffered:    true,
		CommandOffered:         true,
		ProjectRunnerAvailable: true,
		ProjectRunnerCommand:   "external -- python3 -m pytest",
		NativeReadOffered:      true,
		ReadOnly:               true,
	}, RecoveryStopContext{
		PolicyCode:            pythonruntime.StopEffectNotAllowed,
		InitiatingTool:        "command",
		StoppedOperationID:    "op-write-1",
		PriorCompletedEffects: 1,
		Target:                "src/app.py",
	})
	if f4.AdmittedExternalRunner != nil || f4.ReachableDirectPython != nil || f4.ReachableNativeRead == nil {
		t.Fatalf("read-only surface must only expose native read, got %+v", f4)
	}
}
