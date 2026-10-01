package project

import (
	"database/sql"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectChatCountTracksRootLifecycleWithoutWorkers(t *testing.T) {
	SetDefaultOpenPolicy(TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "chat-count.db")
	reg := NewSQLRegistry(sqlDB)
	p, err := CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "create project", err)
	other, err := CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "create other project", err)
	now := db.FormatTime(time.Now().UTC())
	insert := func(id, projectID, parent string, archived bool) {
		t.Helper()
		archive := sql.NullString{String: now, Valid: archived}
		_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO sessions
			(id, project_id, owner_person_id, parent_session_id, posture, status, archived_at, created_at, activity_at, updated_at)
			VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', ?, ?, ?, ?)`,
			id, projectID, sql.NullString{String: parent, Valid: parent != ""}, archive, now, now, now)
		testutil.FailErr(t, "insert session", err)
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
	insert("chat-a", p.ID, "", false)
	insert("chat-b", p.ID, "", true)
	insert("other-chat", other.ID, "", false)
	assertCount(2)
	insert("worker-a", p.ID, "chat-a", false)
	insert("worker-b", p.ID, "chat-a", true)
	insert("nested-worker", p.ID, "worker-a", false)
	assertCount(2)
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE sessions SET archived_at = ? WHERE id = 'chat-a'`, now)
	testutil.FailErr(t, "archive chat", err)
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
