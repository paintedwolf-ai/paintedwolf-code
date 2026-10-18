package workerresults

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/closeouts"
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
	cards := NewCards(messages, memoryTranscript{messages}, nil, nil)
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
	cards := NewCards(messages, memoryTranscript{messages}, nil, nil)
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

type checkedTerminalWriter struct {
	memoryTranscript
	before  func()
	failure error
}

func (w checkedTerminalWriter) Update(ctx context.Context, session, id string, msg api.Message) error {
	w.before()
	if w.failure != nil {
		return w.failure
	}
	return w.memoryTranscript.Update(ctx, session, id, msg)
}

func TestTerminalWorkerPublicationRetiresPinnedCloseoutBeforeWake(t *testing.T) {
	for _, status := range []string{"complete", "failed", "canceled"} {
		for _, fails := range []bool{false, true} {
			t.Run(status+fmt.Sprint(fails), func(t *testing.T) {
				messages := store.NewMemory()
				parent, err := messages.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
				testutil.FailErr(t, "create parent", err)
				other, err := messages.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
				testutil.FailErr(t, "create other", err)
				recovery := closeouts.New(messages)
				for _, id := range []string{parent.ID, other.ID} {
					recovery.NoteCloseoutGroundingReject(t.Context(), id, guidance.InvestHandleNotObservedCode, "old", "stale body", nil)
				}
				before := recovery.RecordGroundingFriction(t.Context(), parent.ID)
				writer := checkedTerminalWriter{memoryTranscript: memoryTranscript{messages}, before: func() {
					if state := recovery.CloseoutStallState(t.Context(), parent.ID); state.Active || state.Drafted != "" || state.Attempt != 1 {
						t.Fatalf("publication retained old conclusion or reset budgets: %+v", state)
					}
				}}
				if fails {
					writer.failure = errors.New("publication failed")
				}
				cards := NewCards(messages, writer, nil, recovery)
				target := parent
				if status == "failed" {
					target, err = messages.CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "implementer"})
					testutil.FailErr(t, "nested parent", err)
				}
				envelope := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{JobID: "job", ChildSessionID: "child", AgentType: "implementer", State: status})
				summary := &api.WorkerSummaryMeta{WorkerID: "job", ChildSessionID: "child", AgentType: "implementer", Status: api.WorkerSummaryStatus(status), Envelope: envelope}
				err = cards.Project(t.Context(), target.ID, "job", summary)
				if (err != nil) != fails {
					t.Fatalf("publication err=%v want failure=%v", err, fails)
				}
				if got := recovery.RecordGroundingFriction(t.Context(), parent.ID); got.Remaining != before.Remaining-1 {
					t.Fatalf("publication reset friction: before=%+v after=%+v", before, got)
				}
				if state := recovery.CloseoutStallState(t.Context(), other.ID); !state.Active || state.Drafted != "stale body" {
					t.Fatalf("unrelated root changed: %+v", state)
				}
				// No parent wake or worker-result acknowledgement is needed for retirement.
			})
		}
	}
}
