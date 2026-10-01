//go:build integration

package property

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

// Updating live message fields preserves the session's row count.
func TestStorePatchNeverRemovesRows(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "append-only.db")
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := store.NewSQL(sqlDB)

	rapid.Check(t, func(t *rapid.T) {
		// Fresh session per iteration so the row count is isolated.
		sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		failErr(t, "create", err)

		count := rapid.IntRange(1, 5).Draw(t, "count")
		ids := make([]string, 0, count)
		for i := 0; i < count; i++ {
			id := uuid.NewString()
			ids = append(ids, id)
			failErr(t, "append", store.AppendMessages(ctx, sess.ID, api.Message{
				ID: id, Role: api.MessageRoleAssistant, Content: rapid.String().Draw(t, "body"),
			}))
		}
		for _, id := range ids {
			rows, err := store.GetMessages(ctx, sess.ID)
			failErr(t, "get", err)
			var row api.Message
			for _, m := range rows {
				if m.ID == id {
					row = m
					break
				}
			}
			row.DraftStatus = api.DraftStatusRejected
			_, err = store.UpdateMessage(ctx, sess.ID, id, row)
			failErr(t, "patch", err)
		}
		after, err := store.GetMessages(ctx, sess.ID)
		failErr(t, "get after", err)
		if len(after) != count {
			t.Fatalf("row count = %d want %d (append-only store)", len(after), count)
		}
	})
}
