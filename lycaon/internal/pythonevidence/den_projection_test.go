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
		Actor:          "worker-alpha",
		View:           "worktree-alpha",
		Surface:        "python",
		RuntimeImageID: "cpython-wasi-3.12@sha256:abc",
	}, nil, fixedClock())

	// 1. Loop reading 5 files under one parent operation.
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

	// 2. Published workspace diff.
	err := ledger.RecordPublication(pythondocuments.PublicationRecord{
		ExecutionID: "exec-den-1",
		OperationID: "op-pub-ok",
		Target: pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceWorkspace,
			RootID:    "root-1",
			RelPath:   "pkg/summary.json",
		},
		Operation: "write",
		BaseRevision: pythondocuments.DocumentRevision{
			DocumentID:       "doc-sum",
			DocumentRevision: "rev-1",
		},
		CommittedRevision: &pythondocuments.DocumentRevision{
			DocumentID:       "doc-sum",
			DocumentRevision: "rev-2",
		},
		BytesWritten: 256,
	}, "op-sys-settle", "pub-101", AttributionPythonWorkspacePublication, nil, nil)
	testutil.FailErr(t, "RecordPublication ok", err)

	// 3. Revision conflict on a second file.
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

	// 4. Refused child operation (COMMAND_EFFECT_NOT_ALLOWED).
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

	// 5. Opaque/uncertain external subprocess with secret in argv.
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

	// 6. Preview listener.
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
	proj := ProjectForDen(ledger, ProjectionScope{Role: RoleCoordinator})

	if !proj.Visible {
		t.Fatalf("coordinator projection must be visible")
	}
	if len(proj.ReadGroups) != 1 {
		t.Fatalf("expected 1 grouped read batch, got %d", len(proj.ReadGroups))
	}
	grp := proj.ReadGroups[0]
	if grp.FileCount != 5 || grp.ReadCount != 5 {
		t.Fatalf("expected 5 files / 5 reads, got %d / %d", grp.FileCount, grp.ReadCount)
	}
	// 100 + 200 + 300 + 400 + 500 = 1500 bytes.
	if grp.TotalBytesDelivered != 1500 {
		t.Fatalf("expected TotalBytesDelivered=1500, got %d", grp.TotalBytesDelivered)
	}
	if len(grp.Members) != 5 {
		t.Fatalf("expected 5 inspectable members, got %d", len(grp.Members))
	}
	for i, m := range grp.Members {
		wantPath := fmt.Sprintf("pkg/file_%d.py", i+1)
		if m.Target.RelPath != wantPath || m.BytesDeliveredToGuest != int64(100*(i+1)) {
			t.Fatalf("member %d mismatch: %+v", i, m)
		}
		if m.Revision.DocumentRevision != fmt.Sprintf("rev-%d", i+1) {
			t.Fatalf("member %d missing revision: %+v", i, m.Revision)
		}
	}
}

func TestDenProjectionDiffsConflictsRefusalsAndUncertainExternals(t *testing.T) {
	ledger := buildRichTestLedger(t)
	proj := ProjectForDen(ledger, ProjectionScope{Role: RoleCoordinator})

	if len(proj.PublishedDiffs) != 1 {
		t.Fatalf("expected 1 published diff, got %d", len(proj.PublishedDiffs))
	}
	if proj.PublishedDiffs[0].AttributionKind != AttributionPythonWorkspacePublication {
		t.Fatalf("expected python_workspace_publication attribution, got %q", proj.PublishedDiffs[0].AttributionKind)
	}

	if len(proj.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(proj.Conflicts))
	}
	c := proj.Conflicts[0]
	if c.OperationID != "op-pub-conflict" || c.ExpectedRevision != "rev-stale-1" || c.ActualRevision != "rev-current-2" {
		t.Fatalf("unexpected conflict projection: %+v", c)
	}

	if len(proj.RefusedChildren) != 1 || proj.RefusedChildren[0].StopCode != pythonruntime.StopEffectNotAllowed {
		t.Fatalf("expected 1 refused child with COMMAND_EFFECT_NOT_ALLOWED, got %+v", proj.RefusedChildren)
	}

	if len(proj.UncertainExternals) != 1 || !proj.UncertainExternals[0].Uncertain {
		t.Fatalf("expected 1 uncertain external subprocess, got %+v", proj.UncertainExternals)
	}
	// Secret in subprocess argv and stdout must be redacted in Den projection.
	if strings.Contains(strings.Join(proj.UncertainExternals[0].Argv, " "), "topsecret99") {
		t.Fatalf("secret leaked in Den subprocess projection: %v", proj.UncertainExternals[0].Argv)
	}
	if len(proj.ScreenedOutputs) != 1 || strings.Contains(proj.ScreenedOutputs[0].ScreenedText, "ghp_abcdef1234567890") {
		t.Fatalf("secret leaked in Den screened output projection: %+v", proj.ScreenedOutputs)
	}
}

func TestDenProjectionVisibilityScoping(t *testing.T) {
	ledger := buildRichTestLedger(t)

	// 1. Coordinator sees everything.
	coord := ProjectForDen(ledger, ProjectionScope{Role: RoleCoordinator})
	if !coord.Visible || len(coord.PublishedDiffs) != 1 || len(coord.PreviewListeners) != 1 {
		t.Fatalf("coordinator should see full projection, got %+v", coord)
	}

	// 2. Same worker sees its own execution.
	ownWorker := ProjectForDen(ledger, ProjectionScope{
		Role:  RoleWorker,
		Actor: "worker-alpha",
		View:  "worktree-alpha",
	})
	if !ownWorker.Visible || ownWorker.RedactedForVisibility || len(ownWorker.PublishedDiffs) != 1 {
		t.Fatalf("own worker should see its execution, got %+v", ownWorker)
	}

	// 3. Different worker is denied visibility.
	otherWorker := ProjectForDen(ledger, ProjectionScope{
		Role:  RoleWorker,
		Actor: "worker-beta",
		View:  "worktree-beta",
	})
	if otherWorker.Visible || !otherWorker.RedactedForVisibility {
		t.Fatalf("other worker must not see worker-alpha execution, got %+v", otherWorker)
	}
	if len(otherWorker.ReadGroups) != 0 || len(otherWorker.PublishedDiffs) != 0 || len(otherWorker.ScreenedOutputs) != 0 {
		t.Fatalf("redacted worker projection must omit execution details")
	}

	// 4. Read-only session gets read-only view (reads, conflicts, refusals; no mutation diffs or preview listeners).
	ro := ProjectForDen(ledger, ProjectionScope{Role: RoleReadOnly})
	if !ro.Visible || !ro.ReadOnlyView {
		t.Fatalf("read-only projection must be visible with ReadOnlyView=true, got %+v", ro)
	}
	if len(ro.ReadGroups) != 1 || len(ro.Conflicts) != 1 || len(ro.RefusedChildren) != 1 {
		t.Fatalf("read-only projection should include reads, conflicts, and refusals")
	}
	if len(ro.PublishedDiffs) != 0 || len(ro.PreviewListeners) != 0 {
		t.Fatalf("read-only projection must omit mutating diff internals and preview listeners")
	}
}
