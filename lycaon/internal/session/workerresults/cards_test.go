package workerresults

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type memoryTranscript struct{ messages *store.Memory }

func (m memoryTranscript) Append(ctx context.Context, session string, messages ...api.Message) error {
	return m.messages.AppendMessages(ctx, session, messages...)
}
func (m memoryTranscript) Update(ctx context.Context, session, id string, message api.Message) error {
	_, err := m.messages.UpdateMessage(ctx, session, id, message)
	return err
}

func TestCardsRetainDispatchIdentityAcrossCompletionRetries(t *testing.T) {
	messages := store.NewMemory()
	parent, err := messages.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	cards := NewCards(messages, memoryTranscript{messages}, nil)
	input := WorkerDispatchRowInput{JobID: "job", AgentType: "implementer", ChildSessionID: "child", ToolCallID: "call"}
	testutil.FailErr(t, "record dispatch", cards.Ensure(t.Context(), parent.ID, input))
	before, err := messages.GetMessages(t.Context(), parent.ID)
	testutil.FailErr(t, "read dispatch", err)
	envelope := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{JobID: "job", ChildSessionID: "child", AgentType: "implementer", State: "complete", Summary: "Updated the report"})
	summary := &api.WorkerSummaryMeta{WorkerID: "job", ChildSessionID: "child", AgentType: "implementer", Status: api.WorkerSummaryStatusComplete, Envelope: envelope}
	for range 2 {
		testutil.FailErr(t, "project completion", cards.Project(t.Context(), parent.ID, "job", summary))
	}
	after, err := messages.GetMessages(t.Context(), parent.ID)
	testutil.FailErr(t, "read completion", err)
	if len(before) != 2 || len(after) != 2 || before[1].ID != after[1].ID || after[1].ToolResult.ToolCallID != "call" || after[1].WorkerSummary == nil || after[1].Content != envelope {
		t.Fatalf("completion changed canonical dispatch identity: before=%+v after=%+v", before, after)
	}
}

func TestCardsRejectMismatchedEnvelopeBeforeChangingTranscript(t *testing.T) {
	messages := store.NewMemory()
	parent, err := messages.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	cards := NewCards(messages, memoryTranscript{messages}, nil)
	envelope := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{JobID: "another-job", ChildSessionID: "child", AgentType: "implementer", State: "complete"})
	summary := &api.WorkerSummaryMeta{WorkerID: "job", ChildSessionID: "child", AgentType: "implementer", Status: api.WorkerSummaryStatusComplete, Envelope: envelope}
	if err := cards.Project(t.Context(), parent.ID, "job", summary); err == nil {
		t.Fatal("mismatched completion identity admitted")
	}
	after, err := messages.GetMessages(t.Context(), parent.ID)
	testutil.FailErr(t, "read unchanged transcript", err)
	if len(after) != 0 {
		t.Fatalf("invalid completion changed transcript: %+v", after)
	}
}
