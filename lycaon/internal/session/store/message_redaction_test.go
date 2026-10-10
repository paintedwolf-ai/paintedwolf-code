package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const storedCredential = "ghp_A9fK2mQ7zX4bR1nT6yW8pL3vC5dH0jS2gU7e"

// installTestRedactor binds a durable secret policy for one test.
func installTestRedactor(t *testing.T) {
	t.Helper()
	SetMessageRedactor(func(_ context.Context, msg api.Message) (api.Message, bool) {
		if !strings.Contains(msg.Content, storedCredential) {
			return msg, false
		}
		msg.Content = strings.ReplaceAll(msg.Content, storedCredential, "[REDACTED]")
		return msg, true
	})
	t.Cleanup(func() { SetMessageRedactor(nil) })
}

func userMessage(id, content string) api.Message {
	return api.Message{
		ID: id, Role: api.MessageRoleUser, Content: content,
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser,
		TrustTier: api.ContentTrustTierTrusted,
	}
}

// Store-level screening covers every transcript write path.
func TestEveryTranscriptWritePathIsScreenedByTheStore(t *testing.T) {
	installTestRedactor(t)
	ctx := t.Context()
	st := openTurnSQLStore(t).(*SQL)
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	t.Run("AppendMessages", func(t *testing.T) {
		msg := userMessage("direct-append", "token "+storedCredential+" here")
		batch := []api.Message{msg}
		testutil.FailErr(t, "append", st.AppendMessages(ctx, sess.ID, batch...))

		// The caller's batch carries the screened row.
		if strings.Contains(batch[0].Content, storedCredential) {
			t.Fatalf("caller batch kept the credential for publication: %q", batch[0].Content)
		}
		assertStoredRowIsScreened(t, ctx, st, sess.ID, "direct-append")
	})

	t.Run("AppendMessagesWith", func(t *testing.T) {
		msg := userMessage("tx-append", "token "+storedCredential+" here")
		err := st.AppendMessagesWith(ctx, sess.ID, func(*sql.Tx) error { return nil }, msg)
		testutil.FailErr(t, "append with mutation", err)
		assertStoredRowIsScreened(t, ctx, st, sess.ID, "tx-append")
	})

	t.Run("UpdateMessage", func(t *testing.T) {
		msg := userMessage("updated", "clean")
		testutil.FailErr(t, "append", st.AppendMessages(ctx, sess.ID, msg))

		replacement := msg
		replacement.Content = "token " + storedCredential + " here"
		got, err := st.UpdateMessage(ctx, sess.ID, msg.ID, replacement)
		testutil.FailErr(t, "update", err)
		if strings.Contains(got.Content, storedCredential) {
			t.Fatalf("update returned the credential: %q", got.Content)
		}
		assertStoredRowIsScreened(t, ctx, st, sess.ID, "updated")
	})
}

func assertStoredRowIsScreened(t *testing.T, ctx context.Context, st *SQL, sessionID, messageID string) {
	t.Helper()
	msgs, err := st.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "read messages", err)
	for _, msg := range msgs {
		if msg.ID != messageID {
			continue
		}
		if strings.Contains(msg.Content, storedCredential) {
			t.Fatalf("stored row kept the credential: %q", msg.Content)
		}
		if !strings.Contains(msg.Content, "[REDACTED]") {
			t.Fatalf("stored row lost the redaction marker: %q", msg.Content)
		}
		return
	}
	t.Fatalf("message %s not found in the transcript", messageID)
}

func TestMessageRedactorReleasePreservesReplacementOnDurableWrites(t *testing.T) {
	install := func(marker string) func() {
		return SetMessageRedactor(func(_ context.Context, msg api.Message) (api.Message, bool) {
			msg.Content = strings.ReplaceAll(msg.Content, "owner-marker", marker)
			return msg, true
		})
	}
	releaseOld := install("old-owner")
	releaseCurrent := install("current-owner")
	defer releaseOld()
	defer releaseCurrent()
	st := openTurnSQLStore(t).(*SQL)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create owner test session", err)
	releaseOld()
	releaseOld()
	testutil.FailErr(t, "append through replacement", st.AppendMessages(t.Context(), sess.ID, userMessage("before-release", "owner-marker")))
	releaseCurrent()
	testutil.FailErr(t, "append after owner release", st.AppendMessages(t.Context(), sess.ID, userMessage("after-release", "owner-marker")))
	messages, err := st.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read screened durable rows", err)
	if len(messages) != 2 || messages[0].Content != "current-owner" || messages[1].Content != "owner-marker" {
		t.Fatalf("owner lifetime changed durable screening: %#v", messages)
	}
}
