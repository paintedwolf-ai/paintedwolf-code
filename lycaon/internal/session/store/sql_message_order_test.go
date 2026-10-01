package store

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestAppendMessagesBatchSharesTimestampAndPreservesOrder verifies stable batch ordering.
func TestAppendMessagesBatchSharesTimestampAndPreservesOrder(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "msg-order.db")

	store := NewSQL(sqlDB)
	ctx := context.Background()
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	// The batch uses automatic timestamps.
	batch := []api.Message{
		{Role: api.MessageRoleUser, Content: "fix handler"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c1", Name: "edit"}}},
		{Role: api.MessageRoleTool, Content: "ok", ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted}},
	}
	testutil.FailErr(t, "append batch", store.AppendMessages(ctx, sess.ID, batch...))

	got, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	if len(got) != len(batch) {
		t.Fatalf("got %d messages, want %d", len(got), len(batch))
	}

	wantRoles := []api.MessageRole{api.MessageRoleUser, api.MessageRoleAssistant, api.MessageRoleTool}
	for i, want := range wantRoles {
		if got[i].Role != want {
			t.Fatalf("message %d role = %q, want %q — batch did not return in insertion order", i, got[i].Role, want)
		}
	}

	// One logical append carries one timestamp.
	for i := 1; i < len(got); i++ {
		if !got[i].CreatedAt.Equal(got[0].CreatedAt) {
			t.Fatalf("message %d ts %v differs from batch ts %v — a batch must share one timestamp", i, got[i].CreatedAt, got[0].CreatedAt)
		}
	}
	// The boundary follows the leading user message.
	if boundary := api.UserIntentBoundary(got); boundary != 1 {
		t.Fatalf("UserIntentBoundary = %d, want 1 (user leads, edit follows)", boundary)
	}
}

func TestAppendMessagesRejectsExistingIDSQL(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "msg-dup.db")
	st := NewSQL(sqlDB)
	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	msg := api.Message{ID: "same-id", Role: api.MessageRoleUser, Content: "first"}
	testutil.FailErr(t, "first append", st.AppendMessages(ctx, sess.ID, msg))
	err = st.AppendMessages(ctx, sess.ID, api.Message{ID: "same-id", Role: api.MessageRoleUser, Content: "again"})
	if err == nil || !errors.Is(err, ErrDuplicateMessageID) {
		t.Fatalf("second append = %v want ErrDuplicateMessageID", err)
	}
}
