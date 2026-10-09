package workflow

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

// outboxRow is one queued event, in the order delivery will take it.
type outboxRow struct {
	id       int64
	topic    string
	facet    string
	revision uint64
	data     string
}

func readOutbox(t *testing.T, sqlDB db.Handle) []outboxRow {
	t.Helper()
	rows, err := sqlDB.QueryContext(context.Background(),
		`SELECT id, topic, COALESCE(facet, ''), entity_revision, data_json FROM event_outbox ORDER BY id`)
	testutil.FailErr(t, "read event_outbox", err)
	defer func() { _ = rows.Close() }()
	var out []outboxRow
	for rows.Next() {
		var row outboxRow
		testutil.FailErr(t, "scan event_outbox row",
			rows.Scan(&row.id, &row.topic, &row.facet, &row.revision, &row.data))
		out = append(out, row)
	}
	testutil.FailErr(t, "iterate event_outbox", rows.Err())
	return out
}

// assertRowsSeeRunRevision requires each row's run revision to appear first.
func assertRowsSeeRunRevision(t *testing.T, sqlDB db.Handle, runID string, want uint64) {
	t.Helper()
	var announced uint64
	checked := 0
	for _, row := range readOutbox(t, sqlDB) {
		switch api.EventTopic(row.topic) {
		case api.EventTopicWorkflow:
			if row.facet == runID && row.revision > announced {
				announced = row.revision
			}
		case api.EventTopicMessage:
			var ev api.MessageEvent
			testutil.FailErr(t, "decode message event", json.Unmarshal([]byte(row.data), &ev))
			if ev.Message.WorkflowRunID != runID {
				continue
			}
			checked++
			if announced < want {
				t.Fatalf("outbox row %d is message %q for run %s, but only revision %d was announced before it (want %d)",
					row.id, ev.Message.ID, runID, announced, want)
			}
		default:
		}
	}
	if checked == 0 {
		t.Fatalf("no message events for run %s to check", runID)
	}
}

// assertRunEventsPrecedeTheirRows verifies run-before-row outbox order.
func assertRunEventsPrecedeTheirRows(t *testing.T, sqlDB db.Handle, wantRows int) {
	t.Helper()
	announced := map[string]bool{}
	stamped := 0
	for _, row := range readOutbox(t, sqlDB) {
		switch api.EventTopic(row.topic) {
		case api.EventTopicWorkflow:
			announced[row.facet] = true
		case api.EventTopicMessage:
			var ev api.MessageEvent
			testutil.FailErr(t, "decode message event", json.Unmarshal([]byte(row.data), &ev))
			runID := ev.Message.WorkflowRunID
			if runID == "" {
				continue
			}
			stamped++
			if announced[runID] {
				continue
			}
			t.Fatalf("outbox row %d is message %q naming run %s, which no earlier workflow event announced",
				row.id, ev.Message.ID, runID)
		default:
		}
	}
	if stamped != wantRows {
		t.Fatalf("run-stamped message events = %d, want %d", stamped, wantRows)
	}
}

type orderHarness struct {
	runs    *runstate.Repository
	db      db.Handle
	session *api.Session
}

func newOrderHarness(t *testing.T) orderHarness {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "run-event-order.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())

	// Left undrained: the queued rows are the assertion.
	outbox := eventoutbox.New(sqlDB, events.NewMemoryHub())
	sessions := store.NewSQL(sqlDB)
	sessions.SetEventOutbox(outbox)
	runs := workflowpersistence.New(sqlDB)
	runs.Transactions.SetEventOutbox(outbox)
	runs.Transactions.SetSessionMutations(sessions)

	sess, err := sessions.Create(context.Background(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	return orderHarness{runs: runs, db: sqlDB, session: sess}
}

func (h orderHarness) run(id string) *api.WorkflowRun {
	return &api.WorkflowRun{
		ID: id, SessionID: h.session.ID, ProjectID: h.session.ProjectID,
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "expand",
		Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func (h orderHarness) row(runID, content string) api.Message {
	return api.Message{
		ID: uuid.NewString(), Role: api.MessageRoleUser, Content: content,
		WorkflowRunID: runID, CreatedAt: time.Now().UTC(),
	}
}

func TestActivateStartAnnouncesTheRunBeforeItsRows(t *testing.T) {
	h := newOrderHarness(t)
	run := h.run("run-start")

	_, err := h.runs.Starts.ActivateStart(context.Background(), uuid.NewString(), "digest", nil, run,
		runstate.StartMutation{Messages: []api.Message{h.row(run.ID, "/plan go")}})
	testutil.FailErr(t, "ActivateStart", err)

	assertRunEventsPrecedeTheirRows(t, h.db, 1)
}

func TestSupersedingStartAnnouncesEveryRunBeforeItsRows(t *testing.T) {
	h := newOrderHarness(t)
	ctx := context.Background()

	first := h.run("run-first")
	_, err := h.runs.Starts.ActivateStart(ctx, uuid.NewString(), "digest-1", nil, first,
		runstate.StartMutation{Messages: []api.Message{h.row(first.ID, "/plan one")}})
	testutil.FailErr(t, "ActivateStart first", err)

	// Replacing cancels the first run and writes a boundary row naming it.
	second := h.run("run-second")
	replaced, err := h.runs.Starts.ActivateStart(ctx, uuid.NewString(), "digest-2", first, second,
		runstate.StartMutation{Messages: []api.Message{h.row(second.ID, "/plan two")}})
	testutil.FailErr(t, "ActivateStart replacement", err)
	if len(replaced) != 1 {
		t.Fatalf("replaced runs = %d, want 1", len(replaced))
	}

	// Two prompts, plus the superseded run's boundary row.
	assertRunEventsPrecedeTheirRows(t, h.db, 3)
}

func TestCommitCommandAnnouncesTheRunBeforeItsRows(t *testing.T) {
	h := newOrderHarness(t)
	ctx := context.Background()

	run := h.run("run-command")
	_, err := h.runs.Starts.ActivateStart(ctx, uuid.NewString(), "digest", nil, run, runstate.StartMutation{})
	testutil.FailErr(t, "ActivateStart", err)

	next := *run
	next.CurrentPhase = "build"
	testutil.FailErr(t, "CommitCommand", h.runs.Commands.CommitCommand(ctx, &next, runstate.CommandMutation{
		OperationID: uuid.NewString(), Kind: runstate.AdvanceCommandKind, InputDigest: "digest",
		Messages: []api.Message{h.row(run.ID, "phase advanced")},
	}))

	// The advance takes the run to revision 2; its boundary row names that phase.
	assertRunEventsPrecedeTheirRows(t, h.db, 1)
	assertRowsSeeRunRevision(t, h.db, run.ID, 2)
}

func TestStartChildAnnouncesBothRunsBeforeItsRows(t *testing.T) {
	h := newOrderHarness(t)
	ctx := context.Background()

	parent := h.run("run-parent")
	_, err := h.runs.Starts.ActivateStart(ctx, uuid.NewString(), "digest", nil, parent, runstate.StartMutation{})
	testutil.FailErr(t, "ActivateStart parent", err)

	child := h.run("run-child")
	child.ParentRunID = &parent.ID
	testutil.FailErr(t, "StartChild", h.runs.Starts.StartChild(ctx, parent, child, runstate.ChildStartMutation{
		Messages: []api.Message{h.row(child.ID, "child started")},
	}))

	assertRunEventsPrecedeTheirRows(t, h.db, 1)
}

func TestCancelActiveTreeAnnouncesCancellationBeforeItsBoundaries(t *testing.T) {
	h := newOrderHarness(t)
	ctx := context.Background()

	run := h.run("run-cancel")
	_, err := h.runs.Starts.ActivateStart(ctx, uuid.NewString(), "digest", nil, run, runstate.StartMutation{})
	testutil.FailErr(t, "ActivateStart", err)

	canceled, err := h.runs.Commands.CancelActiveTree(ctx, run, "user exited", "", nil)
	testutil.FailErr(t, "CancelActiveTree", err)
	if len(canceled) != 1 {
		t.Fatalf("canceled runs = %d, want 1", len(canceled))
	}

	// The exit boundary is the only stamped row, and names revision 2.
	assertRunEventsPrecedeTheirRows(t, h.db, 1)
	assertRowsSeeRunRevision(t, h.db, run.ID, 2)
}
