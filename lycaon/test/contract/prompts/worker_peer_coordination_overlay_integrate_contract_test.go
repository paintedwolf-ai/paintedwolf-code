package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/promotefix"
)

// Coordinator tools that name a worker-branch path reject before HITL.
func TestCoordinatorOverlayPathReadForbiddenContractP3(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hints", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	rejectFmt := guidance.NewStaticRejectFormatter(cfg)

	sess := &api.Session{ID: "parent-1", AgentType: "coordinator"}
	gc := oar.NewGuardContext()
	guard.ObserveCoordinatorWorkerBranchPath(
		sess,
		"read",
		map[string]any{"path": "/home/dev/.config/paintedwolf/worker-branches/ab12cd34/job-timer/shellsim/builtins.py"},
		gc)

	if !observeHasCode(gc, guard.CoordinatorOverlayPathReadForbiddenCode) {
		t.Fatalf("want observation %s got %v", guard.CoordinatorOverlayPathReadForbiddenCode, gc.ArgValidationErrors)
	}
	// Copy parity still comes from the hint registry renderer (OAR block plane in production).
	formatted, err := rejectFmt.Format(guard.CoordinatorOverlayPathReadForbiddenCode, gc.RejectData[guard.CoordinatorOverlayPathReadForbiddenCode])
	contractcheck.FailErr(t, "format overlay path read", err)
	if !strings.Contains(formatted, "Code: "+guard.CoordinatorOverlayPathReadForbiddenCode) {
		t.Fatalf("reject = %v", formatted)
	}
	if !strings.Contains(formatted, "preview_overlay") {
		t.Fatalf("reject should steer to preview_overlay: %v", formatted)
	}
}

func observeHasCode(gc *oar.GuardContext, code string) bool {
	if gc == nil {
		return false
	}
	for _, c := range gc.ArgValidationErrors {
		if c == code {
			return true
		}
	}
	if gc.RejectData != nil {
		if _, ok := gc.RejectData[code]; ok {
			return true
		}
	}
	return guard.EvaluateObserveHasCode(gc, oar.AnchorToolPreInvoke, code) ||
		guard.EvaluateObserveHasCode(gc, oar.AnchorCoordinatorPreInvoke, code)
}

// Invalid resolutions leave clean paths untouched.
func TestPromoteAtomicResolutionContractP4(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hints", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	rejectFmt := guidance.NewStaticRejectFormatter(cfg)

	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, "lycaon", "sandboxes", "deadbeef", "job-shift")
	contractcheck.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	contractcheck.FailErr(t, "WriteFile safe.go", os.WriteFile(filepath.Join(branch, "safe.go"), []byte("safe\n"), 0o644))

	base, primary, branchBody := promotefix.LineShiftBodies()
	contractcheck.FailErr(t, "WriteFile shifted.py branch", os.WriteFile(filepath.Join(branch, "shifted.py"), []byte(branchBody), 0o644))
	contractcheck.FailErr(t, "WriteFile shifted.py primary", os.WriteFile(filepath.Join(dir, "shifted.py"), []byte(primary), 0o644))

	task := &api.WorkerTask{
		ID:              "job-shift",
		ParentSessionID: "parent-1",
		ProjectID:       testdbseed.DefaultProjectID, WorkspacePath: dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"safe.go", "shifted.py"}},
		WorkspaceBaselinePath: testbaseline.FromFiles(t, map[string]testbaseline.File{"safe.go": {}, "shifted.py": {Content: base}}),
		Status:                api.WorkerStatusComplete,
	}
	svc := &worker.MergeService{
		Queue:  contractMergeQueue{task: task},
		Reject: rejectFmt,
	}
	_, mergeErr := svc.PromoteOverlay(ctx, "parent-1", "job-shift", api.PromoteOverlayInput{
		Detail: "hunks",
		Resolutions: []api.WorkerPromoteResolution{{
			Path: "shifted.py",
			Hunks: []api.WorkerPromoteHunkResolution{{
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
	preview, err := svc.PreviewForSession(ctx, "parent-1", "job-shift", "hunks", []string{"shifted.py"})
	contractcheck.FailErr(t, "preview", err)
	if len(preview.Conflicts) == 0 {
		t.Fatal("preview expected conflicts")
	}
}

type contractMergeQueue struct {
	task *api.WorkerTask
}

func (q contractMergeQueue) Get(jobID string) (*api.WorkerTask, bool) {
	if q.task == nil || q.task.ID != jobID {
		return nil, false
	}
	cp := *q.task
	return &cp, true
}

func (q contractMergeQueue) EnsureWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, *worker.BranchLease, error) {
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
