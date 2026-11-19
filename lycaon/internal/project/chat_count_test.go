package project

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
)

func TestProjectChatCountTracksRootLifecycleWithoutWorkers(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "chat-count.db")
	reg := NewSQLRegistry(sqlDB)
	p, err := CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "create project", err)
	other, err := CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "create other project", err)
	now := timelayout.Format(time.Now().UTC())
	archive := func(id string) {
		t.Helper()
		_, err := sqlDB.ExecContext(t.Context(), `UPDATE sessions SET archived_at = ? WHERE id = ?`, now, id)
		testutil.FailErr(t, "archive session", err)
	}
	assertCount := func(want int) {
		t.Helper()
		got, err := reg.Get(t.Context(), p.ID)
		testutil.FailErr(t, "get project stats", err)
		if got.SessionCount != want {
			t.Fatalf("Get chat count = %d want %d", got.SessionCount, want)
		}
		projects, err := reg.List(t.Context())
		testutil.FailErr(t, "list project stats", err)
		for _, item := range projects {
			if item.ID == p.ID && item.SessionCount != want {
				t.Fatalf("List chat count = %d want %d", item.SessionCount, want)
			}
		}
	}
	assertCount(0)
	testdbseed.InsertSession(t, sqlDB, "chat-a", p.ID)
	testdbseed.InsertSession(t, sqlDB, "chat-b", p.ID)
	archive("chat-b")
	testdbseed.InsertSession(t, sqlDB, "other-chat", other.ID)
	assertCount(2)
	testdbseed.InsertChildSession(t, sqlDB, "worker-a", "chat-a")
	testdbseed.InsertChildSession(t, sqlDB, "worker-b", "chat-a")
	archive("worker-b")
	testdbseed.InsertChildSession(t, sqlDB, "nested-worker", "worker-a")
	assertCount(2)
	archive("chat-a")
	assertCount(2)
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE sessions SET archived_at = NULL WHERE id = 'chat-b'`)
	testutil.FailErr(t, "unarchive chat", err)
	assertCount(2)
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE sessions SET project_id = ? WHERE id = 'chat-b'`, other.ID)
	testutil.FailErr(t, "move root chat", err)
	assertCount(1)
	_, err = sqlDB.ExecContext(t.Context(), `DELETE FROM sessions WHERE id = 'worker-a'`)
	testutil.FailErr(t, "delete worker tree", err)
	assertCount(1)
	_, err = sqlDB.ExecContext(t.Context(), `DELETE FROM sessions WHERE id = 'chat-a'`)
	testutil.FailErr(t, "delete root chat", err)
	assertCount(0)
}
