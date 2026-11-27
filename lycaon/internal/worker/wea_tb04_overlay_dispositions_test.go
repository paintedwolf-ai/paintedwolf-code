package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/overlayplan"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	"github.com/lycaon/lycaon/internal/session"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerplan/plantest"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// tb04OutsideDiff independently computes changed relative paths between baselineDir and branchDir
// without calling any product diff code.
func tb04OutsideDiff(t *testing.T, baselineDir, branchDir string) []string {
	t.Helper()
	readTree := func(root string) map[string]string {
		out := map[string]string{}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				name := d.Name()
				if name == ".git" || name == ".lycaon" || name == ".paintedwolf" || name == ".meta" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(raw)
			out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
			return nil
		})
		testutil.FailErr(t, "tb04OutsideDiff walk "+root, err)
		return out
	}
	before := readTree(baselineDir)
	after := readTree(branchDir)
	seen := map[string]struct{}{}
	for p, h := range before {
		if after[p] != h {
			seen[p] = struct{}{}
		}
	}
	for p, h := range after {
		if before[p] != h {
			seen[p] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

type tb04StaticScanHook struct {
	planID string
}

func (h tb04StaticScanHook) PrepareOverlayPromotion(_ context.Context, task api.WorkerTask, appliedPaths, deletedPaths []string) (obligation.Plan, error) {
	return obligation.Plan{
		ID:                   h.planID,
		WorkerJobID:          task.ID,
		CanonicalPath:        task.WorkspacePath,
		SessionID:            task.ParentSessionID,
		ChangedPaths:         append([]string(nil), appliedPaths...),
		DeletedPaths:         append([]string(nil), deletedPaths...),
		Required:             true,
		PathScoped:           true,
		ScanID:               "scan-" + h.planID,
		AssessmentID:         "assess-" + h.planID,
		ScannerID:            "tb04-scanner",
		ExecutionManifest:    api.ScanExecutionManifest{DefinitionFingerprint: "tb04-def-fp"},
		ExecutionFingerprint: "tb04-exec-fp",
		FingerprintScheme:    "v1",
	}, nil
}

func (tb04StaticScanHook) PublishObligation(context.Context, obligation.Plan) error { return nil }

type tb04PendingSessionLister struct {
	queue *SQLQueue
	ids   []string
}

func (l tb04PendingSessionLister) ListPendingOverlayPromote(_ context.Context, sessionID string) ([]api.WorkerTask, error) {
	var out []api.WorkerTask
	for _, id := range l.ids {
		task, ok := l.queue.Get(id)
		if !ok || task == nil {
			continue
		}
		if strings.TrimSpace(task.ParentSessionID) != strings.TrimSpace(sessionID) {
			continue
		}
		if task.MergeStatus == api.WorkerMergeStatusPending {
			out = append(out, *task)
		}
	}
	return out, nil
}

func (l tb04PendingSessionLister) ListLiveOverlaysForSession(ctx context.Context, sessionID string) ([]api.WorkerTask, error) {
	return l.ListPendingOverlayPromote(ctx, sessionID)
}

// =============================================================================
// W02 · Changed-path proof, grounding, and baseline enforcement
// =============================================================================

// Slot W02.1: Host changed-path proof equals independent outside diff across create, edit,
// rename, delete, gitignored, and binary files regardless of what the worker reports;
// empty/read-only leg reports no changed paths; absent/unreadable baseline blocks promotion.
func TestWEATB04_W02_ChangedPathProofMatchesOutsideDiffAcrossMutations(t *testing.T) {
	ctx := t.Context()
	initial := map[string][]byte{
		".gitignore":    []byte("ignored.log\n"),
		"keep.go":       []byte("package p\nconst Keep = 1\n"),
		"edit.go":       []byte("package p\nconst Edit = 1\n"),
		"rename_old.go": []byte("package p\nconst Renamed = 1\n"),
		"delete.go":     []byte("package p\nconst Gone = 1\n"),
	}
	f := tb04NewSQLFixture(t, initial)
	baselineMirror := t.TempDir()
	tb04WriteFiles(t, baselineMirror, initial)

	branch, _ := f.seedPendingWorker(
		t, "tb04-w02-proof",
		[]string{"."},
		map[string][]byte{
			"edit.go":       []byte("package p\nconst Edit = 2\n"),
			"added.go":      []byte("package p\nconst Added = 1\n"),
			"ignored.log":   []byte("ignored runtime output\n"),
			"opaque.bin":    {0x89, 0x50, 0x4e, 0x47, 0x00, 0xde, 0xad, 0xbe, 0xef},
			"rename_new.go": []byte("package p\nconst Renamed = 1\n"),
		},
		[]string{"rename_old.go", "delete.go"},
	)

	task, ok := f.svcs.Queue.Get("tb04-w02-proof")
	if !ok {
		t.Fatal("missing tb04-w02-proof")
	}

	// Independent outside diff of branch against starting baseline.
	wantPaths := tb04OutsideDiff(t, baselineMirror, branch)
	expectedSet := []string{"added.go", "delete.go", "edit.go", "ignored.log", "opaque.bin", "rename_new.go", "rename_old.go"}
	if !reflect.DeepEqual(wantPaths, expectedSet) {
		t.Fatalf("outside diff = %v, want %v", wantPaths, expectedSet)
	}

	// Worker's report lies: adds phantom path, omits added.go & ignored.log, renames edit.go.
	lyingDeps := ChangeReportDeps{
		Messages: func(context.Context, string) ([]api.Message, error) {
			return []api.Message{{
				Role: api.MessageRoleTool,
				ToolResult: &api.ToolResult{
					Tool:    "complete_leg",
					Outcome: api.ToolResultOutcomeCompleted,
					ToolArgs: map[string]any{
						"leg_status":    "complete",
						"brief":         "claimed only wrong.go and phantom.go",
						"changed_paths": []string{"phantom.go", "wrong.go"},
					},
				},
			}}, nil
		},
	}

	// Host-observed changed paths must match the independent outside diff verbatim.
	inspected, err := session.InspectOverlayChanges(ctx, task, nil, "")
	testutil.FailErr(t, "InspectOverlayChanges", err)
	sort.Strings(inspected)
	if !reflect.DeepEqual(inspected, wantPaths) {
		t.Fatalf("InspectOverlayChanges = %v, want outside diff %v", inspected, wantPaths)
	}

	report := BuildChangeReport(ctx, *task, lyingDeps)
	gotReport := append([]string(nil), report.ChangedPaths...)
	sort.Strings(gotReport)
	if !report.WorkspaceDirty || !reflect.DeepEqual(gotReport, wantPaths) {
		t.Fatalf("BuildChangeReport = (dirty=%v, paths=%v), want (true, %v)", report.WorkspaceDirty, gotReport, wantPaths)
	}

	// Read-only / empty no-op leg has zero changed paths and is not dirty.
	_, err = f.svcs.Queue.Enqueue(ctx, api.WorkerTask{
		ID: "tb04-w02-readonly", ParentSessionID: f.parent, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: f.primary, AgentType: "repo-researcher", Prompt: "read only", Brief: "read only",
		Scope: &api.TaskScope{Mode: api.TaskScopeModeRead},
	})
	testutil.FailErr(t, "enqueue readonly", err)
	roTask, ok := f.svcs.Queue.Get("tb04-w02-readonly")
	if !ok {
		t.Fatal("missing readonly task")
	}
	roReport := BuildChangeReport(ctx, *roTask, ChangeReportDeps{})
	if roReport.WorkspaceDirty || len(roReport.ChangedPaths) != 0 {
		t.Fatalf("read-only change report = %+v, want clean/empty", roReport)
	}

	// Absent or unreadable baseline blocks automatic promotion and keeps the work open.
	beforeHash := tb04HashTree(t, f.primary)
	missingBaselineTask := *task
	missingBaselineTask.WorkspaceBaselinePath = ""
	if _, err := AssessPromotePaths3Way(ctx, f.merge, f.parent, &missingBaselineTask, []string{"edit.go"}); err == nil {
		t.Fatal("AssessPromotePaths3Way accepted absent baseline")
	}

	corruptBaseline := filepath.Join(t.TempDir(), "corrupt-baseline.db")
	testutil.FailErr(t, "write corrupt baseline", os.WriteFile(corruptBaseline, []byte("not-a-sqlite-manifest"), 0o600))
	unreadableBaselineTask := *task
	unreadableBaselineTask.WorkspaceBaselinePath = corruptBaseline
	if _, err := AssessPromotePaths3Way(ctx, f.merge, f.parent, &unreadableBaselineTask, []string{"edit.go"}); err == nil {
		t.Fatal("AssessPromotePaths3Way accepted unreadable baseline")
	}
	// Work remains pending and primary tree is untouched.
	stillPending, ok := f.svcs.Queue.Get("tb04-w02-proof")
	if !ok || stillPending.MergeStatus != api.WorkerMergeStatusPending {
		t.Fatalf("task status after blocked baseline = %+v, want pending", stillPending)
	}
	if got := tb04HashTree(t, f.primary); got != beforeHash {
		t.Fatalf("primary tree changed while baseline was unreadable: %s != %s", got, beforeHash)
	}
}

// Slot W02.2: Overlay grounding — promoted changes land atomically into worker_results,
// landed_changes, and source_versions with exact SHA-256 parity against disk; out-of-scope
// explicit promotion path is rejected without mutating the primary tree.
func TestWEATB04_W02_OverlayGroundingSourceVersionsSHA256AndScopeRejection(t *testing.T) {
	ctx := t.Context()
	f := tb04NewSQLFixture(t, map[string][]byte{
		"allowed.go": []byte("package p\nconst V = 1\n"),
		"outside.go": []byte("package p\nconst Out = 1\n"),
	})
	ledger := sourceledger.New(f.sqlDB, filepath.Join(f.dataDir, "source-content"), sourceledger.Timing{})
	f.merge.SourceLedger = ledger
	f.merge.Scans = tb04StaticScanHook{planID: "tb04-lc-w02"}

	f.seedPendingWorker(
		t, "tb04-w02-grounding",
		[]string{"allowed.go", "created.go"},
		map[string][]byte{
			"allowed.go": []byte("package p\nconst V = 2\n"),
			"created.go": []byte("package p\nconst Created = true\n"),
		},
		nil,
	)

	// Unlisted and traversal paths outside the overlay must be refused and leave primary untouched.
	beforeHash := tb04HashTree(t, f.primary)
	for _, badPath := range []string{"outside.go", "../escape.go"} {
		if _, err := f.merge.PreviewForSession(ctx, f.parent, "tb04-w02-grounding", "summary", []string{badPath}); err == nil {
			t.Fatalf("PreviewForSession accepted out-of-overlay path %q", badPath)
		}
	}
	if got := tb04HashTree(t, f.primary); got != beforeHash {
		t.Fatalf("primary tree changed on out-of-overlay preview: %s != %s", got, beforeHash)
	}

	// Promote in-scope paths and verify worker_results, landed_changes, and source_versions SHA-256 parity.
	out, err := f.merge.PromoteOverlay(ctx, f.parent, "tb04-w02-grounding", api.PromoteOverlayInput{})
	testutil.FailErr(t, "PromoteOverlay", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("promote status = %q, want merged", out.Status)
	}

	// Verify landed_changes row committed for the worker job.
	var landedID string
	testutil.FailErr(t, "select landed_changes", f.sqlDB.QueryRowContext(
		ctx, `SELECT id FROM landed_changes WHERE worker_job_id = ?`, "tb04-w02-grounding",
	).Scan(&landedID))
	if landedID != "tb04-lc-w02" {
		t.Fatalf("landed_changes.id = %q, want tb04-lc-w02", landedID)
	}

	// Verify worker_results row exists for the job.
	if n := tb04CountWorkerResults(t, f.sqlDB, "tb04-w02-grounding"); n != 1 {
		t.Fatalf("worker_results count = %d, want 1", n)
	}

	// Verify source_versions content_sha256 matches independent disk SHA-256 for every promoted file.
	for _, rel := range []string{"allowed.go", "created.go"} {
		diskBytes, readErr := os.ReadFile(filepath.Join(f.primary, rel))
		testutil.FailErr(t, "read promoted "+rel, readErr)
		sum := sha256.Sum256(diskBytes)
		wantSHA := hex.EncodeToString(sum[:])

		var gotSHA string
		testutil.FailErr(t, "select source_versions "+rel, f.sqlDB.QueryRowContext(
			ctx, `SELECT content_sha256 FROM source_versions WHERE path = ? ORDER BY seq DESC LIMIT 1`, rel,
		).Scan(&gotSHA))
		if gotSHA != wantSHA {
			t.Fatalf("source_versions(%s).content_sha256 = %q, want disk sha256 %q", rel, gotSHA, wantSHA)
		}
	}
}

// =============================================================================
// W03 · Dispositions (landed, rejected, shelved) and typed refusals
// =============================================================================

type tb04DispositionHarness struct {
	*tb04SQLFixture
	rootID    string
	records   *workerrecords.Store
	decisions sessiondecisions.Store
	ops       *workerops.Service
	reg       *tools.DefaultRegistry
	coord     workerrecords.Actor
}

func tb04NewDispositionHarness(t *testing.T, initialFiles map[string][]byte) *tb04DispositionHarness {
	t.Helper()
	f := tb04NewSQLFixture(t, initialFiles)
	var rootID string
	testutil.FailErr(t, "lookup disposition root id", f.sqlDB.QueryRowContext(
		t.Context(), `SELECT id FROM project_roots WHERE project_id = ? AND path = ? LIMIT 1`,
		testdbseed.DefaultProjectID, f.primary,
	).Scan(&rootID))
	records := workerrecords.New(f.sqlDB)
	decisions := sessiondecisions.NewSQL(f.sqlDB)
	ops := &workerops.Service{
		Reader:     workerops.Reader{Records: records, Jobs: f.svcs.Queue, Decisions: decisions},
		Agents:     prompts.AgentMutationCapable,
		WakeParked: func(context.Context, string) error { return nil },
		Release:    f.svcs.Queue.ReleaseAdmitted,
	}
	f.merge.Names = records
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterWorkerTools", RegisterWorkerTools(reg, WorkerToolDeps{
		Ops: ops, Queue: f.svcs.Queue, Plan: plantest.New(t, plantest.Deps(records)),
	}))
	testutil.FailErr(t, "RegisterOverlayTools", RegisterOverlayTools(reg, OverlayToolDeps{
		Merge: f.merge, Results: ops,
	}))
	return &tb04DispositionHarness{
		tb04SQLFixture: f,
		rootID:         rootID,
		records:        records,
		decisions:      decisions,
		ops:            ops,
		reg:            reg,
		coord:          workerrecords.Actor{SessionID: f.parent, Role: workerrecords.RoleCoordinator, PrincipalID: f.parent},
	}
}

func (h *tb04DispositionHarness) createWorkerAndRound(
	t *testing.T,
	workerName, agentType, scopeMode, jobID string,
	scopePaths []string,
	branchEdits map[string][]byte,
) (workerID, childID, resultHandle string) {
	t.Helper()
	ctx := t.Context()
	scope := &api.TaskScope{Mode: api.TaskScopeMode(scopeMode), Paths: scopePaths}
	_, err := h.svcs.Queue.Enqueue(ctx, api.WorkerTask{
		ID: jobID, ParentSessionID: h.parent, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: h.primary, AgentType: agentType, Prompt: "prompt " + jobID, Brief: "brief " + jobID,
		Scope: scope,
	})
	testutil.FailErr(t, "Enqueue "+jobID, err)

	repos := sessionstore.NewSQL(h.sqlDB)
	parent, err := repos.Sessions.Get(ctx, h.parent)
	testutil.FailErr(t, "get parent", err)
	child, err := repos.Sessions.CreateChild(ctx, parent, api.SpawnChildRequest{
		AgentType: agentType, Prompt: "work " + jobID, WorkerJobID: jobID,
	})
	testutil.FailErr(t, "CreateChild "+workerName, err)
	testutil.FailErr(t, "SetChildSessionID "+jobID, h.svcs.Queue.store.SetChildSessionID(ctx, jobID, child.ID))

	w, err := h.records.CreateWorker(ctx, h.coord, workerrecords.Op{ID: "create-" + workerName}, workerrecords.WorkerSpec{
		Name: workerName, AgentType: agentType, ScopeMode: scopeMode, ChildSessionID: child.ID,
	})
	testutil.FailErr(t, "CreateWorker "+workerName, err)

	_, err = h.records.AdmitRound(ctx, h.coord, workerrecords.Op{ID: "admit-" + jobID}, w.WorkerID, workerrecords.RoundSpec{
		JobID: jobID, ExecutionTarget: "local",
	})
	testutil.FailErr(t, "AdmitRound "+jobID, err)

	claimed, err := h.svcs.Queue.ClaimNext(ctx, ClaimRequest{
		ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "tb04-poller", ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "ClaimNext "+jobID, err)

	if scopeMode == string(api.TaskScopeModeWrite) {
		branch := filepath.Join(h.dataDir, "worker-branches", jobID)
		tb04CopyDirFiles(t, h.primary, branch)
		baseline := testbaseline.Durable(t, "root", h.sqlDB, jobID, h.primary)
		testbaseline.RecordBranch(t, "root", h.primary, branch)
		bound, err := h.svcs.Queue.store.SetWorkerWorkspace(ctx, jobID, branch, baseline)
		testutil.FailErr(t, "SetWorkerWorkspace "+jobID, err)
		if !bound {
			t.Fatalf("SetWorkerWorkspace(%s) returned false", jobID)
		}
		tb04WriteFiles(t, branch, branchEdits)
		claimed.WorkspaceRoot = branch
		claimed.WorkspaceBaselinePath = baseline
	}

	if scopeMode == string(api.TaskScopeModeWrite) && len(branchEdits) > 0 {
		comp, err := h.svcs.Queue.store.CompleteJob(
			ctx, jobID, api.WorkerResult{Status: "complete", Summary: "done " + jobID},
			claimed.ClaimToken, api.WorkerMergeStatusPending, false, "", "chk-"+jobID, "",
		)
		testutil.FailErr(t, "CompleteJob "+jobID, err)
		if !comp.Committed {
			t.Fatalf("CompleteJob(%s) did not commit", jobID)
		}
	} else {
		won, err := h.svcs.Queue.Complete(ctx, claimed, api.WorkerResult{Status: "complete", Summary: "done " + jobID})
		testutil.FailErr(t, "Complete "+jobID, err)
		if !won {
			t.Fatalf("Complete(%s) did not commit", jobID)
		}
	}
	return w.WorkerID, child.ID, tb04SingleResultID(t, h.sqlDB, jobID)
}

func (h *tb04DispositionHarness) runTool(t *testing.T, toolName, callID string, args map[string]any) (string, error) {
	t.Helper()
	return h.reg.Run(t.Context(), toolName, args, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: h.parent, ProjectID: testdbseed.DefaultProjectID, ToolCallID: callID},
		Source:   tools.InvocationSource{ActiveRootID: h.rootID, Roots: []projectroot.RootRef{{ID: h.rootID, Path: h.primary, IsPrimary: true}}},
		Effects:  tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	})
}

// Slot W03: Drive results to landed, rejected, and shelved through real coordinator tools
// (promote_overlay, reject_overlay, shelve_overlay, instruct_worker) and reach every typed refusal:
// OVERLAY_PROMOTE_NOT_PENDING, OVERLAY_PROMOTE_NO_PATHS, OVERLAY_REJECT_WORKER_WORKING,
// SHELVE_OVERLAY_NO_CHANGES, RESULT_CHANGES_NOT_ADMITTED, and RESULT_HANDLE_UNKNOWN.
func TestWEATB04_W03_DispositionsLandedRejectedShelvedAndRefusals(t *testing.T) {
	ctx := t.Context()
	h := tb04NewDispositionHarness(t, map[string][]byte{
		"a.go": []byte("package p\nconst A = 1\n"),
		"b.go": []byte("package p\nconst B = 1\n"),
		"c.go": []byte("package p\nconst C = 1\n"),
	})

	// 1. Landed disposition via promote_overlay (addressed by result handle).
	_, _, handleLand := h.createWorkerAndRound(t, "w-land", "implementer", "write", "tb04-w03-land",
		[]string{"a.go"}, map[string][]byte{"a.go": []byte("package p\nconst A = 2\n")})
	_, err := h.runTool(t, "promote_overlay", "call-land", map[string]any{"overlay_id": handleLand})
	testutil.FailErr(t, "promote_overlay landed", err)
	rawA, err := os.ReadFile(filepath.Join(h.primary, "a.go"))
	testutil.FailErr(t, "read landed a.go", err)
	if string(rawA) != "package p\nconst A = 2\n" {
		t.Fatalf("landed a.go = %q", rawA)
	}
	var landState string
	testutil.FailErr(t, "query landed change_state", h.sqlDB.QueryRowContext(
		ctx, `SELECT change_state FROM worker_result_dispositions WHERE result_id = ?`, handleLand,
	).Scan(&landState))
	if landState != "landed" {
		t.Fatalf("landed change_state = %q, want landed", landState)
	}
	// Re-promoting an already-landed result hits RESULT_CHANGES_NOT_ADMITTED (via tool)
	// and OVERLAY_PROMOTE_NOT_PENDING (via MergeService directly).
	_, err = h.runTool(t, "promote_overlay", "call-land-dup", map[string]any{"overlay_id": handleLand})
	tb04RequireRejectCode(t, err, "RESULT_CHANGES_NOT_ADMITTED")
	_, err = h.merge.PromoteOverlay(ctx, h.parent, "tb04-w03-land", api.PromoteOverlayInput{})
	tb04RequireRejectCode(t, err, OverlayPromoteNotPendingCode)

	// 2. Rejected disposition via reject_overlay — primary untouched, child session survives.
	_, childReject, handleReject := h.createWorkerAndRound(t, "w-reject", "implementer", "write", "tb04-w03-reject",
		[]string{"b.go"}, map[string][]byte{"b.go": []byte("package p\nconst B = 99\n")})
	_, err = h.runTool(t, "reject_overlay", "call-reject", map[string]any{"overlay_id": handleReject, "reason": "not needed"})
	testutil.FailErr(t, "reject_overlay", err)
	rawB, err := os.ReadFile(filepath.Join(h.primary, "b.go"))
	testutil.FailErr(t, "read rejected b.go", err)
	if string(rawB) != "package p\nconst B = 1\n" {
		t.Fatalf("rejected b.go mutated primary: %q", rawB)
	}
	if _, err := sessionstore.NewSQL(h.sqlDB).Sessions.Get(ctx, childReject); err != nil {
		t.Fatalf("child session did not survive rejection: %v", err)
	}
	var rejectState string
	testutil.FailErr(t, "query rejected change_state", h.sqlDB.QueryRowContext(
		ctx, `SELECT change_state FROM worker_result_dispositions WHERE result_id = ?`, handleReject,
	).Scan(&rejectState))
	if rejectState != "rejected" {
		t.Fatalf("rejected change_state = %q, want rejected", rejectState)
	}

	// 3. Shelved disposition via shelve_overlay; a later round via instruct_worker ends shelved state.
	_, _, handleShelve := h.createWorkerAndRound(t, "w-shelve", "implementer", "write", "tb04-w03-shelve",
		[]string{"c.go"}, map[string][]byte{"c.go": []byte("package p\nconst C = 3\n")})
	shelveOut, err := h.runTool(t, ShelveOverlayTool, "call-shelve", map[string]any{"overlay_id": handleShelve, "reason": "defer for now"})
	testutil.FailErr(t, "shelve_overlay", err)
	var shelveReceipt map[string]any
	testutil.FailErr(t, "decode shelve receipt", json.Unmarshal([]byte(shelveOut), &shelveReceipt))
	if shelveReceipt["status"] != "shelved" || shelveReceipt["accounted"] != true {
		t.Fatalf("shelve receipt = %s", shelveOut)
	}
	rawC, err := os.ReadFile(filepath.Join(h.primary, "c.go"))
	testutil.FailErr(t, "read shelved c.go", err)
	if string(rawC) != "package p\nconst C = 1\n" {
		t.Fatalf("shelved c.go mutated primary: %q", rawC)
	}
	// 4. OVERLAY_PROMOTE_NO_PATHS (pending overlay with unmatched path filter) &
	//    OVERLAY_PROMOTE_NOT_PENDING / RESULT_CHANGES_NOT_ADMITTED on zero-change write worker.
	_, err = h.merge.PreviewForSession(ctx, h.parent, "tb04-w03-shelve", "summary", []string{"unmatched.go"})
	tb04RequireRejectCode(t, err, OverlayPromoteNoPathsCode)

	_, _, handleNoChanges := h.createWorkerAndRound(t, "w-noop", "implementer", "write", "tb04-w03-noop",
		[]string{"a.go"}, nil)
	_, err = h.runTool(t, ShelveOverlayTool, "call-shelve-noop", map[string]any{"overlay_id": handleNoChanges, "reason": "empty"})
	tb04RequireRejectCode(t, err, "RESULT_CHANGES_NOT_ADMITTED")
	_, err = h.merge.PreviewForSession(ctx, h.parent, "tb04-w03-noop", "summary", nil)
	tb04RequireRejectCode(t, err, OverlayPromoteNotPendingCode)

	// 5. RESULT_CHANGES_NOT_ADMITTED: read-only worker result cannot be promoted or shelved.
	_, _, handleReadOnly := h.createWorkerAndRound(t, "w-read", "repo-researcher", "read", "tb04-w03-read", nil, nil)
	_, err = h.runTool(t, "promote_overlay", "call-promote-ro", map[string]any{"overlay_id": handleReadOnly})
	tb04RequireRejectCode(t, err, "RESULT_CHANGES_NOT_ADMITTED")
	_, err = h.runTool(t, ShelveOverlayTool, "call-shelve-ro", map[string]any{"overlay_id": handleReadOnly, "reason": "nope"})
	tb04RequireRejectCode(t, err, "RESULT_CHANGES_NOT_ADMITTED")

	// 6. Starting a later round on the shelved worker ends the shelved state and transitions it to working;
	// while that later round is active, RejectOverlay on the earlier round hits OVERLAY_REJECT_WORKER_WORKING.
	instructOut, err := h.runTool(t, InstructWorkerTool, "call-shelve-next", map[string]any{
		"worker": "w-shelve", "instructions": "continue with revised approach", "agent_type": "implementer",
	})
	testutil.FailErr(t, "instruct_worker after shelve", err)
	if !strings.Contains(instructOut, `"outcome":"started"`) {
		t.Fatalf("instruct after shelve = %s", instructOut)
	}
	_, err = h.merge.RejectOverlay(ctx, h.parent, "tb04-w03-shelve", "cannot reject while working")
	tb04RequireRejectCode(t, err, OverlayRejectWorkerWorkingCode)

	// 7. RESULT_HANDLE_UNKNOWN: stale/unknown handle rejected across all overlay disposition tools.
	for _, toolName := range []string{"promote_overlay", "reject_overlay", ShelveOverlayTool} {
		_, err = h.runTool(t, toolName, "call-unknown-"+toolName, map[string]any{
			"overlay_id": "result-does-not-exist", "reason": "stale",
		})
		tb04RequireRejectCode(t, err, "RESULT_HANDLE_UNKNOWN")
	}
}

// =============================================================================
// W09 · Verification receipts & multi-worker overlay merge plan
// =============================================================================

// Slot W09.1: Verification receipts name their exact revision and digest, satisfy source
// evidence while current, and stop counting as current once source changes via edit or landing.
func TestWEATB04_W09_VerificationReceiptsInvalidateOnSourceChange(t *testing.T) {
	ctx := t.Context()
	f := tb04NewSQLFixture(t, map[string][]byte{
		"calc.go": []byte("package calc\nfunc Add(a, b int) int { return a + b }\n"),
	})
	branch, _ := f.seedPendingWorker(
		t, "tb04-w09-verify",
		[]string{"calc.go"},
		map[string][]byte{"calc.go": []byte("package calc\nfunc Add(a, b int) int { return a + b + 0 }\n")},
		nil,
	)
	task, ok := f.svcs.Queue.Get("tb04-w09-verify")
	if !ok {
		t.Fatal("missing tb04-w09-verify")
	}
	repos := sessionstore.NewSQL(f.sqlDB)
	parentSess, err := repos.Sessions.Get(ctx, f.parent)
	testutil.FailErr(t, "get parent session", err)
	childSess, err := repos.Sessions.CreateChild(ctx, parentSess, api.SpawnChildRequest{
		AgentType: "implementer", Prompt: "verify", WorkerJobID: task.ID,
	})
	testutil.FailErr(t, "CreateChild verify", err)
	testutil.FailErr(t, "bind child session", f.svcs.Queue.store.SetChildSessionID(ctx, task.ID, childSess.ID))

	// Compute the exact revision & root digest of the worker branch at verification time.
	origRev, origDigest := invocation.SourceRevisionForRoot(branch)
	if origDigest == "" {
		t.Fatal("expected non-empty source root digest for worker branch")
	}

	var currentRev, currentDigest string
	currentRev, currentDigest = origRev, origDigest

	f.merge.Reports = ChangeReportDeps{
		Messages: func(context.Context, string) ([]api.Message, error) {
			return []api.Message{{
				Role: api.MessageRoleTool,
				ToolResult: &api.ToolResult{
					Content: `{"outcome":"passed"}`,
					Invocation: &api.InvocationReceipt{
						ID:               "tb04-receipt-1",
						Tool:             "verify",
						Status:           api.InvocationStatusCompleted,
						SourceRevision:   origRev,
						SourceRootDigest: origDigest,
						SourceVerdict:    api.SourceVerdictPassed,
					},
				},
			}}, nil
		},
	}
	f.merge.Evidence = SourceEvidenceContext{
		Workspaces: workerworkspace.New(nil, nil, nil),
		SourceRevision: func(context.Context, *api.WorkerTask) workercompletion.SourceRevision {
			return workercompletion.SourceRevision{Revision: currentRev, RootDigest: currentDigest}
		},
	}

	// While source matches the receipt's revision & digest, evidence is satisfied.
	out, err := f.merge.PreviewForSession(ctx, f.parent, task.ID, "summary", nil)
	testutil.FailErr(t, "PreviewForSession current receipt", err)
	if out.SourceEvidence == nil || out.SourceEvidence.Status != workercompletion.EvidenceStatusSatisfied {
		t.Fatalf("current receipt evidence = %+v, want satisfied", out.SourceEvidence)
	}
	if out.SourceEvidence.SourceRootDigest != origDigest || out.SourceEvidence.SourceRevision != origRev {
		t.Fatalf("receipt identity = (%q, %q), want (%q, %q)",
			out.SourceEvidence.SourceRevision, out.SourceEvidence.SourceRootDigest, origRev, origDigest)
	}

	// 1. Mutating the branch source advances its revision epoch -> receipt stops counting as current.
	tb04WriteFiles(t, branch, map[string][]byte{
		"calc.go": []byte("package calc\n// edited after verify\nfunc Add(a, b int) int { return a + b + 1 }\n"),
	})
	repochange.Advance(branch)
	newRev, newDigest := invocation.SourceRevisionForRoot(branch)
	if newRev == origRev {
		t.Fatal("expected source revision epoch to advance after edit")
	}
	currentRev, currentDigest = newRev, newDigest

	staleRevOut, err := f.merge.PreviewForSession(ctx, f.parent, task.ID, "summary", nil)
	testutil.FailErr(t, "PreviewForSession stale revision", err)
	if staleRevOut.SourceEvidence == nil || staleRevOut.SourceEvidence.Status != workercompletion.EvidenceStatusUnmet {
		t.Fatalf("stale revision evidence = %+v, want unmet after source edit", staleRevOut.SourceEvidence)
	}

	// 2. Changing the root digest (even with original revision) also invalidates the receipt.
	currentRev, currentDigest = origRev, origDigest+"-changed"
	staleDigestOut, err := f.merge.PreviewForSession(ctx, f.parent, task.ID, "summary", nil)
	testutil.FailErr(t, "PreviewForSession stale digest", err)
	if staleDigestOut.SourceEvidence == nil || staleDigestOut.SourceEvidence.Status != workercompletion.EvidenceStatusUnmet {
		t.Fatalf("stale digest evidence = %+v, want unmet after digest change", staleDigestOut.SourceEvidence)
	}
}

type tb04JobNamesMap map[string]workerrecords.JobNames

func (m tb04JobNamesMap) NamesByJob(_ context.Context, jobID string) (workerrecords.JobNames, bool, error) {
	names, ok := m[jobID]
	return names, ok, nil
}

// Slot W09.2: Multi-worker overlay merge plan (overlayplan.Build / promote_sequence)
// across disjoint and overlapping overlays, ordered promotion, and conflict resolution
// without silent overwrite.
func TestWEATB04_W09_MultiWorkerOverlayMergePlanAndSequencedPromotion(t *testing.T) {
	ctx := t.Context()

	t.Run("disjoint_overlays_follow_promote_sequence", func(t *testing.T) {
		f := tb04NewSQLFixture(t, map[string][]byte{
			"one.go": []byte("package p\nconst One = 1\n"),
			"two.go": []byte("package p\nconst Two = 1\n"),
		})
		f.seedPendingWorker(t, "tb04-plan-1", []string{"one.go"}, map[string][]byte{
			"one.go": []byte("package p\nconst One = 10\n"),
		}, nil)
		f.seedPendingWorker(t, "tb04-plan-2", []string{"two.go"}, map[string][]byte{
			"two.go": []byte("package p\nconst Two = 20\n"),
		}, nil)
		f.merge.Sessions = tb04PendingSessionLister{queue: f.svcs.Queue, ids: []string{"tb04-plan-1", "tb04-plan-2"}}

		t1, _ := f.svcs.Queue.Get("tb04-plan-1")
		t2, _ := f.svcs.Queue.Get("tb04-plan-2")
		p1, err := f.merge.PreviewForSession(ctx, f.parent, "tb04-plan-1", "summary", nil)
		testutil.FailErr(t, "preview plan-1", err)
		p2, err := f.merge.PreviewForSession(ctx, f.parent, "tb04-plan-2", "summary", nil)
		testutil.FailErr(t, "preview plan-2", err)

		previews := map[string]overlayplan.PreviewSnapshot{
			"tb04-plan-1": {PromoteOrder: p1.PromoteOrder, PromoteAfter: p1.PromoteAfter, BlockedBy: p1.BlockedBy, CleanPaths: p1.CleanPaths},
			"tb04-plan-2": {PromoteOrder: p2.PromoteOrder, PromoteAfter: p2.PromoteAfter, BlockedBy: p2.BlockedBy, CleanPaths: p2.CleanPaths},
		}
		handleToJob := map[string]string{"r-plan-1": "tb04-plan-1", "r-plan-2": "tb04-plan-2"}
		latest := map[string]overlayplan.LatestResult{
			"tb04-plan-1": {Worker: "w-one", Handle: "r-plan-1"},
			"tb04-plan-2": {Worker: "w-two", Handle: "r-plan-2"},
		}
		plan := overlayplan.Build(f.parent, []api.WorkerTask{*t1, *t2}, func(_, jobID string) (overlayplan.PreviewSnapshot, bool) {
			snap, ok := previews[jobID]
			return snap, ok
		}, latest)
		if plan.PendingCount != 2 || len(plan.PromoteSequence) != 2 {
			t.Fatalf("disjoint overlay_merge_plan = %+v, want 2 pending in promote_sequence", plan)
		}

		// Follow promote_sequence in order; both overlays land cleanly.
		for _, handle := range plan.PromoteSequence {
			out, err := f.merge.PromoteOverlay(ctx, f.parent, handleToJob[handle], api.PromoteOverlayInput{})
			testutil.FailErr(t, "promote "+handle, err)
			if out.Status != api.WorkerMergeStatusMerged {
				t.Fatalf("promote %s status = %q, want merged", handle, out.Status)
			}
		}
		expected := t.TempDir()
		tb04WriteFiles(t, expected, map[string][]byte{
			"one.go": []byte("package p\nconst One = 10\n"),
			"two.go": []byte("package p\nconst Two = 20\n"),
		})
		if got, want := tb04HashTree(t, f.primary), tb04HashTree(t, expected); got != want {
			t.Fatalf("primary tree after disjoint sequence = %s, want %s", got, want)
		}
	})

	t.Run("overlapping_overlays_order_and_explicit_resolution", func(t *testing.T) {
		f := tb04NewSQLFixture(t, map[string][]byte{
			"shared.go": []byte("package p\nconst Shared = \"base\"\n"),
		})
		f.seedPendingWorker(t, "tb04-ov-1", []string{"shared.go", "a_extra.go"}, map[string][]byte{
			"shared.go":  []byte("package p\nconst Shared = \"from-1\"\n"),
			"a_extra.go": []byte("package p\nconst AExtra = 1\n"),
		}, nil)
		f.seedPendingWorker(t, "tb04-ov-2", []string{"shared.go", "b_extra.go"}, map[string][]byte{
			"shared.go":  []byte("package p\nconst Shared = \"from-2\"\n"),
			"b_extra.go": []byte("package p\nconst BExtra = 2\n"),
		}, nil)
		f.merge.Sessions = tb04PendingSessionLister{queue: f.svcs.Queue, ids: []string{"tb04-ov-1", "tb04-ov-2"}}
		f.merge.Names = tb04JobNamesMap{
			"tb04-ov-1": {Worker: "w-ov-1", Handle: "r-ov-1"},
			"tb04-ov-2": {Worker: "w-ov-2", Handle: "r-ov-2"},
		}

		t1, _ := f.svcs.Queue.Get("tb04-ov-1")
		t2, _ := f.svcs.Queue.Get("tb04-ov-2")
		p1, err := f.merge.PreviewForSession(ctx, f.parent, "tb04-ov-1", "summary", nil)
		testutil.FailErr(t, "preview ov-1", err)
		p2, err := f.merge.PreviewForSession(ctx, f.parent, "tb04-ov-2", "summary", nil)
		testutil.FailErr(t, "preview ov-2", err)
		if p1.PromoteOrder != api.WorkerPromoteOrderCleanIfFirst || len(p1.BlockedBy) != 1 || p1.BlockedBy[0] != "r-ov-2" {
			t.Fatalf("ov-1 preview order = (%q, blocked_by=%v), want clean_if_first blocked_by [r-ov-2]", p1.PromoteOrder, p1.BlockedBy)
		}

		previews := map[string]overlayplan.PreviewSnapshot{
			"tb04-ov-1": {PromoteOrder: p1.PromoteOrder, PromoteAfter: p1.PromoteAfter, BlockedBy: []string{"tb04-ov-2"}, CleanPaths: p1.CleanPaths},
			"tb04-ov-2": {PromoteOrder: p2.PromoteOrder, PromoteAfter: p2.PromoteAfter, BlockedBy: []string{"tb04-ov-1"}, CleanPaths: p2.CleanPaths},
		}
		handleToJob := map[string]string{"r-ov-1": "tb04-ov-1", "r-ov-2": "tb04-ov-2"}
		latest := map[string]overlayplan.LatestResult{
			"tb04-ov-1": {Worker: "w-ov-1", Handle: "r-ov-1"},
			"tb04-ov-2": {Worker: "w-ov-2", Handle: "r-ov-2"},
		}
		plan := overlayplan.Build(f.parent, []api.WorkerTask{*t1, *t2}, func(_, jobID string) (overlayplan.PreviewSnapshot, bool) {
			snap, ok := previews[jobID]
			return snap, ok
		}, latest)
		if plan.PendingCount != 2 || len(plan.PromoteSequence) != 2 {
			t.Fatalf("overlapping overlay_merge_plan = %+v, want 2 pending in promote_sequence", plan)
		}

		// Step 1: First overlay in promote_sequence lands cleanly.
		firstJob := handleToJob[plan.PromoteSequence[0]]
		secondJob := handleToJob[plan.PromoteSequence[1]]
		out1, err := f.merge.PromoteOverlay(ctx, f.parent, firstJob, api.PromoteOverlayInput{})
		testutil.FailErr(t, "promote first in sequence", err)
		if out1.Status != api.WorkerMergeStatusMerged {
			t.Fatalf("first status = %q, want merged", out1.Status)
		}
		afterFirstHash := tb04HashTree(t, f.primary)

		// Step 2: Second overlay conflicts — never silently overwrites first overlay's bytes.
		_, err = f.merge.PromoteOverlay(ctx, f.parent, secondJob, api.PromoteOverlayInput{})
		tb04RequireRejectCode(t, err, OverlayPromoteConflictCode)
		if got := tb04HashTree(t, f.primary); got != afterFirstHash {
			t.Fatalf("conflicting second overlay altered primary tree: %s != %s", got, afterFirstHash)
		}

		// Step 3: Explicit conflict resolution lands second overlay cleanly with combined intent.
		out2, err := f.merge.PromoteOverlay(ctx, f.parent, secondJob, api.PromoteOverlayInput{
			Resolutions: []api.WorkerPromoteResolution{{
				Path:    "shared.go",
				Content: "package p\nconst Shared = \"merged-1-and-2\"\n",
			}},
		})
		testutil.FailErr(t, "promote second with resolution", err)
		if out2.Status != api.WorkerMergeStatusMerged {
			t.Fatalf("second status = %q, want merged", out2.Status)
		}
		expected := t.TempDir()
		tb04WriteFiles(t, expected, map[string][]byte{
			"shared.go":  []byte("package p\nconst Shared = \"merged-1-and-2\"\n"),
			"a_extra.go": []byte("package p\nconst AExtra = 1\n"),
			"b_extra.go": []byte("package p\nconst BExtra = 2\n"),
		})
		if got, want := tb04HashTree(t, f.primary), tb04HashTree(t, expected); got != want {
			t.Fatalf("primary tree after resolved sequence = %s, want %s", got, want)
		}
	})
}
