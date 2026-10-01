package db_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSearchMessageFTSTriggerOnInsert(t *testing.T) {
	dir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dir, "store.db"))
	testutil.FailErr(t, "db.Open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	ctx := context.Background()
	_, err = sqlDB.ExecContext(ctx, `
		INSERT OR IGNORE INTO projects (id, last_opened_at, created_at)
		VALUES ('p1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
	`)
	testutil.FailErr(t, "insert project", err)
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO sessions (id, project_id, owner_person_id, posture, status, created_at, activity_at, updated_at)
		VALUES ('s1', 'p1', (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
	`)
	testutil.FailErr(t, "insert session", err)
	testdbseed.InsertSessionEntry(t, sqlDB, "e1", "s1", "utterance", "m1", 1)
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority, trust_tier, kind, ts)
		VALUES ('m1', 'e1', 's1', 'user', 'hello trigger fts', 'user', 'user', 'trusted', '', '2026-01-01T00:00:00Z')
	`)
	testutil.FailErr(t, "insert message", err)

	var hits int
	testutil.FailErr(t, "messages_fts match", sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM messages m
		JOIN messages_fts ON messages_fts.rowid = m.rowid
		WHERE m.id = ? AND messages_fts MATCH ?
	`, "m1", `"hello"`).Scan(&hits))
	if hits != 1 {
		t.Fatalf("messages_fts hits = %d, want 1", hits)
	}

	testdbseed.InsertSessionEntry(t, sqlDB, "e2", "s1", "model_output", "m2", 2)
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO messages (
			id, entry_id, session_id, role, content, origin, authority, trust_tier,
			kind, draft_status, visibility, ts
		) VALUES (
			'm2', 'e2', 's1', 'assistant', 'unsettled private token', 'model', 'none', 'trusted',
			'draft', 'live', 'transcript', '2026-01-01T00:00:01Z'
		)
	`)
	testutil.FailErr(t, "insert live draft", err)
	testutil.FailErr(t, "probe live draft", sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH ?
	`, `"unsettled"`).Scan(&hits))
	if hits != 0 {
		t.Fatalf("live draft FTS hits = %d, want 0", hits)
	}

	_, err = sqlDB.ExecContext(ctx, `UPDATE messages SET draft_status = 'committed' WHERE id = 'm2'`)
	testutil.FailErr(t, "commit draft", err)
	testutil.FailErr(t, "probe committed draft", sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH ?
	`, `"unsettled"`).Scan(&hits))
	if hits != 1 {
		t.Fatalf("committed draft FTS hits = %d, want 1", hits)
	}
}

func TestSearchEvidenceFTSTriggerOnUpsert(t *testing.T) {
	dir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dir, "store.db"))
	testutil.FailErr(t, "db.Open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	ctx := context.Background()
	_, err = sqlDB.ExecContext(ctx, `
		INSERT OR IGNORE INTO projects (id, last_opened_at, created_at)
		VALUES ('p1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
	`)
	testutil.FailErr(t, "insert project", err)

	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "BeginTx", err)
	rows := []search.IndexRow{{
		ID:        search.RowID(search.SourceMessage, "m1", "cited:h1"),
		ProjectID: "p1",
		Source:    search.SourceMessage,
		HitKind:   search.HitKindEvidence,
		SessionID: "s1",
		MessageID: "m1",
		SourceRef: "m1",
		Handle:    "h1",
		Kind:      search.HitKindEvidence,
		Snippet:   "trigger evidence snippet",
		TS:        "2026-01-01T00:00:00Z",
	}}
	store := search.NewStore()
	testutil.FailErr(t, "UpsertRows", store.UpsertRows(ctx, tx, rows))
	testutil.FailErr(t, "Commit", tx.Commit())

	var hits int
	testutil.FailErr(t, "evidence_fts match", sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM evidence_index e
		JOIN evidence_fts ON evidence_fts.rowid = e.rowid
		WHERE e.id = ? AND evidence_fts MATCH ?
	`, rows[0].ID, `"trigger"`).Scan(&hits))
	if hits != 1 {
		t.Fatalf("evidence_fts hits = %d, want 1", hits)
	}
}

func TestSearchSyncMessageWriteThroughUsesTriggers(t *testing.T) {
	dir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dir, "store.db"))
	testutil.FailErr(t, "db.Open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	ctx := context.Background()
	_, err = sqlDB.ExecContext(ctx, `
		INSERT OR IGNORE INTO projects (id, last_opened_at, created_at)
		VALUES ('p1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
	`)
	testutil.FailErr(t, "insert project", err)
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO sessions (id, project_id, owner_person_id, posture, status, created_at, activity_at, updated_at)
		VALUES ('s1', 'p1', (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
	`)
	testutil.FailErr(t, "insert session", err)

	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "BeginTx", err)
	msg := api.Message{
		ID:        "m1",
		Role:      api.MessageRoleUser,
		Content:   "write-through trigger token",
		CreatedAt: time.Now().UTC(),
		Grounding: &api.CitationGrounding{
			Traced: true,
			CitedEvidence: []api.CitationGroundingCitedEvidence{{
				Handle:  "h1",
				Excerpt: "write-through evidence token",
				Verdict: api.CitationVerdictMatched,
			}},
		},
	}
	testdbseed.InsertSessionEntry(t, tx, "entry-"+msg.ID, "s1", "utterance", msg.ID, 1)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority, trust_tier, kind, ts)
		VALUES (?, ?, ?, ?, ?, 'user', 'user', 'trusted', '', ?)
	`, msg.ID, "entry-"+msg.ID, "s1", msg.Role, msg.Content, db.FormatTime(msg.CreatedAt))
	testutil.FailErr(t, "insert message", err)
	testutil.FailErr(t, "SyncMessageWriteThrough", search.SyncMessageWriteThrough(ctx, tx, "p1", "s1", msg))
	testutil.FailErr(t, "Commit", tx.Commit())

	var messageHits int
	testutil.FailErr(t, "messages_fts", sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM messages m JOIN messages_fts ON messages_fts.rowid = m.rowid
		WHERE m.id = ? AND messages_fts MATCH ?
	`, msg.ID, `"write-through"`).Scan(&messageHits))
	if messageHits != 1 {
		t.Fatalf("message fts hits = %d", messageHits)
	}

	// Both the cited-evidence row and the projected message-text row carry
	// "write-through" in their snippet, so the trigger-backed FTS matches both.
	var evidenceHits int
	testutil.FailErr(t, "evidence_fts", sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM evidence_index e JOIN evidence_fts ON evidence_fts.rowid = e.rowid
		WHERE e.message_id = ? AND evidence_fts MATCH ?
	`, msg.ID, `"write-through"`).Scan(&evidenceHits))
	if evidenceHits != 2 {
		t.Fatalf("evidence fts hits = %d", evidenceHits)
	}
}
