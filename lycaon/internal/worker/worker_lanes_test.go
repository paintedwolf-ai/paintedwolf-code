package worker

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// A write worker's rounds run in one lane: what a round left there, landed
// or not, is still there when the worker is sent back, and a job that runs no
// worker round keeps its own branch.
func TestWorkerRoundsShareTheirLaneThroughTheClaimPath(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, sqlDB, testdbseed.DefaultProjectID, "root-id", dir)
	testdbseed.InsertSession(t, sqlDB, "coord", testdbseed.DefaultProjectID)
	now := db.FormatTime(time.Now().UTC())
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at)
		VALUES ('child', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', 'implementer', ?, ?, ?)`, testdbseed.DefaultProjectID, now, now, now)
	testutil.FailErr(t, "insert child", err)

	q := NewSQLQueue(sqlDB, 4)
	q.SetBaselineStore(sourceledger.New(sqlDB, filepath.Join(testbaseline.DataDir(t, sqlDB), "source-content")).Baselines)
	manager := workspace.NewManager(filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches"))
	q.SetWorkerWorkspaceManager(manager)
	records := workerrecords.New(sqlDB)
	q.SetWorkerLanes(&WorkerLanes{Lanes: workerlane.New(manager, records), Records: records, State: workerlane.NewM14State(q)})

	coord := workerrecords.Actor{SessionID: "coord", Role: workerrecords.RoleCoordinator, PrincipalID: "coord"}
	w, err := records.CreateWorker(t.Context(), coord, workerrecords.Op{ID: "create"}, workerrecords.WorkerSpec{Name: "fixer", AgentType: "implementer", ScopeMode: "write", ChildSessionID: "child"})
	testutil.FailErr(t, "create worker", err)

	enqueue := func(id string) {
		t.Helper()
		_, err := q.Enqueue(t.Context(), api.WorkerTask{
			ID: id, ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "coord", ChildSessionID: "child",
			WorkspacePath: dir, WorkspaceRootID: "root-id", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite},
			AgentType: "implementer", Prompt: "fix main.go", Brief: "fix main.go",
		})
		testutil.FailErr(t, "enqueue "+id, err)
	}
	claim := func(id string) *api.WorkerTask {
		t.Helper()
		claimed, err := q.ClaimNext(t.Context(), ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
		testutil.FailErr(t, "claim "+id, err)
		if claimed == nil || claimed.ID != id {
			t.Fatalf("claimed %+v, want %s", claimed, id)
		}
		branch, err := q.ClaimWorkerBranch(t.Context(), id)
		testutil.FailErr(t, "branch "+id, err)
		branch.ClaimToken = claimed.ClaimToken
		return branch
	}

	enqueue("job-1")
	_, err = records.AdmitRound(t.Context(), coord, workerrecords.Op{ID: "admit-1"}, w.WorkerID, workerrecords.RoundSpec{JobID: "job-1", ExecutionTarget: "local"})
	testutil.FailErr(t, "admit round 1", err)
	first := claim("job-1")
	testutil.FailErr(t, "leave notes", os.WriteFile(filepath.Join(first.WorkspaceRoot, "notes.txt"), []byte("found the encoder\n"), 0o644))
	won, err := q.Fail(t.Context(), first, os.ErrDeadlineExceeded)
	if err != nil || !won {
		t.Fatalf("fail round 1: won=%v err=%v", won, err)
	}
	if _, err := os.Stat(filepath.Join(first.WorkspaceRoot, "notes.txt")); err != nil {
		t.Fatalf("a failed round destroyed its worker's lane: %v", err)
	}

	enqueue("job-2")
	_, err = records.StartRound(t.Context(), coord, workerrecords.Op{ID: "start-2"}, w.WorkerID, workerrecords.RoundStart{JobID: "job-2", ExecutionTarget: "local", Instruction: "finish the fix"})
	testutil.FailErr(t, "start round 2", err)
	second := claim("job-2")
	if second.WorkspaceRoot != first.WorkspaceRoot {
		t.Fatalf("round 2 moved the lane: %s -> %s", first.WorkspaceRoot, second.WorkspaceRoot)
	}
	if _, err := os.Stat(filepath.Join(second.WorkspaceRoot, "notes.txt")); err != nil {
		t.Fatalf("round 2 lost what round 1 left: %v", err)
	}

	enqueue("job-solo")
	solo := claim("job-solo")
	if solo.WorkspaceRoot == first.WorkspaceRoot {
		t.Fatal("a job outside any worker round took the worker's lane")
	}
}
