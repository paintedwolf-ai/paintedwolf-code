package session

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestAgentNotePairSSEAndHydration(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "note-pair.db")

	sqlStore := store.NewSQL(sqlDB)
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := NewManager(sqlStore, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetEventPublisher(pub)

	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := sqlStore.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	t.Cleanup(unsub)

	toolID := uuid.NewString()
	noteID := uuid.NewString()
	toolMsg := wire.Message{
		ID:         toolID,
		Role:       wire.MessageRoleTool,
		Content:    `{"status":"noted","message_id":"` + noteID + `"}`,
		Visibility: wire.MessageVisibilityTranscript,
		CreatedAt:  time.Now().UTC(),
		ToolResult: &wire.ToolResult{
			ToolCallID: "call_note",
			Tool:       "surface_note",
			Content:    `{"status":"noted"}`,
			Outcome:    wire.ToolResultOutcomeCompleted,
		},
	}
	noteMsg := wire.Message{
		ID:         noteID,
		Role:       wire.MessageRoleAssistant,
		Kind:       wire.MessageKindAgentNote,
		Content:    "Auth lives in middleware.go.",
		Visibility: wire.MessageVisibilityTranscript,
		CreatedAt:  time.Now().UTC().Add(time.Millisecond),
		Grounding:  &wire.CitationGrounding{Traced: true},
	}

	testutil.FailErr(t, "append pair", mgr.Transcript.Append(ctx, sess.ID, toolMsg, noteMsg))

	toolEv := waitParentMessageAppend(t, ch, sess.ID, toolID)
	noteEv := waitParentMessageAppend(t, ch, sess.ID, noteID)
	if toolEv.Message.Ord <= 0 || noteEv.Message.Ord <= 0 {
		t.Fatalf("SSE ords tool=%d note=%d", toolEv.Message.Ord, noteEv.Message.Ord)
	}
	if noteEv.Message.Ord != toolEv.Message.Ord+1 {
		t.Fatalf("SSE ord gap: tool=%d note=%d want contiguous", toolEv.Message.Ord, noteEv.Message.Ord)
	}
	if noteEv.Message.Kind != wire.MessageKindAgentNote {
		t.Fatalf("SSE kind = %q", noteEv.Message.Kind)
	}

	hydrated, err := sqlStore.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	var toolRow, noteRow *wire.Message
	for i := range hydrated {
		switch hydrated[i].ID {
		case toolID:
			toolRow = &hydrated[i]
		case noteID:
			noteRow = &hydrated[i]
		}
	}
	if toolRow == nil || noteRow == nil {
		t.Fatalf("hydration missing rows tool=%v note=%v len=%d", toolRow != nil, noteRow != nil, len(hydrated))
	}
	if noteRow.Ord != toolRow.Ord+1 {
		t.Fatalf("hydrate ord gap: tool=%d note=%d", toolRow.Ord, noteRow.Ord)
	}
	if noteRow.Kind != wire.MessageKindAgentNote {
		t.Fatalf("hydrate kind = %q", noteRow.Kind)
	}
	if noteRow.Grounding == nil || !noteRow.Grounding.Traced {
		t.Fatalf("hydrate grounding = %+v", noteRow.Grounding)
	}
}
