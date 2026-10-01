package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSQLStoreCreateChildPersistsAgentType(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	ctx := t.Context()
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	if _, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{}); err == nil {
		t.Fatal("expected blank agent_type to fail")
	}
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: " implementer "})
	testutil.FailErr(t, "create child", err)
	if child.AgentType != "implementer" {
		t.Fatalf("child agent_type = %q want implementer", child.AgentType)
	}
	stored, err := store.Get(ctx, child.ID)
	testutil.FailErr(t, "get child", err)
	if stored.AgentType != "implementer" {
		t.Fatalf("stored agent_type = %q want implementer", stored.AgentType)
	}
}

func TestSQLStoreReferenceFenceAndArtifactIDsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	sess, err := store.Create(context.Background(), api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	wantID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	fence := "```attachment filename=\"readme.md\" mime=\"text/plain\" truncated=\"false\"\n[User attached file: readme.md]\n```"
	testutil.FailErr(t, "append", store.AppendMessages(context.Background(), sess.ID, api.Message{
		ID:          "msg-ref",
		Role:        api.MessageRoleUser,
		Content:     "look\n\n" + fence,
		ArtifactIDs: []string{wantID},
	}))
	msgs, err := store.GetMessages(context.Background(), sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 1 {
		t.Fatalf("msgs=%d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "[User attached file: readme.md]") {
		t.Fatalf("content missing fence: %q", msgs[0].Content)
	}
	if len(msgs[0].ArtifactIDs) != 1 || msgs[0].ArtifactIDs[0] != wantID {
		t.Fatalf("ArtifactIDs = %#v", msgs[0].ArtifactIDs)
	}
}

func TestSQLStoreArtifactIDsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	sess, err := store.Create(context.Background(), api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	wantID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testutil.FailErr(t, "append", store.AppendMessages(context.Background(), sess.ID, api.Message{
		ID:          "msg-art",
		Role:        api.MessageRoleUser,
		Content:     "with image",
		ArtifactIDs: []string{wantID},
	}))
	msgs, err := store.GetMessages(context.Background(), sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 1 || len(msgs[0].ArtifactIDs) != 1 || msgs[0].ArtifactIDs[0] != wantID {
		t.Fatalf("ArtifactIDs after append = %#v", msgs)
	}

	msgs[0].Content = "patched"
	msgs[0].ArtifactIDs = []string{wantID, "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"}
	_, err = store.UpdateMessage(context.Background(), sess.ID, "msg-art", msgs[0])
	testutil.FailErr(t, "UpdateMessage", err)
	msgs, err = store.GetMessages(context.Background(), sess.ID)
	testutil.FailErr(t, "GetMessages after update", err)
	if len(msgs) != 1 || len(msgs[0].ArtifactIDs) != 2 {
		t.Fatalf("ArtifactIDs after update = %#v", msgs[0].ArtifactIDs)
	}
}

func TestSQLStorePersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "store.db")

	sqlDB := testdbfixture.OpenPath(t, dbPath)
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	sess, err := store.Create(context.Background(), api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if err := store.AppendMessages(context.Background(), sess.ID, api.Message{
		ID:      "msg-1",
		Role:    api.MessageRoleUser,
		Content: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	testutil.FailErr(t, "close database for reopen", sqlDB.Close())

	sqlDB2 := testdbfixture.OpenPath(t, dbPath)
	store2 := NewSQL(sqlDB2)
	got, err := store2.Get(context.Background(), sess.ID)
	testutil.FailErr(t, "store2.Get failed", err)
	if got.ProjectID != testdbseed.DefaultProjectID {
		t.Fatalf("project_id = %q", got.ProjectID)
	}
	msgs, err := store2.GetMessages(context.Background(), sess.ID)
	if err != nil || len(msgs) != 1 || msgs[0].Content != "hello" {
		t.Fatalf("messages = %#v err=%v", msgs, err)
	}
}

func TestSQLDeleteRemovesSessionTreeAndPreservesReviewHistory(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	now := db.FormatTime(time.Now().UTC())
	seed := func(label, query string, args ...any) {
		t.Helper()
		_, seedErr := sqlDB.ExecContext(ctx, query, args...)
		testutil.FailErr(t, label, seedErr)
	}
	seed("sessions", `
		INSERT INTO sessions (id, project_id, owner_person_id, workspace_root_id, posture, status, created_at, activity_at, updated_at)
		VALUES
			('parent', ?, (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', ?, ?, ?),
			('survivor', ?, (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', ?, ?, ?)
	`, testdbseed.DefaultProjectID, rootID, now, now, now,
		testdbseed.DefaultProjectID, rootID, now, now, now)
	seed("child session", `
		INSERT INTO sessions (id, project_id, owner_person_id, workspace_root_id, posture, status, parent_session_id, created_at, activity_at, updated_at)
		VALUES ('child', ?, (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', 'parent', ?, ?, ?)
	`, testdbseed.DefaultProjectID, rootID, now, now, now)
	seed("child session entry", `
		INSERT INTO session_entries (id, session_id, ord, resource_kind, resource_id, created_at)
		VALUES ('child-entry', 'child', 1, 'utterance', 'child-message', ?)
	`, now)
	seed("child message", `
		INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority, trust_tier, ts)
		VALUES ('child-message', 'child-entry', 'child', 'user', 'x', 'user', 'user', 'trusted', ?);
	`, now)
	seed("evidence", `
		INSERT INTO evidence_index (id, project_id, source, hit_kind, session_id) VALUES
			('parent-evidence', ?, 'tool', 'evidence', 'parent'),
			('child-evidence', ?, 'tool', 'evidence', 'child'),
			('survivor-evidence', ?, 'tool', 'evidence', 'survivor')
	`, testdbseed.DefaultProjectID, testdbseed.DefaultProjectID, testdbseed.DefaultProjectID)
	seed("findings", `
		INSERT INTO findings (session_id, summary, created_at) VALUES ('parent', 'finding', ?);
	`, now)
	seed("progress", `
		INSERT INTO session_progress (session_id, content, updated_at) VALUES ('child', 'progress', ?);
	`, now)
	seed("source content", `
		INSERT INTO source_blob_objects (sha256, size, stored_size, storage_relpath, git_oid_sha1, git_oid_sha256) VALUES
			('orphan-sha', 1, 1, 'or/phan.zst', 'orphan-oid-sha1', 'orphan-oid-sha256'),
			('shared-sha', 1, 1, 'sh/ared.zst', 'shared-oid-sha1', 'shared-oid-sha256')
	`)
	seed("source files", `
		INSERT INTO source_files (id, project_id, entry_kind, created_ts) VALUES
			('child-file', ?, 'file', ?),
			('parent-file', ?, 'file', ?),
			('survivor-file', ?, 'file', ?)
	`, testdbseed.DefaultProjectID, now, testdbseed.DefaultProjectID, now, testdbseed.DefaultProjectID, now)
	seed("source operations", `
		INSERT INTO source_operations
			(id, project_id, origin, cause, session_id,
			 capture_quality, started_ts, committed_ts) VALUES
			('child-operation', ?, 'agent', 'tool', 'child', 'exact', ?, ?),
			('parent-operation', ?, 'agent', 'tool', 'parent', 'exact', ?, ?),
			('survivor-operation', ?, 'agent', 'tool', 'survivor', 'exact', ?, ?)
	`, testdbseed.DefaultProjectID, now, now, testdbseed.DefaultProjectID, now, now,
		testdbseed.DefaultProjectID, now, now)
	seed("source versions", `
		INSERT INTO source_versions
			(id, file_id, project_id, operation_id, root_id, path,
			 state, content_sha256, byte_size, capture_state, capture_quality, created_ts, seq) VALUES
			('child-version', 'child-file', ?, 'child-operation', ?, 'child.go',
			 'content', 'orphan-sha', 1, 'stored', 'exact', ?, 11),
			('parent-version', 'parent-file', ?, 'parent-operation', ?, 'parent.go',
			 'content', 'shared-sha', 1, 'stored', 'exact', ?, 12),
			('survivor-version', 'survivor-file', ?, 'survivor-operation', ?, 'survivor.go',
			 'content', 'shared-sha', 1, 'stored', 'exact', ?, 13)
	`, testdbseed.DefaultProjectID, rootID, now, testdbseed.DefaultProjectID, rootID, now,
		testdbseed.DefaultProjectID, rootID, now)
	seed("source effects", `
		INSERT INTO source_effects
			(id, project_id, operation_id, file_id, after_version_id, root_id, path, op,
			 entry_kind, ordinal, created_ts) VALUES
			('child-effect', ?, 'child-operation', 'child-file', 'child-version', ?, 'child.go', 'write', 'file', 1, ?),
			('parent-effect', ?, 'parent-operation', 'parent-file', 'parent-version', ?, 'parent.go', 'write', 'file', 2, ?),
			('survivor-effect', ?, 'survivor-operation', 'survivor-file', 'survivor-version', ?, 'survivor.go', 'write', 'file', 3, ?)
	`,
		testdbseed.DefaultProjectID, rootID, now, testdbseed.DefaultProjectID, rootID, now, testdbseed.DefaultProjectID, rootID, now)
	seed("line attribution", `
		INSERT INTO source_line_attr (project_id, file_id, start_line, end_line, effect_id)
		VALUES (?, 'child-file', 1, 1, 'child-effect')
	`, testdbseed.DefaultProjectID)

	testutil.FailErr(t, "delete parent", NewSQL(sqlDB).Delete(ctx, "parent"))
	assertCount := func(label, query string, want int) {
		t.Helper()
		var got int
		testutil.FailErr(t, label, sqlDB.QueryRowContext(ctx, query).Scan(&got))
		if got != want {
			t.Fatalf("%s count = %d want %d", label, got, want)
		}
	}
	assertCount("deleted sessions", `SELECT count(*) FROM sessions WHERE id IN ('parent', 'child')`, 0)
	assertCount("survivor session", `SELECT count(*) FROM sessions WHERE id = 'survivor'`, 1)
	// Tombstones keep identity while clearing content.
	assertCount("live evidence", `SELECT count(*) FROM evidence_index WHERE session_id IN ('parent', 'child') AND tombstoned = 0`, 0)
	assertCount("tombstoned evidence", `SELECT count(*) FROM evidence_index WHERE session_id IN ('parent', 'child') AND tombstoned = 1`, 2)
	assertCount("tombstone content", `SELECT count(*) FROM evidence_index
		WHERE session_id IN ('parent', 'child')
		  AND (snippet IS NOT NULL OR path IS NOT NULL OR url IS NOT NULL OR line IS NOT NULL)`, 0)
	assertCount("survivor evidence", `SELECT count(*) FROM evidence_index WHERE session_id = 'survivor'`, 1)
	assertCount("preserved source operations", `SELECT count(*) FROM source_operations WHERE session_id IN ('parent', 'child')`, 2)
	assertCount("survivor source operation", `SELECT count(*) FROM source_operations WHERE session_id = 'survivor'`, 1)
	assertCount("historical content", `SELECT count(*) FROM source_blob_objects WHERE sha256 = 'orphan-sha'`, 1)
	assertCount("shared content", `SELECT count(*) FROM source_blob_objects WHERE sha256 = 'shared-sha'`, 1)
	assertCount("line attribution", `SELECT count(*) FROM source_line_attr WHERE effect_id = 'child-effect'`, 1)
}

func TestSQLDeleteRevokesBlueprintGrantsSealedByTheSessionTree(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	now := db.FormatTime(time.Now().UTC())
	seed := func(label, query string, args ...any) {
		t.Helper()
		_, seedErr := sqlDB.ExecContext(ctx, query, args...)
		testutil.FailErr(t, label, seedErr)
	}
	seed("sessions", `
		INSERT INTO sessions (id, project_id, owner_person_id, workspace_root_id, posture, status, created_at, activity_at, updated_at)
		VALUES ('parent', ?, (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', ?, ?, ?), ('survivor', ?, (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', ?, ?, ?)
	`, testdbseed.DefaultProjectID, rootID, now, now, now,
		testdbseed.DefaultProjectID, rootID, now, now, now)
	seed("child session", `
		INSERT INTO sessions (id, project_id, owner_person_id, workspace_root_id, posture, status, parent_session_id, created_at, activity_at, updated_at)
		VALUES ('child', ?, (SELECT id FROM people WHERE role = 'owner'), ?, 'build', 'idle', 'parent', ?, ?, ?)
	`, testdbseed.DefaultProjectID, rootID, now, now, now)
	seed("child run", `
		INSERT INTO workflow_runs (id, session_id, project_id, workflow_id, workflow_version, status, current_phase, created_at, updated_at)
		VALUES ('child-run', 'child', ?, 'plan', '1', 'running', 'approve', ?, ?)
	`, testdbseed.DefaultProjectID, now, now)
	seed("authz event", `
		INSERT INTO authz_events (id, session_id, event_seq, row_hash, ts, action, outcome, resolved_by)
		VALUES ('ev-1', 'child', 1, 'hash', ?, 'blueprint_approved', 'allowed', 'human')
	`, now)
	seed("grants", `
		INSERT INTO blueprint_approvals (
			project_id, path, content_digest, workflow_run_id, workflow_revision, status, approved_at, approved_via, approved_by_person_id, session_id
		) VALUES
			(?, '.paintedwolf/blueprints/child.md', 'digest', 'child-run', 1, 'approved', ?, 'chat', (SELECT id FROM people WHERE role = 'owner'), 'child'),
			(?, '.paintedwolf/blueprints/survivor.md', 'digest', NULL, 0, 'approved', ?, 'chat', (SELECT id FROM people WHERE role = 'owner'), 'survivor')
	`, testdbseed.DefaultProjectID, now, testdbseed.DefaultProjectID, now)

	testutil.FailErr(t, "delete parent", NewSQL(sqlDB).Delete(ctx, "parent"))

	var status, cause string
	testutil.FailErr(t, "read child grant", sqlDB.QueryRowContext(ctx, `
		SELECT status, revoked_cause FROM blueprint_approvals WHERE path = '.paintedwolf/blueprints/child.md'`).Scan(&status, &cause))
	if status != "revoked" || cause != db.BlueprintGrantCauseSessionDeleted {
		t.Fatalf("grant sealed by the deleted tree = %q/%q", status, cause)
	}
	testutil.FailErr(t, "read survivor grant", sqlDB.QueryRowContext(ctx, `
		SELECT status FROM blueprint_approvals WHERE path = '.paintedwolf/blueprints/survivor.md'`).Scan(&status))
	if status != "approved" {
		t.Fatalf("unrelated session's grant = %q, want untouched", status)
	}
	var chainRows int
	testutil.FailErr(t, "count chain", sqlDB.QueryRowContext(ctx, `
		SELECT count(*) FROM authz_events WHERE session_id = 'child'`).Scan(&chainRows))
	if chainRows != 0 {
		t.Fatalf("authz chain rows = %d, want 0", chainRows)
	}
}

// Draft projects use their scratch root as the working directory.
func TestSQLStoreCreateDraftProjectUsesScratchWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	rootID, wantPrefix := testdbseed.InsertDraftScratchRoot(t, sqlDB, testdbseed.DefaultProjectID)
	store := NewSQL(sqlDB)

	sess, err := store.Create(context.Background(), api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session on rootless project", err)

	if sess.WorkspacePath != wantPrefix {
		t.Fatalf("WorkspacePath = %q, want %q", sess.WorkspacePath, wantPrefix)
	}
	if sess.WorkspaceRootID != rootID {
		t.Fatalf("WorkspaceRootID = %q want %q", sess.WorkspaceRootID, rootID)
	}
	if info, statErr := os.Stat(sess.WorkspacePath); statErr != nil || !info.IsDir() {
		t.Fatalf("scratch workspace not created: %v", statErr)
	}

	got, err := store.Get(context.Background(), sess.ID)
	testutil.FailErr(t, "get rootless session", err)
	if !strings.HasPrefix(got.WorkspacePath, wantPrefix) {
		t.Fatalf("hydrated WorkspacePath = %q, want prefix %q", got.WorkspacePath, wantPrefix)
	}
}

func TestSQLStoreRejectsProjectWithoutPrimaryRoot(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO project_roots (
			id, project_id, path, label, is_primary, added_at, kind
		) VALUES ('root', ?, '/tmp/project', 'project', 0, '2026-01-01T00:00:00Z', 'attached')
	`, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert root", err)

	_, err = NewSQL(sqlDB).Create(t.Context(), api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	if !errors.Is(err, errProjectHasNoWorkspaceRoot) {
		t.Fatalf("Create error = %v, want %v", err, errProjectHasNoWorkspaceRoot)
	}
}

func TestSQLTranscriptPageOrdCursors(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	s := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := s.Create(ctx, api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	batch := make([]api.Message, 0, 10)
	for i := 0; i < 10; i++ {
		batch = append(batch, api.Message{Role: api.MessageRoleUser, Content: "m"})
	}
	testutil.FailErr(t, "append", s.AppendMessages(ctx, sess.ID, batch...))

	tail, err := s.GetTranscriptPage(ctx, sess.ID, api.TranscriptPageQuery{Limit: 4})
	testutil.FailErr(t, "tail", err)
	if len(tail.Messages) != 4 || tail.BeforeCursor == "" || tail.AfterCursor != "" {
		t.Fatalf("tail=%d before=%v after=%v", len(tail.Messages), tail.BeforeCursor, tail.AfterCursor)
	}
	before := tail.Messages[0].Ord
	older, err := s.GetTranscriptPage(ctx, sess.ID, api.TranscriptPageQuery{Limit: 4, Before: &before})
	testutil.FailErr(t, "before", err)
	if len(older.Messages) != 4 || older.Messages[len(older.Messages)-1].Ord >= before {
		t.Fatalf("before page newest=%d want < %d (len=%d)", older.Messages[len(older.Messages)-1].Ord, before, len(older.Messages))
	}
	after := older.Messages[len(older.Messages)-1].Ord
	forward, err := s.GetTranscriptPage(ctx, sess.ID, api.TranscriptPageQuery{Limit: 4, After: &after})
	testutil.FailErr(t, "after", err)
	if len(forward.Messages) != 4 || forward.Messages[0].Ord <= after {
		t.Fatalf("after page oldest=%d want > %d", forward.Messages[0].Ord, after)
	}
}

func TestSQLCompactionViewCannotRepublishDeletedBoundary(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	s := NewSQL(sqlDB)
	sess, err := s.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append history", s.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "before", Role: api.MessageRoleUser, Content: "before"},
		api.Message{ID: "boundary", Role: api.MessageRoleAssistant, Content: "boundary"},
	))
	messages, err := s.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "load history", err)
	boundary := messages[len(messages)-1]
	view := CompactionView{
		Generation:        1,
		Messages:          []api.Message{{Role: api.MessageRoleAssistant, Content: "summary"}},
		CoveredThroughOrd: boundary.Ord,
		CoveredThroughID:  boundary.ID,
		SourceSeq:         boundary.Seq,
	}
	testutil.FailErr(t, "put compaction view", s.PutCompactionView(t.Context(), sess.ID, view))
	_, err = s.TruncateMessagesFrom(t.Context(), sess.ID, boundary.ID)
	testutil.FailErr(t, "truncate boundary", err)
	if err := s.PutCompactionView(t.Context(), sess.ID, view); err == nil {
		t.Fatal("stale compaction view republished after its boundary was deleted")
	}
}

func TestSQLEvidenceHandlesSurviveTranscriptReload(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	sess, err := store.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append evidence row", store.AppendMessages(t.Context(), sess.ID, api.Message{
		Role: api.MessageRoleTool, Content: "observed", EvidenceHandles: []string{"read#1"},
	}))

	messages, err := store.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "reload messages", err)
	if len(messages) != 1 || len(messages[0].EvidenceHandles) != 1 || messages[0].EvidenceHandles[0] != "read#1" {
		t.Fatalf("evidence handles = %+v", messages)
	}
}
