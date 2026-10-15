package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSearchProjectionWriteThroughAndFTS(t *testing.T) {
	dir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dir, "store.db"))
	testutil.FailErr(t, "db.Open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx := context.Background()
	sessionID := "sess-search-1"
	testdbseed.InsertSessionWithRoot(t, sqlDB, sessionID, testdbseed.DefaultProjectID, dir)

	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "BeginTx", err)
	msg := api.Message{
		ID:        "msg-fts-1",
		Role:      api.MessageRoleUser,
		Content:   "alpha bravo unique-token-1061",
		CreatedAt: time.Now().UTC(),
		Grounding: &api.CitationGrounding{
			Traced: true,
			CitedEvidence: []api.CitationGroundingCitedEvidence{{
				Handle:  "read#1",
				Path:    "src/a.go",
				Line:    1,
				Excerpt: "unique-evidence-snippet-1061",
				Verdict: api.CitationVerdictMatched,
			}},
		},
	}
	testdbseed.InsertSessionEntry(t, tx, "entry-"+msg.ID, sessionID, "utterance", msg.ID, 1)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO messages (
			id, entry_id, session_id, role, content, origin, authority, trust_tier, kind, ts
		) VALUES (?, ?, ?, ?, ?, 'user', 'user', 'trusted', '', ?)
	`, msg.ID, "entry-"+msg.ID, sessionID, msg.Role, msg.Content, db.FormatTime(msg.CreatedAt))
	testutil.FailErr(t, "insert message", err)
	testutil.FailErr(t, "SyncMessageWriteThrough", search.SyncMessageWriteThrough(ctx, tx, testdbseed.DefaultProjectID, sessionID, msg))
	testutil.FailErr(t, "Commit", tx.Commit())

	// One cited-evidence row plus the projected message-text row (hit_kind=message).
	var evidenceCount int
	testutil.FailErr(t, "count evidence_index", sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM evidence_index WHERE project_id = ? AND message_id = ?
	`, testdbseed.DefaultProjectID, msg.ID).Scan(&evidenceCount))
	if evidenceCount != 2 {
		t.Fatalf("evidence_index rows = %d, want 2", evidenceCount)
	}

	var messageKindRows int
	testutil.FailErr(t, "count message rows", sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM evidence_index WHERE message_id = ? AND hit_kind = 'message'
	`, msg.ID).Scan(&messageKindRows))
	if messageKindRows != 1 {
		t.Fatalf("message-text rows = %d, want 1", messageKindRows)
	}

	var messageHits int
	testutil.FailErr(t, "messages_fts match", sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM messages m
		JOIN messages_fts ON messages_fts.rowid = m.rowid
		WHERE m.id = ? AND messages_fts MATCH ?
	`, msg.ID, `"unique-token-1061"`).Scan(&messageHits))
	if messageHits != 1 {
		t.Fatalf("messages_fts hits = %d, want 1", messageHits)
	}

	var evidenceHits int
	testutil.FailErr(t, "evidence_fts match", sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM evidence_index e
		JOIN evidence_fts ON evidence_fts.rowid = e.rowid
		WHERE e.message_id = ? AND evidence_fts MATCH ?
	`, msg.ID, `"unique-evidence-snippet-1061"`).Scan(&evidenceHits))
	if evidenceHits != 1 {
		t.Fatalf("evidence_fts hits = %d, want 1", evidenceHits)
	}
}

func TestSyncMessageWriteThroughReplacesEvidence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dir, "store.db"))
	testutil.FailErr(t, "db.Open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	sessionID := "sess-replace-1"
	testdbseed.InsertSessionWithRoot(t, sqlDB, sessionID, testdbseed.DefaultProjectID, dir)

	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "BeginTx", err)
	msg := api.Message{ID: "msg-replace-1", Role: api.MessageRoleUser, Content: "old-unique-token", CreatedAt: time.Now().UTC()}
	testdbseed.InsertSessionEntry(t, tx, "entry-"+msg.ID, sessionID, "utterance", msg.ID, 1)
	_, err = tx.ExecContext(ctx, `INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority, trust_tier, kind, ts) VALUES (?, ?, ?, ?, ?, 'user', 'user', 'trusted', '', ?)`,
		msg.ID, "entry-"+msg.ID, sessionID, msg.Role, msg.Content, db.FormatTime(msg.CreatedAt))
	testutil.FailErr(t, "insert", err)
	testutil.FailErr(t, "write-through old", search.SyncMessageWriteThrough(ctx, tx, testdbseed.DefaultProjectID, sessionID, msg))
	testutil.FailErr(t, "Commit", tx.Commit())

	tx, err = sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "BeginTx patch", err)
	msg.Content = "new-unique-token"
	testutil.FailErr(t, "write-through new", search.SyncMessageWriteThrough(ctx, tx, testdbseed.DefaultProjectID, sessionID, msg))
	testutil.FailErr(t, "Commit patch", tx.Commit())

	var oldHits, newHits int
	testutil.FailErr(t, "evidence_fts old", sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM evidence_index e
		JOIN evidence_fts ON evidence_fts.rowid = e.rowid
		WHERE e.message_id = ? AND evidence_fts MATCH ?
	`, msg.ID, `"old-unique-token"`).Scan(&oldHits))
	if oldHits != 0 {
		t.Fatalf("evidence_fts old hits = %d, want 0 after in-place write-through", oldHits)
	}
	testutil.FailErr(t, "evidence_fts new", sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM evidence_index e
		JOIN evidence_fts ON evidence_fts.rowid = e.rowid
		WHERE e.message_id = ? AND evidence_fts MATCH ?
	`, msg.ID, `"new-unique-token"`).Scan(&newHits))
	if newHits == 0 {
		t.Fatal("evidence_fts new hits = 0, want ≥1 after in-place write-through")
	}
}

func TestToolResultPathUpgradeRepairsPagedProjectionAndFTS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	database, err := db.Open(path)
	testutil.FailErr(t, "open projection fixture", err)
	const session = "path-repair"
	testdbseed.InsertSessionWithRoot(t, database, session, testdbseed.DefaultProjectID, t.TempDir())
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin retained observation fixture", err)
	const target = "\u00a0observations.txt\t"
	for i := range 260 {
		id := fmt.Sprintf("observation-%04d", i)
		result := &api.ToolResult{Tool: "read", ToolCallID: id, Content: "original observation", ToolArgs: map[string]any{"path": target}}
		raw, err := json.Marshal(result)
		testutil.FailErr(t, "encode original result", err)
		testdbseed.InsertSessionEntry(t, tx, id, session, "model_output", id, int64(i+1))
		_, err = tx.ExecContext(t.Context(), `INSERT INTO messages(id,entry_id,session_id,role,content,tool_result_json,origin,authority,trust_tier,ord,seq,ts) VALUES(?,?,?,'tool','original observation',?,'tool','none','untrusted',?,?,'2026-10-15T00:00:00Z')`, id, id, session, string(raw), i+1, i+1)
		testutil.FailErr(t, "insert retained observation", err)
		testutil.FailErr(t, "index original result", search.SyncMessageWriteThrough(t.Context(), tx, testdbseed.DefaultProjectID, session, api.Message{ID: id, Role: api.MessageRoleTool, Content: "original observation", ToolResult: result}))
	}
	_, err = tx.ExecContext(t.Context(), `UPDATE evidence_index SET path=NULL WHERE session_id=?`, session)
	testutil.FailErr(t, "emulate missing projection paths", err)
	_, err = tx.ExecContext(t.Context(), `UPDATE evidence_index SET path='retained-target.go' WHERE message_id='observation-0000'`)
	testutil.FailErr(t, "seed existing path", err)
	_, err = tx.ExecContext(t.Context(), `UPDATE evidence_index SET shape='url' WHERE message_id='observation-0001'`)
	testutil.FailErr(t, "seed unrelated observation shape", err)
	testutil.FailErr(t, "commit source fixture", tx.Commit())
	testutil.FailErr(t, "close source fixture", database.Shutdown(t.Context()))
	source, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open prior revision", err)
	_, err = source.Exec(`PRAGMA user_version=3`)
	testutil.FailErr(t, "set source revision", err)
	testutil.FailErr(t, "close prior revision", source.Close())
	testutil.FailErr(t, "upgrade recorded projection", db.UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "inspect repaired projection", err)
	defer func() { _ = upgraded.Close() }()
	var count int
	testutil.FailErr(t, "count repaired paths", upgraded.QueryRow(`SELECT COUNT(*) FROM evidence_index WHERE session_id=? AND path='observations.txt'`, session).Scan(&count))
	if count != 258 {
		t.Fatalf("repaired %d paths, want 258 across multiple pages", count)
	}
	testutil.FailErr(t, "count indexed paths", upgraded.QueryRow(`SELECT COUNT(*) FROM evidence_index e JOIN evidence_fts ON evidence_fts.rowid=e.rowid WHERE e.session_id=? AND e.path='observations.txt' AND evidence_fts MATCH '"observations.txt"'`, session).Scan(&count))
	if count != 258 {
		t.Fatalf("FTS retained %d repaired paths, want 258", count)
	}
	testutil.FailErr(t, "check unrelated rows", upgraded.QueryRow(`SELECT COUNT(*) FROM evidence_index WHERE (message_id='observation-0000' AND path='retained-target.go') OR (message_id='observation-0001' AND path IS NULL)`).Scan(&count))
	if count != 2 {
		t.Fatal("repair changed an existing path or an unrelated shape")
	}
	testutil.FailErr(t, "check retained bytes", upgraded.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=? AND (content!='original observation' OR json_extract(tool_result_json,'$.content')!='original observation' OR json_extract(tool_result_json,'$.tool_args.path')!=?)`, session, target).Scan(&count))
	if count != 0 {
		t.Fatal("projection repair changed canonical observations or arguments")
	}
	testutil.FailErr(t, "reopen current upgrade", db.UpgradeStaged(t.Context(), path))
}
