package pythonevidence

import (
	"testing"

	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGroundedFindingRequiresNativeRangeAndRevalidatesOnRevisionBump(t *testing.T) {
	ledger := NewExecutionLedger(ExecutionRecord{
		ExecutionID:    "exec-gf-1",
		CallID:         "call-gf-1",
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
	rev1 := pythondocuments.DocumentRevision{
		DocumentID:       "doc-auth",
		DocumentRevision: "rev-1",
		DiskSHA256:       "sha256:aaa",
	}
	rev2 := pythondocuments.DocumentRevision{
		DocumentID:       "doc-auth",
		DocumentRevision: "rev-2",
		DiskSHA256:       "sha256:bbb",
	}

	// Guest reads the file and prints a discovery hint.
	err := ledger.RecordDocumentRead(pythondocuments.DocumentReadRecord{
		ExecutionID:           "exec-gf-1",
		OperationID:           "op-read-auth",
		Target:                target,
		Revision:              rev1,
		BytesDeliveredToGuest: 512,
	}, "op-sys-read", 1, nil, nil)
	testutil.FailErr(t, "RecordDocumentRead", err)
	ledger.RecordScreenedModelOutput("internal/auth/check.go:40-48: missing nil check")

	hint := DiscoveryHint{
		ExecutionID: "exec-gf-1",
		Target:      target,
		StartLine:   40,
		EndLine:     48,
		Symbol:      "VerifyToken",
	}

	// 1. Before native range delivery, hint is NOT grounded.
	res1 := CheckGroundedFinding(hint, ledger, rev1)
	if res1.Grounded || res1.NeedsRevalidation {
		t.Fatalf("expected ungrounded before native range delivery, got %+v", res1)
	}

	// 2. Deliver bounded native source range covering lines 35..55 at rev1.
	err = ledger.RecordNativeSourceRange(NativeSourceRangeFact{
		Target:     target,
		Revision:   rev1,
		StartLine:  35,
		EndLine:    55,
		HostHandle: "handle-read-001",
	})
	testutil.FailErr(t, "RecordNativeSourceRange", err)

	res2 := CheckGroundedFinding(hint, ledger, rev1)
	if !res2.Grounded || res2.NeedsRevalidation || res2.Delivery == nil || res2.Delivery.HostHandle != "handle-read-001" {
		t.Fatalf("expected grounded finding at matching revision, got %+v", res2)
	}

	// 3. Revision changes to rev2 -> forces revalidation.
	res3 := CheckGroundedFinding(hint, ledger, rev2)
	if res3.Grounded || !res3.NeedsRevalidation {
		t.Fatalf("expected Grounded=false and NeedsRevalidation=true after revision bump, got %+v", res3)
	}
}

func TestReachabilityOnlyRecoveryAcrossSurfaces(t *testing.T) {
	stopCtx := RecoveryStopContext{
		StopCode:              StopPythonToolRequired,
		StoppedOperationID:    "op-cmd-1",
		StoppedOperation:      "python3 script.py",
		PriorCompletedEffects: 2,
	}

	// 1. Direct Python offered + project runner available.
	adv1 := BuildRecoveryAdvice(ReachableSurface{
		DirectPythonOffered:    true,
		CommandOffered:         true,
		ProjectRunnerAvailable: true,
		ProjectRunnerCommand:   "external -- python3 -m pytest",
	}, stopCtx)
	if adv1.NoRecoveryRoute || len(adv1.Routes) != 2 {
		t.Fatalf("expected 2 reachable routes (direct_python + project_runner), got %+v", adv1)
	}
	if adv1.Routes[0].Kind != "direct_python" || adv1.Routes[1].Kind != "project_runner" {
		t.Fatalf("unexpected route kinds: %+v", adv1.Routes)
	}

	// 2. Direct Python NOT offered, but requestable via request_tools; command not offered.
	adv2 := BuildRecoveryAdvice(ReachableSurface{
		DirectPythonOffered:   false,
		CommandOffered:        false,
		RequestToolsAvailable: true,
		RequestableTools:      []string{"python"},
	}, stopCtx)
	if adv2.NoRecoveryRoute || len(adv2.Routes) != 1 {
		t.Fatalf("expected 1 request_tool route, got %+v", adv2)
	}
	r := adv2.Routes[0]
	if r.Kind != "request_tool" || r.RequiresLoader != "request_tools" || r.LoadArgument != "python" {
		t.Fatalf("expected request_tools loader with exact load arg 'python', got %+v", r)
	}

	// 3. Completely unavailable surface -> NoRecoveryRoute=true, reports stopped op and prior effects.
	adv3 := BuildRecoveryAdvice(ReachableSurface{
		DirectPythonOffered:    false,
		CommandOffered:         false,
		ProjectRunnerAvailable: false,
		RequestToolsAvailable:  false,
	}, stopCtx)
	if !adv3.NoRecoveryRoute || len(adv3.Routes) != 0 || adv3.PriorEffectsPreserved != 2 {
		t.Fatalf("expected NoRecoveryRoute with prior effects preserved, got %+v", adv3)
	}

	// 4. Read-only surface must NEVER suggest project_runner or mutating python execution.
	adv4 := BuildRecoveryAdvice(ReachableSurface{
		DirectPythonOffered:    false,
		CommandOffered:         true,
		ProjectRunnerAvailable: true,
		NativeReadOffered:      true,
		ReadOnly:               true,
	}, RecoveryStopContext{
		StopCode:              pythonruntime.StopEffectNotAllowed,
		StoppedOperationID:    "op-write-1",
		PriorCompletedEffects: 1,
	})
	for _, route := range adv4.Routes {
		if route.Kind == "project_runner" || route.Kind == "direct_python" {
			t.Fatalf("read-only surface must never suggest %q route: %+v", route.Kind, route)
		}
	}
	if len(adv4.Routes) != 1 || adv4.Routes[0].Kind != "native_read" {
		t.Fatalf("expected only native_read on read-only surface, got %+v", adv4.Routes)
	}

	// 5. Revision conflict on read-only surface suggests native_read only (no mutating retry).
	adv5 := BuildRecoveryAdvice(ReachableSurface{
		DirectPythonOffered: true,
		NativeReadOffered:   true,
		ReadOnly:            true,
	}, RecoveryStopContext{
		StopCode:           pythonruntime.StopRevisionConflict,
		StoppedOperationID: "op-pub-1",
		Target:             "src/app.py",
	})
	if len(adv5.Routes) != 1 || adv5.Routes[0].Kind != "native_read" {
		t.Fatalf("read-only conflict recovery must only offer native_read, got %+v", adv5.Routes)
	}
}
