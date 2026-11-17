package contract

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	reporttools "github.com/lycaon/lycaon/internal/tools/native/reporting"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestRootSessionKeyedFindingsAppendRecentParityContract(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	overlay := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-a")

	store := store.NewMemory()
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	contractcheck.FailErr(t, "create parent", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer", Prompt: "leg"})
	contractcheck.FailErr(t, "create child", err)

	findingsStore := findings.NewMemoryStore()
	scopeKey := func(c context.Context, sessionID string) string {
		return sessiontree.RootID(c, store, sessionID)
	}
	reg := tools.NewDefaultRegistry()
	contractcheck.FailErr(t, "register record_finding", native.RegisterRecordFindingTool(reg, reporttools.RecordFindingGates{}, findingsStore, scopeKey))
	// Findings use the root session key.
	_, err = reg.Run(ctx, "record_finding", map[string]any{"summary": "parity note", "ref": "AGENTS.md:1"},
		toolContext(overlay, child.ID, "job-a"))
	contractcheck.FailErr(t, "record_finding", err)

	recent, _, err := findingsStore.Recent(ctx, parent.ID, "", 0, 5, time.Time{})
	contractcheck.FailErr(t, "read root findings", err)
	if len(recent) != 1 {
		t.Fatalf("recent on root session = %+v want 1", recent)
	}
	childRecent, _, err := findingsStore.Recent(ctx, child.ID, "", 0, 5, time.Time{})
	contractcheck.FailErr(t, "read child findings", err)
	if len(childRecent) != 0 {
		t.Fatalf("recent on child id = %+v want empty (findings are root-keyed)", childRecent)
	}
	overlayRecent, _, err := findingsStore.Recent(ctx, overlay, "", 0, 5, time.Time{})
	contractcheck.FailErr(t, "read overlay findings", err)
	if len(overlayRecent) != 0 {
		t.Fatalf("overlay recent = %+v want empty", overlayRecent)
	}
}

func toolContext(dir, sessionID, workerJobID string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	return tools.ToolContext{
		Source: tools.InvocationSource{Roots: roots,
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: sessionID,
			WorkerJobID: workerJobID},
	}
}

// A worker's child session delegates in the parent's project directory, never in
// the overlay its write task stages into.
func TestWorkerChildDelegationProjectDirInvariantContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	overlay := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-write")
	sqlDB := testdbfixture.Open(t, "child-delegation.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	sessions := store.NewSQL(sqlDB)
	parent, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	contractcheck.FailErr(t, "create parent", err)
	if parent.WorkspacePath != dir {
		t.Fatalf("parent workspace = %q, want the project root %q", parent.WorkspacePath, dir)
	}

	task := api.WorkerTask{
		Prompt: "fixture", Brief: "fixture",
		ID: "job-write", ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: orchestration.ProfileImplementer, Status: api.WorkerStatusPending,
		WorkspaceRoot: overlay, Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"**"}},
	}
	projScope := project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir, HasRoots: true}
	contractcheck.FailErr(t, "enqueue defaults", worker.ApplyEnqueueDefaults(&task, projScope, worker.DefaultWorkersConfig()))
	_, err = worker.NewSQLQueue(sqlDB, 4).Enqueue(ctx, task)
	contractcheck.FailErr(t, "enqueue", err)

	mgr := session.NewHost(sessions, session.Models{Client: llm.NewMockProvider(nil), Limits: settings.DefaultSessionLimits()}, tools.NewStubRegistry())
	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer, Prompt: "write", WorkerJobID: task.ID,
	})
	contractcheck.FailErr(t, "spawn child", err)
	if child.WorkspacePath != dir {
		t.Fatalf("child delegation dir = %q, want the project root %q", child.WorkspacePath, dir)
	}
	if child.WorkspacePath == overlay {
		t.Fatal("child delegation dir is the overlay workspace root")
	}
}
