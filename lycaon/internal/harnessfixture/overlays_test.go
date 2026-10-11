package harnessfixture

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPreparedOverlaysUseRealQueueAndConflict(t *testing.T) {
	database := testdbfixture.Open(t, "fixture.db")
	projects := project.NewSQLRegistry(database)
	root := t.TempDir()
	testutil.FailErr(t, "write baseline", os.WriteFile(filepath.Join(root, "catalog.py"), []byte("value = 'base'\n"), 0o600))
	testutil.FailErr(t, "write preference", os.WriteFile(filepath.Join(root, "preferences.json"), []byte("amber"), 0o600))
	project, err := projects.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: root}}})
	testutil.FailErr(t, "create project", err)
	sessions := store.NewSQL(database)
	parent, err := sessions.Create(t.Context(), api.CreateSessionRequest{WorkspaceRootID: project.Roots[0].ID}, project.ID)
	testutil.FailErr(t, "create parent", err)
	queue := &fixtureQueue{SQLQueue: worker.NewSQLQueue(database, 2)}
	manager := workspace.NewManager(filepath.Join(testbaseline.DataDir(t, database), "worker-branches"), t.TempDir())
	ledger := sourceledger.New(database, filepath.Join(testbaseline.DataDir(t, database), "source-content"))
	queue.SetBaselineStore(ledger.Baselines)
	queue.SetProjectStore(projects)
	queue.SetWorkerWorkspaceManager(manager)
	setup := Setup{Overlays: []Overlay{
		{Label: "Filter", Files: map[string]string{"catalog.py": "value = 'filter'\n"}},
		{Label: "Page", ResultStatus: "partial", Files: map[string]string{"catalog.py": "value = 'page'\n"}},
	}, IntegrationFiles: map[string]string{"preferences.json": "violet"}}
	evidence, err := Prepare(t.Context(), queue, sessions, parent, project.Roots[0], setup, nil)
	testutil.FailErr(t, "prepare overlays", err)
	if queue.pendingOutcomes != 0 {
		t.Fatal("fixture exposed outcomes to the automatic coordinator wake path")
	}
	if len(evidence.Overlays) != 2 || evidence.Overlays[0].BaselineSHA256 != evidence.Overlays[1].BaselineSHA256 {
		t.Fatalf("baseline evidence: %+v", evidence)
	}
	for index, prepared := range evidence.Overlays {
		task, ok := queue.Get(prepared.JobID)
		if !ok || task.Status != api.WorkerStatusComplete || task.MergeStatus != api.WorkerMergeStatusPending || task.ChildSessionID == "" {
			t.Fatalf("fixture job: %+v", task)
		}
		wantStatus := "complete"
		if index == 1 {
			wantStatus = "partial"
		}
		if task.Result == nil || task.Result.Status != wantStatus || task.Result.CompletionReport == nil || task.Result.CompletionReport.LegStatus != wantStatus || prepared.ResultStatus != wantStatus {
			t.Fatalf("worker return did not preserve %s: %+v", wantStatus, task.Result)
		}
		child, err := sessions.Get(t.Context(), task.ChildSessionID)
		testutil.FailErr(t, "get child", err)
		if child.ParentSessionID != parent.ID {
			t.Fatalf("child parent: %s", child.ParentSessionID)
		}
	}
	service := &worker.MergeService{Queue: queue, Store: worker.NewSQLStore(database), Projects: projects, Workspace: manager, DataDir: t.TempDir(), SourceLedger: ledger, SourceHistory: ledger.Walk}
	first := evidence.Overlays[0].JobID
	landed, err := service.PromoteOverlay(t.Context(), parent.ID, first, api.PromoteOverlayInput{})
	testutil.FailErr(t, "promote first overlay", err)
	if landed.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("first promotion: %+v", landed)
	}
	second := evidence.Overlays[1].JobID
	task, _ := queue.Get(second)
	assessment, err := worker.AssessPromotePaths3Way(t.Context(), service, parent.ID, task, []string{"catalog.py"})
	testutil.FailErr(t, "assess second overlay", err)
	if len(assessment.Conflicts) != 1 {
		t.Fatalf("expected actual conflict: %+v", assessment)
	}
	resolved, err := service.PromoteOverlay(t.Context(), parent.ID, second, api.PromoteOverlayInput{
		Resolutions: []api.WorkerPromoteResolution{{Path: "catalog.py", Content: "value = 'filter-then-page'\n"}},
	})
	testutil.FailErr(t, "resolve second overlay", err)
	if resolved.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("second promotion: %+v", resolved)
	}
	body, err := os.ReadFile(filepath.Join(root, "catalog.py"))
	testutil.FailErr(t, "read integration", err)
	if string(body) != "value = 'filter-then-page'\n" {
		t.Fatalf("integrated source: %q", body)
	}
	body, err = os.ReadFile(filepath.Join(root, "preferences.json"))
	testutil.FailErr(t, "read user preference", err)
	if string(body) != "violet" {
		t.Fatalf("user edit lost: %q", body)
	}
	if _, err := Prepare(t.Context(), queue, sessions, parent, project.Roots[0], setup, nil); err == nil {
		t.Fatal("fixture reused an existing project")
	}
}

func TestSetupRejectsUnsafePaths(t *testing.T) {
	for _, path := range []string{"../escape", "/absolute", "a/../escape", ".paintedwolf/settings", ".git/config"} {
		setup := Setup{Overlays: []Overlay{{Label: "a", Files: map[string]string{path: "bad"}}, {Label: "b", Files: map[string]string{"ok": "ok"}}}}
		if err := setup.Validate(); err == nil {
			t.Errorf("accepted unsafe fixture path %q", path)
		}
	}
}

type fixtureQueue struct {
	*worker.SQLQueue
	pendingOutcomes int
}

func (q *fixtureQueue) Complete(ctx context.Context, claimed *api.WorkerTask, result api.WorkerResult) (bool, error) {
	completed, err := q.SQLQueue.Complete(ctx, claimed, result)
	if err != nil {
		return completed, err
	}
	pending, err := q.ListPendingOutcomes(ctx)
	q.pendingOutcomes += len(pending)
	return completed, err
}
