package inject

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// canonicalRows serves an in-memory transcript and counts what it was asked for.
type canonicalRows struct {
	rows       []api.Message
	reads, got int
}

func newCanonicalRows(rows []api.Message) *canonicalRows {
	out := &canonicalRows{}
	for i, row := range rows {
		row.Ord = int64(i + 1)
		out.rows = append(out.rows, row)
	}
	return out
}

func (c *canonicalRows) read(_ context.Context, _ string, window messageview.MessageWindow) ([]api.Message, error) {
	c.reads++
	end := len(c.rows)
	if window.BeforeOrd > 0 {
		end = int(window.BeforeOrd) - 1
	}
	page := c.rows[max(0, end-window.Limit):end]
	c.got += len(page)
	return page, nil
}

func TestWindowWorkStaysBoundedOverLongTurns_ACEXEC04(t *testing.T) {
	user := api.Message{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "go"}
	for _, length := range []int{10, 500, 5000} {
		t.Run(fmt.Sprintf("%d batches", length), func(t *testing.T) {
			rows := append([]api.Message{user, rejectedWrite("old", "old.go")}, settledBatches("later", length)...)
			canonical := newCanonicalRows(rows)
			window, err := ReadExecutionStateWindow(t.Context(), canonical.read, "sess")
			testutil.FailErr(t, "read window", err)
			if canonical.reads != 1 || canonical.got > executionStatePageRows {
				t.Fatalf("reads=%d rows=%d", canonical.reads, canonical.got)
			}
			if got := projectExecutionState(window, ""); len(got.Operations) != 0 {
				t.Fatalf("old reminder returned: %+v", got.Operations)
			}
		})
	}
}

func TestWindowExtendsToCompleteLiveBatches_ACEXEC04(t *testing.T) {
	var rows []api.Message
	rows = append(rows, api.Message{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "go"}, rejectedWrite("failed", "a.go"))
	for i := range 3 {
		for j := range executionStatePageRows {
			rows = append(rows, receiptMessage(receiptSpec{batch: fmt.Sprintf("wide-%d", i), call: fmt.Sprintf("wide-%d-%d", i, j), tool: "read", digest: "d"}))
		}
	}
	window, err := ReadExecutionStateWindow(t.Context(), newCanonicalRows(rows).read, "sess")
	testutil.FailErr(t, "read window", err)
	got := projectExecutionState(window, "")
	if !sameCalls(got.Operations, "failed") || got.Operations[0].Age != 3 {
		t.Fatalf("wide batches lost the reminder: %+v", got.Operations)
	}
}

func TestWindowStopsAtIntentAndCap_ACEXEC04(t *testing.T) {
	prior := rejectedWrite("prior", "prior.go")
	user := api.Message{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "next"}
	canonical := newCanonicalRows([]api.Message{prior, user, rejectedWrite("current", "a.go")})
	window, err := ReadExecutionStateWindow(t.Context(), canonical.read, "sess")
	testutil.FailErr(t, "read intent", err)
	if got := projectExecutionState(window, ""); !sameCalls(got.Operations, "current") {
		t.Fatalf("prior intent leaked: %+v", got.Operations)
	}

	polls := []api.Message{rejectedWrite("buried", "b.go")}
	for i := range 2 * executionStateMaxRows {
		polls = append(polls, receiptMessage(receiptSpec{batch: fmt.Sprintf("p%d", i), call: fmt.Sprintf("p%d", i), tool: "command_output", digest: "p"}))
	}
	canonical = newCanonicalRows(polls)
	window, err = ReadExecutionStateWindow(t.Context(), canonical.read, "sess")
	testutil.FailErr(t, "read polls", err)
	if canonical.got > executionStateMaxRows || len(window) != executionStateMaxRows {
		t.Fatalf("polling escaped the row cap: read %d", canonical.got)
	}

	empty := newCanonicalRows(nil)
	if window, err = ReadExecutionStateWindow(t.Context(), empty.read, "sess"); err != nil || len(window) != 0 {
		t.Fatalf("empty transcript: %v %v", window, err)
	}
	broken := errors.New("store offline")
	_, err = ReadExecutionStateWindow(t.Context(), func(context.Context, string, messageview.MessageWindow) ([]api.Message, error) { return nil, broken }, "sess")
	if !errors.Is(err, broken) {
		t.Fatalf("read failure hidden: %v", err)
	}
}

func TestAgesSurviveCompactionAndRestartInTheStore_ACEXEC04(t *testing.T) {
	database := testdbfixture.Open(t, "execution-state.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := store.NewSQL(database)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	assistant := func(id string) api.Message {
		return api.Message{ID: id, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call-" + id, Name: "read"}}}
	}
	tool := func(m api.Message) api.Message {
		m.ID = "result-" + m.ToolResult.ToolCallID
		return m
	}
	testutil.FailErr(t, "append opening rows", st.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "user", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "go"},
		assistant("batch-failed"),
		tool(rejectedWrite("failed", "a.go"))))
	appendBatch := func(store interface {
		AppendMessages(context.Context, string, ...api.Message) error
	}, i int) {
		id := fmt.Sprintf("batch-%d", i)
		testutil.FailErr(t, "append batch", store.AppendMessages(t.Context(), sess.ID, assistant(id),
			tool(receiptMessage(receiptSpec{batch: id, call: "call-" + id, tool: "read", digest: id}))))
	}
	for age := 1; age <= MaxRejectedOperationStepAge; age++ {
		appendBatch(st, age)
		rows, err := st.GetMessages(t.Context(), sess.ID)
		testutil.FailErr(t, "read canonical", err)
		last := rows[len(rows)-1]
		// Every pass publishes a view covering all rows, then reopens the store.
		testutil.FailErr(t, "publish view", st.PutCompactionView(t.Context(), sess.ID, store.CompactionView{
			Generation: int64(age), CoveredThroughOrd: last.Ord, CoveredThroughID: last.ID, SourceSeq: last.Seq,
			Messages: []api.Message{{ID: "summary", Role: api.MessageRoleAssistant, ContentParts: []api.MessageContentPart{{Source: "compaction_summary"}}}},
		}))
		st = store.NewSQL(database)
		window, err := ReadExecutionStateWindow(t.Context(), st.ReadMessageWindow, sess.ID)
		testutil.FailErr(t, "read window", err)
		got := projectExecutionState(window, "")
		if age < MaxRejectedOperationStepAge {
			if !sameCalls(got.Operations, "failed") || got.Operations[0].Age != age || got.Operations[0].Path != `"a.go"` {
				t.Fatalf("age %d lost across compaction and restart: %+v", age, got.Operations)
			}
		} else if len(got.Operations) != 0 {
			t.Fatalf("expired reminder resurrected: %+v", got.Operations)
		}
	}
}
