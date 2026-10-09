package episode

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEvidenceUsesApplicationRepositoriesAndSessionIdentity(t *testing.T) {
	capture := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(capture, "store.db"))
	testdbseed.InsertWorkflowRun(t, database, "flow", "root", "project")
	for _, id := range []string{"child", "other"} {
		testdbseed.InsertSession(t, database, id, "project")
	}
	_, err := database.ExecContext(t.Context(), `UPDATE sessions SET parent_session_id='root' WHERE id='child'`)
	testutil.FailErr(t, "bind child", err)
	sessions := store.NewSQL(database)
	testutil.FailErr(t, "append observed progress", sessions.AppendMessages(t.Context(), "root", api.Message{
		ID: "progress", WorkflowRunID: "flow", Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Kind: api.MessageKindProgressComplete,
		ProgressComplete: &api.ProgressCompleteMeta{Steps: []api.ProgressStep{{Label: "Apply mapping", State: "done"}}},
	}))
	tasks := worker.NewSQLStore(database)
	testutil.FailErr(t, "insert completed worker", tasks.InsertTask(t.Context(), api.WorkerTask{
		ID: "job", ProjectID: "project", ParentSessionID: "root", ChildSessionID: "child", AgentType: "implementer",
		Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusMerged, SourceToolCallID: "dispatch",
		Prompt: "Apply mapping", Brief: "Apply mapping", ExecutionTarget: api.ExecutionTargetLocal,
		Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"report.py"}},
	}))
	_, err = database.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	testutil.FailErr(t, "checkpoint capture", err)
	facts, err := Read(t.Context(), capture, "root")
	testutil.FailErr(t, "read typed episode", err)
	if len(facts.Sessions) != 2 || len(facts.Workers) != 1 || facts.Workers[0].SourceToolCallID != "dispatch" {
		t.Fatalf("episode identities: %+v", facts)
	}
	var messages []api.Message
	for _, session := range facts.Sessions {
		if session.ID == "root" {
			messages = session.Messages
		}
	}
	if len(messages) != 1 || messages[0].ProgressComplete == nil || messages[0].ProgressComplete.Steps[0].State != "done" {
		t.Fatalf("progress facts: %+v", messages)
	}
	if facts.Execution != nil || facts.Allowance != nil {
		t.Fatalf("absent execution was invented: %+v", facts)
	}
}

func TestEvidenceRejectsPendingWritesAndMissingSession(t *testing.T) {
	capture := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(capture, "store.db"))
	testdbseed.InsertSession(t, database, "root", "project")
	if _, err := Read(t.Context(), capture, "root"); err == nil {
		t.Fatal("uncheckpointed store accepted")
	}
	_, err := database.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	testutil.FailErr(t, "checkpoint capture", err)
	if _, err := Read(t.Context(), capture, "missing"); err == nil {
		t.Fatal("missing session accepted")
	}
	before, err := os.ReadFile(filepath.Join(capture, "store.db"))
	testutil.FailErr(t, "read capture", err)
	_, err = Read(t.Context(), capture, "root")
	testutil.FailErr(t, "read closed capture", err)
	after, err := os.ReadFile(filepath.Join(capture, "store.db"))
	testutil.FailErr(t, "reread capture", err)
	if string(before) != string(after) {
		t.Fatal("extraction changed application data")
	}
}

func TestWorkflowHistoryIncludesEveryPage(t *testing.T) {
	capture := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(capture, "store.db"))
	testdbseed.InsertSession(t, database, "root", "project")
	store := workflowpersistence.New(database)
	for i := range 103 {
		run := api.WorkflowRun{ID: fmt.Sprintf("run-%03d", i), SessionID: "root", ProjectID: "project", WorkflowID: "release", WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusComplete, CurrentPhase: "done", CompletedAt: new(time.Now().UTC())}
		testutil.FailErr(t, "create completed workflow", store.State.CreateState(t.Context(), &run, "", nil))
	}
	_, err := database.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	testutil.FailErr(t, "checkpoint workflow history", err)
	facts, err := Read(t.Context(), capture, "root")
	testutil.FailErr(t, "read full workflow history", err)
	if len(facts.Sessions) != 1 || len(facts.Sessions[0].Workflows) != 103 {
		t.Fatalf("workflow history: %+v", facts.Sessions)
	}
}
