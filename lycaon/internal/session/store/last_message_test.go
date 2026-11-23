package store

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLastMessageReadersSkipInternalRows(t *testing.T) {
	st := openPinTestStore(t)
	ctx := t.Context()
	id := createPinTestChats(t, st, 1)[0]

	testutil.FailErr(t, "append", st.Transcript.AppendMessages(ctx, id,
		api.Message{Role: api.MessageRoleUser, Content: "question"},
		api.Message{Role: api.MessageRoleAssistant, Content: "answer"},
		api.Message{Role: api.MessageRoleUser, Content: "repair prompt", Visibility: api.MessageVisibilityInternal},
		api.Message{Role: api.MessageRoleAssistant, Content: "repair draft", Visibility: api.MessageVisibilityInternal},
	))

	turn, err := st.Transcript.LastTurnMessageContent(ctx, id)
	testutil.FailErr(t, "last turn", err)
	if turn != "answer" {
		t.Fatalf("LastTurnMessageContent = %q, want the newest visible row", turn)
	}
	assistant, err := st.Transcript.LastAssistantMessageContent(ctx, id)
	testutil.FailErr(t, "last assistant", err)
	if assistant != "answer" {
		t.Fatalf("LastAssistantMessageContent = %q, want the newest visible row", assistant)
	}
}

func TestLastMessageReadersReturnEmptyWhenOnlyInternalRows(t *testing.T) {
	st := openPinTestStore(t)
	ctx := t.Context()
	id := createPinTestChats(t, st, 1)[0]

	testutil.FailErr(t, "append", st.Transcript.AppendMessages(ctx, id,
		api.Message{Role: api.MessageRoleUser, Content: "host note", Visibility: api.MessageVisibilityInternal},
		api.Message{Role: api.MessageRoleAssistant, Content: "host reply", Visibility: api.MessageVisibilityInternal},
	))

	turn, err := st.Transcript.LastTurnMessageContent(ctx, id)
	testutil.FailErr(t, "last turn", err)
	assistant, err := st.Transcript.LastAssistantMessageContent(ctx, id)
	testutil.FailErr(t, "last assistant", err)
	if turn != "" || assistant != "" {
		t.Fatalf("readers returned internal content: turn=%q assistant=%q", turn, assistant)
	}
}
