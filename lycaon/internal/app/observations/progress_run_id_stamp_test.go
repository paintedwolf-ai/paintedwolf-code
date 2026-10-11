package observations

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func newStampStore(t *testing.T) (*store.SQL, string, db.Handle, progress.RunScopedStore) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "stamp.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	st := store.NewSQL(sqlDB)
	sess, err := st.Create(context.Background(), wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "store.Create failed", err)
	return st, sess.ID, sqlDB, progress.NewSQLStore(sqlDB)
}

func seedWorkflowRun(t *testing.T, sqlDB db.Handle, sessionID, runID string, parentRunID ...string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var parent any
	if len(parentRunID) > 0 {
		parent = parentRunID[0]
	}
	_, err := sqlDB.ExecContext(context.Background(), `
		INSERT INTO workflow_runs (id, session_id, project_id, parent_run_id, workflow_id, workflow_version, status, current_phase, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'plan', '1.0.0', 'running', 'intake', ?, ?)
	`, runID, sessionID, testdbseed.DefaultProjectID, parent, now, now)
	testutil.FailErr(t, "seed workflow_runs row", err)
}

func fixedActiveRun(runID string) ActiveRunID {
	return func(context.Context, string) string { return runID }
}

func findMessageByKind(t *testing.T, msgs []wire.Message, kind wire.MessageKind) wire.Message {
	t.Helper()
	for _, m := range msgs {
		if m.Kind == kind {
			return m
		}
	}
	t.Fatalf("no message of kind %q in %d rows", kind, len(msgs))
	return wire.Message{}
}

func TestProgressUpdateStampedWithActiveRun(t *testing.T) {
	st, sessionID, sqlDB, prog := newStampStore(t)
	const runID = "run-update-1"
	seedWorkflowRun(t, sqlDB, sessionID, runID)

	flush := newProgressChangeEmitter(st, &events.Publisher{}, fixedActiveRun(runID), prog)
	flush(progress.FlushPayload{
		SessionID: sessionID,
		Baseline:  "",
		Latest:    "- [ ] a\n- [ ] b\n",
		Seq:       1,
		ChangedAt: time.Now().UTC(),
	})

	msgs, err := st.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "GetMessages failed", err)
	row := findMessageByKind(t, msgs, wire.MessageKindProgressUpdate)
	if row.WorkflowRunID != runID {
		t.Fatalf("progress_update workflow_run_id = %q want %q", row.WorkflowRunID, runID)
	}
	if prog.BoundRunID(sessionID) != runID {
		t.Fatalf("bound run = %q want %q", prog.BoundRunID(sessionID), runID)
	}
}

func TestProgressCompleteStampedWithActiveRun(t *testing.T) {
	st, sessionID, sqlDB, prog := newStampStore(t)
	const runID = "run-complete-1"
	seedWorkflowRun(t, sqlDB, sessionID, runID)
	prog.Set(sessionID, "- [x] a\n- [x] b\n")

	emitProgressCompletion(context.Background(), st, &events.Publisher{}, prog, fixedActiveRun(runID), sessionID)

	msgs, err := st.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "GetMessages failed", err)
	row := findMessageByKind(t, msgs, wire.MessageKindProgressComplete)
	if row.WorkflowRunID != runID {
		t.Fatalf("progress_complete workflow_run_id = %q want %q", row.WorkflowRunID, runID)
	}
}

// Parent bind + child active: stamp is the child; emit leaves bind unchanged.
func TestProgressStampIgnoresStaleBoundRunID(t *testing.T) {
	st, sessionID, sqlDB, prog := newStampStore(t)
	const parentRun = "run-parent"
	const childRun = "run-child"
	seedWorkflowRun(t, sqlDB, sessionID, parentRun)
	_, err := sqlDB.ExecContext(context.Background(), `UPDATE workflow_runs SET status = 'paused_on_child' WHERE id = ?`, parentRun)
	testutil.FailErr(t, "pause parent workflow", err)
	seedWorkflowRun(t, sqlDB, sessionID, childRun, parentRun)
	prog.BindRun(sessionID, parentRun)

	flush := newProgressChangeEmitter(st, &events.Publisher{}, fixedActiveRun(childRun), prog)
	flush(progress.FlushPayload{
		SessionID: sessionID,
		Baseline:  "",
		Latest:    "- [ ] a\n",
		Seq:       1,
		ChangedAt: time.Now().UTC(),
	})

	msgs, err := st.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "GetMessages failed", err)
	row := findMessageByKind(t, msgs, wire.MessageKindProgressUpdate)
	if row.WorkflowRunID != childRun {
		t.Fatalf("progress_update workflow_run_id = %q want %q", row.WorkflowRunID, childRun)
	}
	if prog.BoundRunID(sessionID) != parentRun {
		t.Fatalf("bound run = %q want %q", prog.BoundRunID(sessionID), parentRun)
	}
}

func TestProgressRowsSkippedWhenNoActiveRun(t *testing.T) {
	st, sessionID, _, prog := newStampStore(t)

	flush := newProgressChangeEmitter(st, &events.Publisher{}, fixedActiveRun(""), prog)
	flush(progress.FlushPayload{
		SessionID: sessionID,
		Baseline:  "",
		Latest:    "- [ ] a\n",
		Seq:       1,
		ChangedAt: time.Now().UTC(),
	})
	prog.Set(sessionID, "- [x] a\n")
	emitProgressCompletion(context.Background(), st, &events.Publisher{}, prog, fixedActiveRun(""), sessionID)

	msgs, err := st.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "GetMessages failed", err)
	for _, m := range msgs {
		if m.Kind == wire.MessageKindProgressUpdate || m.Kind == wire.MessageKindProgressComplete {
			t.Fatalf("unexpected %s row", m.Kind)
		}
	}
}

func TestStoreRejectsUnstampedProgressUpdate(t *testing.T) {
	st, sessionID, _, _ := newStampStore(t)
	err := st.AppendMessages(context.Background(), sessionID, wire.Message{
		Role:           wire.MessageRoleSystem,
		Kind:           wire.MessageKindProgressUpdate,
		Visibility:     wire.MessageVisibilityTranscript,
		ProgressUpdate: &wire.ProgressUpdateMeta{Seq: 1},
	})
	if err == nil {
		t.Fatal("unstamped progress_update must fail at store")
	}
}
