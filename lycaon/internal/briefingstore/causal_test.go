package briefingstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const workerRunA = "11111111-1111-4111-8111-111111111111"

type sessionWriter interface {
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	AppendMessages(context.Context, string, ...api.Message) error
}

func TestActionCause(t *testing.T) {
	database := testdbfixture.Open(t, "briefing-cause.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := store.NewSQL(database)
	session, err := sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES (?, ?, ?, 'repo-researcher', 'complete', 'fixture', 'fixture', ?)`, workerRunA, testdbseed.DefaultProjectID, session.ID, time.Now().UTC().Format(time.RFC3339Nano))
	testutil.FailErr(t, "insert worker run", err)
	assertActionCause(t, sessions, NewCausal(database), session.ID)
}

func call(id string) []api.ToolCall { return []api.ToolCall{{ID: id, Name: "command"}} }

func result(id, callID, content string) api.Message {
	return api.Message{ID: id, Role: api.MessageRoleTool, Content: content, ToolResult: &api.ToolResult{ToolCallID: callID, Tool: "command", Outcome: api.ToolResultOutcome("success"), Content: content}}
}

func assertActionCause(t *testing.T, sessions sessionWriter, reader briefing.CausalReader, sessionID string) {
	t.Helper()
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
		api.Message{ID: "anchor", Role: api.MessageRoleAssistant, Content: "Pushing now.", ToolCalls: append([]api.ToolCall{{ID: "target", Name: "command", Args: map[string]any{"command": "git push"}}}, call("other")...), ModelReasoning: &api.ModelReasoning{Text: "push after tests"}},
		api.Message{ID: "worker", Role: api.MessageRoleAssistant, Content: "worker step", ToolCalls: call("worker-call"), WorkerID: workerRunA},
	)
	testutil.FailErr(t, "append history", sessions.AppendMessages(t.Context(), sessionID, history...))

	read, err := reader.ReadActionCause(t.Context(), briefing.ActionRef{SessionID: sessionID, ToolCallID: "target"})
	testutil.FailErr(t, "read pending action", err)
	if !read.Found || read.OutputID != "anchor" || read.Prose != "Pushing now." || read.CallsInOutput != 2 || read.Intent != "second goal" || read.IntentID != "u2" {
		t.Fatalf("anchor read: %+v", read)
	}
	if read.Call.Name != "command" || !strings.Contains(read.Call.Args, "git push") || read.Call.Result != nil {
		t.Fatalf("pending call: %+v", read.Call)
	}
	if len(read.Results) != 3 || read.Results[0].Content != "result c1" || read.Results[2].Content != "result c3" {
		t.Fatalf("results are not the last three after the goal, oldest first: %+v", read.Results)
	}
	if reasoning, state := briefing.ReasoningExcerpt(read); state != briefing.ReasoningAvailable || reasoning.Text != "push after tests" || !reasoning.Shared {
		t.Fatalf("reasoning %s %+v", state, reasoning)
	}

	testutil.FailErr(t, "settle target", sessions.AppendMessages(t.Context(), sessionID, result("r-target", "target", "pushed"), api.Message{ID: "later", Role: api.MessageRoleAssistant, Content: "after", ToolCalls: call("target-again")}))
	settled, err := reader.ReadActionCause(t.Context(), briefing.ActionRef{SessionID: sessionID, ToolCallID: "target"})
	testutil.FailErr(t, "read settled action", err)
	if settled.Call.Result == nil || settled.Call.Result.Content != "pushed" || settled.Call.Result.Ref != "message:r-target" {
		t.Fatalf("settled call result: %+v", settled.Call)
	}
	if settled.OutputID != "anchor" || len(settled.Results) != 3 {
		t.Fatalf("a settled read saw later rows: %+v", settled)
	}

	found := func(ref briefing.ActionRef) bool {
		t.Helper()
		read, err := reader.ReadActionCause(t.Context(), ref)
		testutil.FailErr(t, "read "+ref.ToolCallID, err)
		return read.Found
	}
	if found(briefing.ActionRef{SessionID: sessionID, WorkerJobID: workerRunA, ToolCallID: "target"}) {
		t.Fatal("a worker-scoped read found another scope's output")
	}
	if !found(briefing.ActionRef{SessionID: sessionID, WorkerJobID: workerRunA, ToolCallID: "worker-call"}) {
		t.Fatal("a worker-scoped read missed its own output")
	}
	if found(briefing.ActionRef{SessionID: sessionID, ToolCallID: "unknown"}) {
		t.Fatal("an unknown call was found")
	}

	var filler []api.Message
	for i := range 64 {
		filler = append(filler, api.Message{ID: fmt.Sprintf("f%d", i), Role: api.MessageRoleAssistant, Content: "filler"})
	}
	testutil.FailErr(t, "append filler", sessions.AppendMessages(t.Context(), sessionID, filler...))
	if found(briefing.ActionRef{SessionID: sessionID, ToolCallID: "target-again"}) {
		t.Fatal("the search reached past its 64-row bound")
	}
}
