package pythonevidence

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
)

func buildRichTestLedger(t *testing.T) *ExecutionLedger {
	t.Helper()
	ledger := NewExecutionLedger(ExecutionRecord{
		ExecutionID:    "exec-den-1",
		CallID:         "call-den-1",
		SessionID:      "sess-alpha",
		CheckoutID:     "chk-alpha",
		Actor:          "worker-alpha",
		View:           "worktree-alpha",
		Surface:        "python",
		RuntimeImageID: "cpython-wasi-3.12@sha256:abc",
	}, nil, fixedClock())

	for i := 1; i <= 5; i++ {
		target := pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceWorkspace,
			RootID:    "root-1",
			Branch:    "worktree-alpha",
			RelPath:   fmt.Sprintf("pkg/file_%d.py", i),
		}
		err := ledger.RecordDocumentRead(pythondocuments.DocumentReadRecord{
			ExecutionID: "exec-den-1",
			OperationID: fmt.Sprintf("op-read-%d", i),
			Target:      target,
			Revision: pythondocuments.DocumentRevision{
				DocumentID:       fmt.Sprintf("doc-%d", i),
				DocumentRevision: fmt.Sprintf("rev-%d", i),
			},
			BytesDeliveredToGuest: int64(100 * i),
		}, "op-loop-read", 1, nil, nil)
		testutil.FailErr(t, "RecordDocumentRead", err)
	}

	err := ledger.RecordPublication(pythondocuments.PublicationRecord{
		ExecutionID: "exec-den-1",
		OperationID: "op-pub-ok",
		Target: pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceWorkspace,
			RootID:    "root-1",
			RelPath:   "pkg/summary.json",
		},
		Operation:    "write",
		BaseRevision: pythondocuments.DocumentRevision{DocumentID: "doc-sum", DocumentRevision: "rev-1"},
		CommittedRevision: &pythondocuments.DocumentRevision{
			DocumentID:       "doc-sum",
			DocumentRevision: "rev-2",
		},
		BytesWritten: 256,
	}, "op-sys-settle", "pub-101", AttributionPythonWorkspacePublication, nil, nil)
	testutil.FailErr(t, "RecordPublication ok", err)

	conflictTarget := pythonruntime.CanonicalTarget{
		Namespace: pythonruntime.NamespaceWorkspace,
		RootID:    "root-1",
		RelPath:   "pkg/conflict.py",
	}
	err = ledger.RecordPublication(pythondocuments.PublicationRecord{
		ExecutionID: "exec-den-1",
		OperationID: "op-pub-conflict",
		Target:      conflictTarget,
		Operation:   "write",
		Conflict: &pythonruntime.RevisionConflict{
			Target:           conflictTarget,
			ExpectedRevision: "rev-stale-1",
			ActualRevision:   "rev-current-2",
		},
	}, "op-sys-settle", "pub-conflict-1", AttributionPythonWorkspacePublication, nil, nil)
	testutil.FailErr(t, "RecordPublication conflict", err)

	ledger.RecordAdmission(pythonadmission.AdmissionRecord{
		ExecutionID: "exec-den-1",
		OperationID: "op-refused-1",
		Family:      pythonruntime.EffectSubprocess,
		Operation:   "spawn",
		AuthoritativeTarget: pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceWorkspace,
			RootID:    "root-1",
			RelPath:   "/usr/bin/rm",
		},
		Decision: pythonruntime.DecisionStop,
		StopCode: pythonruntime.StopEffectNotAllowed,
		Reason:   "outside effect policy",
	}, "exec-den-1", nil)

	err = ledger.RecordSubprocess(pythonadmission.SubprocessRecord{
		ExecutionID:       "exec-den-1",
		OperationID:       "op-sub-opaque",
		Argv:              []string{"git", "fetch", "https://user:topsecret99@github.com/org/repo.git"},
		MediatedView:      false,
		ObservationLimits: "external child filesystem and network side effects unmediated",
		Decision:          pythonruntime.DecisionAllow,
		ExitCode:          0,
	}, "op-sys-sub")
	testutil.FailErr(t, "RecordSubprocess", err)

	err = ledger.RecordPreviewListener(pythonadmission.PreviewListenerRecord{
		JobID:       "job-preview-1",
		ExecutionID: "exec-den-1",
		Address:     "127.0.0.1",
		Port:        8000,
		Directory:   "dist",
		Active:      true,
	}, "op-sys-preview")
	testutil.FailErr(t, "RecordPreviewListener", err)

	ledger.RecordScreenedModelOutput("processed 5 files with token=ghp_abcdef1234567890")
	ledger.RecordCompletion(false, 1, &pythonruntime.TerminalStop{
		Code:        pythonruntime.StopRevisionConflict,
		OperationID: "op-pub-conflict",
	})
	return ledger
}

func TestDenProjectionGroupsRepeatedReadsAndRetainsMembers(t *testing.T) {
	ledger := buildRichTestLedger(t)
	proj := ProjectForDen(ledger, ViewerScope{
		Role:       RoleCoordinator,
		SessionID:  "sess-alpha",
		CheckoutID: "chk-alpha",
		Actor:      "coordinator",
		View:       "main",
	})

	if !proj.Visible || len(proj.ReadGroups) != 1 {
		t.Fatalf("expected visible projection with 1 read group, got visible=%v groups=%d", proj.Visible, len(proj.ReadGroups))
	}
	grp := proj.ReadGroups[0]
	if grp.FileCount != 5 || grp.ReadCount != 5 || grp.TotalBytesDelivered != 1500 || len(grp.Members) != 5 {
		t.Fatalf("unexpected read group summary: %+v", grp)
	}
	for i, m := range grp.Members {
		wantPath := fmt.Sprintf("pkg/file_%d.py", i+1)
		if m.Target.RelPath != wantPath || m.BytesDeliveredToGuest != int64(100*(i+1)) || m.Revision.DocumentRevision != fmt.Sprintf("rev-%d", i+1) {
			t.Fatalf("member %d mismatch: %+v", i, m)
		}
	}
}

func TestDenProjectionDiffsConflictsRefusalsAndUncertainExternals(t *testing.T) {
	ledger := buildRichTestLedger(t)
	proj := ProjectForDen(ledger, ViewerScope{
		Role:       RoleCoordinator,
		SessionID:  "sess-alpha",
		CheckoutID: "chk-alpha",
		Actor:      "coordinator",
		View:       "main",
	})

	if len(proj.PublishedDiffs) != 1 || proj.PublishedDiffs[0].AttributionKind != AttributionPythonWorkspacePublication {
		t.Fatalf("expected 1 python_workspace_publication diff, got %+v", proj.PublishedDiffs)
	}
	if len(proj.Conflicts) != 1 || proj.Conflicts[0].OperationID != "op-pub-conflict" ||
		proj.Conflicts[0].ExpectedRevision != "rev-stale-1" || proj.Conflicts[0].ActualRevision != "rev-current-2" {
		t.Fatalf("unexpected conflict projection: %+v", proj.Conflicts)
	}
	if len(proj.RefusedChildren) != 1 || proj.RefusedChildren[0].StopCode != pythonruntime.StopEffectNotAllowed {
		t.Fatalf("expected 1 refused child, got %+v", proj.RefusedChildren)
	}
	if len(proj.UncertainExternals) != 1 || !proj.UncertainExternals[0].Uncertain {
		t.Fatalf("expected 1 uncertain external subprocess, got %+v", proj.UncertainExternals)
	}
	if strings.Contains(strings.Join(proj.UncertainExternals[0].Argv, " "), "topsecret99") {
		t.Fatalf("secret leaked in Den subprocess projection: %v", proj.UncertainExternals[0].Argv)
	}
	if len(proj.ScreenedOutputs) != 1 || strings.Contains(proj.ScreenedOutputs[0].ScreenedText, "ghp_abcdef1234567890") {
		t.Fatalf("secret leaked in Den screened output projection: %+v", proj.ScreenedOutputs)
	}
}

func TestDenProjectionVisibilityScopingAndFailClosedNegativeControls(t *testing.T) {
	ledger := buildRichTestLedger(t)

	// Authorized coordinator within same session/checkout.
	coord := ProjectForDen(ledger, ViewerScope{
		Role: RoleCoordinator, SessionID: "sess-alpha", CheckoutID: "chk-alpha", Actor: "coordinator", View: "main",
	})
	if !coord.Visible || len(coord.PublishedDiffs) != 1 || len(coord.PreviewListeners) != 1 {
		t.Fatalf("authorized coordinator should see full projection, got %+v", coord)
	}

	// Authorized own worker.
	ownWorker := ProjectForDen(ledger, ViewerScope{
		Role: RoleWorker, SessionID: "sess-alpha", CheckoutID: "chk-alpha", Actor: "worker-alpha", View: "worktree-alpha",
	})
	if !ownWorker.Visible || ownWorker.RedactedForVisibility || len(ownWorker.PublishedDiffs) != 1 {
		t.Fatalf("own worker should see its execution, got %+v", ownWorker)
	}

	// Authorized read-only viewer on its OWN session, checkout, and view.
	ownRO := ProjectForDen(ledger, ViewerScope{
		Role: RoleReadOnly, SessionID: "sess-alpha", CheckoutID: "chk-alpha", Actor: "readonly-user", View: "worktree-alpha",
	})
	if !ownRO.Visible || !ownRO.ReadOnlyView || len(ownRO.ReadGroups) != 1 || len(ownRO.PublishedDiffs) != 0 || len(ownRO.PreviewListeners) != 0 {
		t.Fatalf("authorized read-only viewer should see read-only projection, got %+v", ownRO)
	}

	// Negative controls — all MUST fail closed (Visible=false, RedactedForVisibility=true, zero details):
	deniedCases := []struct {
		name  string
		scope ViewerScope
	}{
		{
			name:  "unrelated session coordinator",
			scope: ViewerScope{Role: RoleCoordinator, SessionID: "sess-other", CheckoutID: "chk-alpha", Actor: "coordinator", View: "main"},
		},
		{
			name:  "unrelated worker in same session",
			scope: ViewerScope{Role: RoleWorker, SessionID: "sess-alpha", CheckoutID: "chk-alpha", Actor: "worker-beta", View: "worktree-beta"},
		},
		{
			name:  "read-only viewer attempting cross-session read",
			scope: ViewerScope{Role: RoleReadOnly, SessionID: "sess-other", CheckoutID: "chk-alpha", Actor: "readonly-user", View: "worktree-alpha"},
		},
		{
			name:  "read-only viewer attempting cross-view read",
			scope: ViewerScope{Role: RoleReadOnly, SessionID: "sess-alpha", CheckoutID: "chk-alpha", Actor: "readonly-user", View: "worktree-beta"},
		},
		{
			name:  "read-only viewer missing checkout pin",
			scope: ViewerScope{Role: RoleReadOnly, SessionID: "sess-alpha", CheckoutID: "", Actor: "readonly-user", View: "worktree-alpha"},
		},
		{
			name:  "incomplete scope empty session_id",
			scope: ViewerScope{Role: RoleWorker, SessionID: "", CheckoutID: "chk-alpha", Actor: "worker-alpha", View: "worktree-alpha"},
		},
		{
			name:  "incomplete scope empty actor",
			scope: ViewerScope{Role: RoleWorker, SessionID: "sess-alpha", CheckoutID: "chk-alpha", Actor: "", View: "worktree-alpha"},
		},
		{
			name:  "incomplete scope empty view",
			scope: ViewerScope{Role: RoleWorker, SessionID: "sess-alpha", CheckoutID: "chk-alpha", Actor: "worker-alpha", View: ""},
		},
	}

	for _, tc := range deniedCases {
		t.Run(tc.name, func(t *testing.T) {
			p := ProjectForDen(ledger, tc.scope)
			if p.Visible || !p.RedactedForVisibility {
				t.Fatalf("%s: expected Visible=false, RedactedForVisibility=true, got %+v", tc.name, p)
			}
			if len(p.ReadGroups) != 0 || len(p.PublishedDiffs) != 0 || len(p.Conflicts) != 0 || len(p.ScreenedOutputs) != 0 {
				t.Fatalf("%s: denied projection must not leak execution details", tc.name)
			}
		})
	}
}
