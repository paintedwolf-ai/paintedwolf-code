package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type causeStore interface {
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	AppendMessages(context.Context, string, ...api.Message) error
	ReadActionCause(context.Context, briefing.ActionRef) (briefing.CausalRead, error)
}

func TestMemoryActionCause(t *testing.T) {
	assertActionCause(t, NewMemory(), func(*testing.T, string) {})
}

func TestSQLActionCause(t *testing.T) {
	database := testdbfixture.Open(t, "briefing-cause.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	assertActionCause(t, NewSQL(database), func(t *testing.T, sessionID string) {
		_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, child_session_id, agent_type, status, prompt, brief, created_at)
			VALUES (?, ?, ?, 'repo-researcher', 'complete', 'fixture', 'fixture', ?)`, workerRunA, testdbseed.DefaultProjectID, sessionID, time.Now().UTC().Format(time.RFC3339Nano))
		testutil.FailErr(t, "insert worker run", err)
	})
}

func call(id string) []api.ToolCall { return []api.ToolCall{{ID: id, Name: "command"}} }

func result(id, callID, content string) api.Message {
	return api.Message{ID: id, Role: api.MessageRoleTool, Content: content, ToolResult: &api.ToolResult{ToolCallID: callID, Tool: "command", Outcome: api.ToolResultOutcome("success"), Content: content}}
}

func assertActionCause(t *testing.T, store causeStore, seedWorker func(*testing.T, string)) {
	t.Helper()
	session, err := store.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	seedWorker(t, session.ID)
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "first goal"},
		{ID: "a1", Role: api.MessageRoleAssistant, Content: "early", ToolCalls: call("early")},
		result("r0", "early", "before the goal"),
		{ID: "u2", Role: api.MessageRoleUser, Content: "second goal"},
	}
	for i := range 4 {
		id := fmt.Sprintf("c%d", i)
		history = append(history, api.Message{ID: "a-" + id, Role: api.MessageRoleAssistant, Content: "step", ToolCalls: call(id)}, result("r-"+id, id, "result "+id))
	}
	history = append(history,
		api.Message{ID: "anchor", Role: api.MessageRoleAssistant, Content: "Pushing now.", ToolCalls: append(call("target"), call("other")...), ModelReasoning: &api.ModelReasoning{Text: "push after tests"}},
		api.Message{ID: "worker", Role: api.MessageRoleAssistant, Content: "worker step", ToolCalls: call("worker-call"), WorkerID: workerRunA},
	)
	testutil.FailErr(t, "append history", store.AppendMessages(t.Context(), session.ID, history...))

	read, err := store.ReadActionCause(t.Context(), briefing.ActionRef{SessionID: session.ID, ToolCallID: "target"})
	testutil.FailErr(t, "read pending action", err)
	if !read.Found || read.OutputID != "anchor" || read.Prose != "Pushing now." || read.CallsInOutput != 2 || read.Intent != "second goal" || read.IntentID != "u2" {
		t.Fatalf("anchor read: %+v", read)
	}
	if len(read.Results) != 3 || read.Results[0].Content != "result c1" || read.Results[2].Content != "result c3" {
		t.Fatalf("results are not the last three after the goal, oldest first: %+v", read.Results)
	}
	if reasoning, state := briefing.ReasoningExcerpt(read); state != briefing.ReasoningAvailable || reasoning.Text != "push after tests" || !reasoning.Shared {
		t.Fatalf("reasoning %s %+v", state, reasoning)
	}

	testutil.FailErr(t, "settle target", store.AppendMessages(t.Context(), session.ID, result("r-target", "target", "pushed"), api.Message{ID: "later", Role: api.MessageRoleAssistant, Content: "after", ToolCalls: call("target-again")}))
	settled, err := store.ReadActionCause(t.Context(), briefing.ActionRef{SessionID: session.ID, ToolCallID: "target"})
	testutil.FailErr(t, "read settled action", err)
	if settled.OutputID != "anchor" || len(settled.Results) != 3 {
		t.Fatalf("a settled read saw later rows: %+v", settled)
	}

	if worker, _ := store.ReadActionCause(t.Context(), briefing.ActionRef{SessionID: session.ID, WorkerJobID: workerRunA, ToolCallID: "target"}); worker.Found {
		t.Fatal("a worker-scoped read found another scope's output")
	}
	if worker, _ := store.ReadActionCause(t.Context(), briefing.ActionRef{SessionID: session.ID, WorkerJobID: workerRunA, ToolCallID: "worker-call"}); !worker.Found {
		t.Fatal("a worker-scoped read missed its own output")
	}
	if missing, _ := store.ReadActionCause(t.Context(), briefing.ActionRef{SessionID: session.ID, ToolCallID: "unknown"}); missing.Found {
		t.Fatal("an unknown call was found")
	}

	var filler []api.Message
	for i := range 64 {
		filler = append(filler, api.Message{ID: fmt.Sprintf("f%d", i), Role: api.MessageRoleAssistant, Content: "filler"})
	}
	testutil.FailErr(t, "append filler", store.AppendMessages(t.Context(), session.ID, filler...))
	if far, _ := store.ReadActionCause(t.Context(), briefing.ActionRef{SessionID: session.ID, ToolCallID: "target-again"}); far.Found {
		t.Fatal("the search reached past its 64-row bound")
	}
}
