package contract

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/store"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
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

func TestWorkerChildDelegationProjectDirInvariantContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	overlay := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-write")

	store := store.NewMemory()
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	contractcheck.FailErr(t, "create parent", err)

	q := worker.NewInMemoryQueue(4)
	task := api.WorkerTask{
		Prompt: "fixture", Brief: "fixture",
		ID: "job-write", ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer", Status: api.WorkerStatusRunning,
		WorkspaceRoot: overlay, Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"**"}},
	}
	projScope := project.ProjectScope{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir, HasRoots: true,
	}
	contractcheck.FailErr(t, "enqueue defaults", worker.ApplyEnqueueDefaults(&task, projScope, worker.DefaultWorkersConfig()))
	_, err = q.Enqueue(ctx, task)
	contractcheck.FailErr(t, "enqueue", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer", Prompt: "write"})
	contractcheck.FailErr(t, "create child", err)

	if child.WorkspacePath != parent.WorkspacePath {
		t.Fatalf("child delegation dir = %q parent = %q", child.WorkspacePath, parent.WorkspacePath)
	}
	if child.WorkspacePath == overlay {
		t.Fatal("child ProjectDir must not equal overlay WorkspaceRoot")
	}
}
