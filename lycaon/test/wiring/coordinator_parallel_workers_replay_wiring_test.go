package wiring

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/promotefix"
	workerpeercoord "github.com/lycaon/lycaon/test/wiring/fixtures/worker_peer_coordination"
)

func TestCoordinatorParallelWorkersReplaySiblingNotes(t *testing.T) {
	fix := workerpeercoord.Load(t)
	h, ctx, _, childA, childB, overlayA := parallelWorkersHarness(t, fix)
	_, err := h.ToolRegistry.Run(ctx, "record_finding", map[string]any{
		"summary": fix.NoteSummary,
		"ref":     fix.NoteRef,
	}, wiringToolContext(childA.ID, overlayA, fix.WorkerA.JobID))
	testutil.FailErr(t, "record_finding", err)

	notes, _, err := h.SessionMgr.RecentSiblingNotes(workercontext.WithJob(ctx, fix.WorkerB.JobID), childB.ID, 0, 5)
	testutil.FailErr(t, "read sibling notes", err)
	if len(notes) != 1 || notes[0].Summary != fix.NoteSummary {
		t.Fatalf("notes=%+v want %q", notes, fix.NoteSummary)
	}
}

func TestCoordinatorParallelWorkersReplayRootSessionKeyParity(t *testing.T) {
	fix := workerpeercoord.Load(t)
	h, ctx, _, childA, _, overlayA := parallelWorkersHarness(t, fix)
	_, err := h.ToolRegistry.Run(ctx, "record_finding", map[string]any{
		"summary": "root session parity",
		"ref":     "pkg/foo.go",
	}, wiringToolContext(childA.ID, overlayA, fix.WorkerA.JobID))
	testutil.FailErr(t, "record_finding", err)

	root := childA.ParentSessionID
	store := findings.NewSQLStore(h.DB)
	recent, _, err := store.Recent(ctx, root, "", 0, 5, time.Time{})
	testutil.FailErr(t, "read root findings", err)
	if len(recent) != 1 {
		t.Fatalf("root session recent = %+v want 1", recent)
	}
	childRecent, _, err := store.Recent(ctx, childA.ID, "", 0, 5, time.Time{})
	testutil.FailErr(t, "read child findings", err)
	if len(childRecent) != 0 {
		t.Fatalf("child id recent = %+v want empty (findings are root-keyed)", childRecent)
	}
	overlayRecent, _, err := store.Recent(ctx, overlayA, "", 0, 5, time.Time{})
	testutil.FailErr(t, "read overlay findings", err)
	if len(overlayRecent) != 0 {
		t.Fatalf("overlay recent = %+v want empty", overlayRecent)
	}
}

func TestCoordinatorParallelWorkersReplayOverlayPathReadForbidden(t *testing.T) {
	fix := workerpeercoord.Load(t)
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hints", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	rejectFmt := guidance.NewStaticRejectFormatter(cfg)

	sess := &wire.Session{ID: "parent-replay", AgentType: "coordinator"}
	gc := oar.NewGuardContext()
	guard.ObserveCoordinatorWorkerBranchPath(
		sess,
		"read",
		map[string]any{"path": fix.OverlayReadPath},
		gc)

	if _, observed := gc.RejectData[guard.CoordinatorOverlayPathReadForbiddenCode]; !observed {
		t.Fatalf("guard did not stamp reject data; facts: worker_branch_path=%v", gc.PathIsWorkerBranch)
	}
	if !gc.PathIsWorkerBranch {
		t.Fatal("path_is_worker_branch fact is unset")
	}
	formatted, err := rejectFmt.Format(guard.CoordinatorOverlayPathReadForbiddenCode, gc.RejectData[guard.CoordinatorOverlayPathReadForbiddenCode])
	testutil.FailErr(t, "format", err)
	if !strings.Contains(formatted, guard.CoordinatorOverlayPathReadForbiddenCode) {
		t.Fatalf("reject = %v", formatted)
	}
}

func TestCoordinatorParallelWorkersReplayLineShiftAtomicRejection(t *testing.T) {
	fix := workerpeercoord.Load(t)
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hints", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	rejectFmt := guidance.NewStaticRejectFormatter(cfg)

	ctx := context.Background()
	dir := t.TempDir()
	jobID := fix.PromoteOverlayID
	branch := filepath.Join(dir, "lycaon", "worker-branches", "deadbeef", jobID)
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile safe.go", os.WriteFile(filepath.Join(branch, "safe.go"), []byte("safe\n"), 0o644))

	base, primary, branchBody := promotefix.LineShiftBodies()
	testutil.FailErr(t, "WriteFile shifted.py branch", os.WriteFile(filepath.Join(branch, "shifted.py"), []byte(branchBody), 0o644))
	testutil.FailErr(t, "WriteFile shifted.py primary", os.WriteFile(filepath.Join(dir, "shifted.py"), []byte(primary), 0o644))

	task := &wire.WorkerTask{
		ID:              jobID,
		ParentSessionID: "parent-replay",
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		WorkspaceRoot:         branch,
		MergeStatus:           wire.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &wire.TaskScope{Mode: wire.TaskScopeModeWrite, Paths: []string{"safe.go", "shifted.py"}},
		WorkspaceBaselinePath: testbaseline.FromFiles(t, map[string]testbaseline.File{"safe.go": {}, "shifted.py": {Content: base}}),
		Status:                wire.WorkerStatusComplete,
	}
	svc := &worker.MergeService{
		Queue:  replayMergeQueue{task: task},
		Reject: rejectFmt,
	}
	_, mergeErr := svc.PromoteOverlay(ctx, "parent-replay", jobID, wire.PromoteOverlayInput{
		Detail: "hunks",
		Resolutions: []wire.WorkerPromoteResolution{{
			Path: "shifted.py",
			Hunks: []wire.WorkerPromoteHunkResolution{{
				StartLine: 1_000_000,
				EndLine:   1_000_000,
				Content:   "invalid\n",
			}},
		}},
	})
	if mergeErr == nil {
		t.Fatal("expected atomic promote to reject invalid resolution")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "safe.go")); !os.IsNotExist(statErr) {
		t.Fatalf("rejected promote changed safe.go: %v", statErr)
	}
	preview, previewErr := svc.PreviewForSession(ctx, "parent-replay", jobID, "hunks", []string{"shifted.py"})
	testutil.FailErr(t, "preview conflict", previewErr)
	if len(preview.Conflicts) == 0 {
		t.Fatal("expected shifted.py conflict to remain pending")
	}
}

func TestCoordinatorParallelWorkersReplayPriorRunFindingsExcluded(t *testing.T) {
	fix := workerpeercoord.Load(t)
	h, ctx, _, childA, childB, overlayA := parallelWorkersHarness(t, fix)
	root := childB.ParentSessionID
	oldTime := time.Now().UTC().Add(-2 * time.Hour)
	_, err := h.DB.ExecContext(t.Context(),
		`INSERT INTO findings (session_id, agent, summary, ref, created_at) VALUES (?, ?, ?, ?, ?)`,
		root, "old-job", fix.PriorRunNoteSummary, "legacy.go", oldTime.Format(time.RFC3339Nano),
	)
	testutil.FailErr(t, "insert prior-run finding", err)

	workerBCtx := workercontext.WithJob(ctx, fix.WorkerB.JobID)
	notes, _, err := h.SessionMgr.RecentSiblingNotes(workerBCtx, childB.ID, 0, 5)
	testutil.FailErr(t, "read prior-run notes", err)
	if len(notes) != 0 {
		t.Fatalf("prior-run notes = %+v want none", notes)
	}

	_, err = h.ToolRegistry.Run(ctx, "record_finding", map[string]any{
		"summary": "fresh batch note",
		"ref":     "pkg/new.go",
	}, wiringToolContext(childA.ID, overlayA, fix.WorkerA.JobID))
	testutil.FailErr(t, "record_finding fresh", err)

	notes2, _, err := h.SessionMgr.RecentSiblingNotes(workerBCtx, childB.ID, 0, 5)
	testutil.FailErr(t, "read fresh notes", err)
	if len(notes2) != 1 || notes2[0].Summary != "fresh batch note" {
		t.Fatalf("post-spawn notes = %+v", notes2)
	}
}

func parallelWorkersHarness(t *testing.T, fix workerpeercoord.ParallelWorkersReplayFixture) (
	*Harness, context.Context, string, *wire.Session, *wire.Session, string,
) {
	t.Helper()
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	parent, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create parent", err)

	spawnAt := time.Now().UTC()
	overlayA := filepath.Join(dir, settingsoverlay.DirName(), "overlays", fix.WorkerA.OverlaySuffix)
	overlayB := filepath.Join(dir, settingsoverlay.DirName(), "overlays", fix.WorkerB.OverlaySuffix)

	taskA := wire.WorkerTask{
		ID: fix.WorkerA.JobID, ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer", Prompt: "leg a", Brief: "leg a", Status: wire.WorkerStatusRunning,
		WorkspaceRoot: overlayA, Scope: &wire.TaskScope{Mode: wire.TaskScopeModeWrite, Paths: []string{"**"}},
		CreatedAt: spawnAt,
	}
	taskB := wire.WorkerTask{
		ID: fix.WorkerB.JobID, ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer", Prompt: "leg b", Brief: "leg b", Status: wire.WorkerStatusRunning,
		WorkspaceRoot: overlayB, Scope: &wire.TaskScope{Mode: wire.TaskScopeModeWrite, Paths: []string{"**"}},
		CreatedAt: spawnAt,
	}
	testutil.FailErr(t, "defaults", worker.ApplyEnqueueDefaults(&taskA, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	testutil.FailErr(t, "defaults", worker.ApplyEnqueueDefaults(&taskB, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	_, err = h.WorkerQueue.Enqueue(ctx, taskA)
	testutil.FailErr(t, "enqueue a", err)
	_, err = h.WorkerQueue.Enqueue(ctx, taskB)
	testutil.FailErr(t, "enqueue b", err)

	childA, err := h.Store.CreateChild(ctx, parent, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "leg a"})
	testutil.FailErr(t, "create child a", err)
	childB, err := h.Store.CreateChild(ctx, parent, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "leg b"})
	testutil.FailErr(t, "create child b", err)
	testutil.FailErr(t, "link a", h.WorkerQueue.SetChildSessionID(ctx, taskA.ID, childA.ID))
	testutil.FailErr(t, "link b", h.WorkerQueue.SetChildSessionID(ctx, taskB.ID, childB.ID))
	seedReplayFindingEvidence(t, h, ctx, childA.ID, fix.NoteRef, "pkg/foo.go", "pkg/new.go")

	return h, ctx, dir, childA, childB, overlayA
}

func seedReplayFindingEvidence(t *testing.T, h *Harness, ctx context.Context, sessionID string, refs ...string) {
	t.Helper()
	for i, ref := range refs {
		path, line, ok := evidence.SplitPathLineToken(ref)
		if !ok {
			path = ref
			line = 0
		}
		ranges := []evidence.LineRange(nil)
		if line > 0 {
			ranges = []evidence.LineRange{{Start: line, End: line}}
		}
		testutil.FailErr(t, "seed finding evidence", h.Store.UpsertEvidenceRecord(ctx, sessionID, evidence.Record{
			Handle: fmt.Sprintf("read#%d", i+1), Kind: "read", Shape: "file_region",
			Fidelity: "structured", SourceTool: "read", Path: path, LineRanges: ranges,
			Body: []string{"fixture observation"},
		}))
	}
}

type replayMergeQueue struct {
	task *wire.WorkerTask
}

func (q replayMergeQueue) Get(jobID string) (*wire.WorkerTask, bool) {
	if q.task == nil || q.task.ID != jobID {
		return nil, false
	}
	cp := *q.task
	return &cp, true
}

func (q replayMergeQueue) EnsureWorkerBranch(ctx context.Context, jobID string) (*wire.WorkerTask, *worker.BranchLease, error) {
	task, ok := q.Get(jobID)
	if !ok {
		return nil, nil, worker.ErrWorkerBranchUnavailable
	}
	lease, err := worker.LeaseExistingBranch(ctx, task)
	if err != nil {
		return nil, nil, err
	}
	return task, lease, nil
}
