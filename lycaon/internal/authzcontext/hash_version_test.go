package authzcontext

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVerifyRejectsUnsupportedStoredHashVersions(t *testing.T) {
	for _, version := range []int{HashVersion1, HashVersion2} {
		contextRow := Context{SessionID: "session", ContextSeq: 1, HashVersion: version}
		var err error
		contextRow.RowHash, err = ComputeRowHash(version, "session", 1, "", chainPayloadFromContext(contextRow), "")
		testutil.FailErr(t, "hash context", err)
		if broken := VerifyContexts("session", []Context{contextRow}); broken != nil {
			t.Fatalf("valid version %d context rejected: %+v", version, broken)
		}
	}
	eventRow := Event{SessionID: "session", EventSeq: 1, HashVersion: HashVersion1}
	var err error
	eventRow.RowHash, err = ComputeEventRowHash(HashVersion1, "session", 1, "", eventChainPayloadFromEvent(eventRow), "")
	testutil.FailErr(t, "hash event", err)
	if broken := VerifyEvents("session", []Event{eventRow}); broken != nil {
		t.Fatalf("valid event rejected: %+v", broken)
	}

	contextRow := Context{SessionID: "session", ContextSeq: 1}
	for _, version := range []int{0, -1, HashVersion2 + 1} {
		contextRow.HashVersion = version
		if broken := VerifyContexts("session", []Context{contextRow}); broken == nil || broken.Reason != "unsupported hash_version" {
			t.Fatalf("context version %d: %+v", version, broken)
		}
	}
	for _, version := range []int{0, -1, HashVersion2} {
		eventRow.HashVersion = version
		if broken := VerifyEvents("session", []Event{eventRow}); broken == nil || broken.Reason != "unsupported hash_version" {
			t.Fatalf("event version %d: %+v", version, broken)
		}
	}
}

// Version 1 digests cover the worker ceiling those rows recorded; version 2
// digests do not, so a stray ceiling cannot change them.
func TestContextDigestCoversCeilingOnlyAtVersion1(t *testing.T) {
	ceiling := &V1Ceiling{MaxToolLoops: 40, Default: 30, Min: 2, Max: 120}
	hash := func(c Context) string {
		t.Helper()
		h, err := ComputeRowHash(c.HashVersion, "session", 1, "", chainPayloadFromContext(c), "")
		testutil.FailErr(t, "hash context", err)
		return h
	}
	v1 := Context{SessionID: "session", ContextSeq: 1, HashVersion: HashVersion1, V1Ceiling: ceiling}
	v1Other := v1
	v1Other.V1Ceiling = &V1Ceiling{MaxToolLoops: 41, Default: 30, Min: 2, Max: 120}
	if hash(v1) == hash(v1Other) {
		t.Fatal("version 1 digest ignores the recorded ceiling")
	}
	v2 := Context{SessionID: "session", ContextSeq: 1, HashVersion: HashVersion2}
	v2Ceiling := v2
	v2Ceiling.V1Ceiling = ceiling
	if hash(v2) != hash(v2Ceiling) {
		t.Fatal("version 2 digest depends on a ceiling")
	}
}

// A chain sealed by an earlier release at version 1 still verifies after new
// contexts append at version 2, and its recorded ceiling stays protected.
func TestSQLChainVerifiesVersion1RowsBeforeVersion2Rows(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(t.TempDir(), "store.db"))
	now := time.Now().UTC()
	stamp := db.FormatTime(now)
	_, err := sqlDB.ExecContext(ctx, `INSERT OR IGNORE INTO projects (id, last_opened_at, created_at) VALUES ('proj', ?, ?)`, stamp, stamp)
	testutil.FailErr(t, "insert project", err)
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO sessions (id, project_id, owner_person_id, posture, created_at, activity_at, updated_at) VALUES ('sess', 'proj', (SELECT id FROM people WHERE role = 'owner'), 'build', ?, ?, ?)`,
		stamp, stamp, stamp)
	testutil.FailErr(t, "insert session", err)

	legacy := Context{
		SessionID: "sess", ContextSeq: 1, HashVersion: HashVersion1, SealedAt: now,
		ProjectID: "proj", AgentType: "implementer", ToolProfile: "implement",
		V1Ceiling: &V1Ceiling{MaxToolLoops: 40, Default: 30, Min: 2, Max: 120},
	}
	legacy.RowHash, err = ComputeRowHash(HashVersion1, "sess", 1, stamp, chainPayloadFromContext(legacy), "")
	testutil.FailErr(t, "hash legacy row", err)
	cols, err := marshalContextColumns(legacy)
	testutil.FailErr(t, "marshal legacy columns", err)
	_, err = sqlDB.ExecContext(ctx, `
		INSERT INTO authorization_contexts (
			id, session_id, context_seq, prev_hash, row_hash, hash_version, ts,
			project_id, agent_type, tool_profile,
			allowed_tools_json, deny_tools_json, mcp_deny_json,
			read_globs_json, write_globs_json, ask_rules_json,
			grants_json, chat_grants_json,
			max_tool_loops, mcp_inventory_json, spawn_allowlist_json, worker_tool_budget_json
		) VALUES ('legacy', 'sess', 1, '', ?, 1, ?, 'proj', 'implementer', 'implement', ?, ?, ?, ?, ?, ?, ?, ?, 40, ?, ?, '{"default":30,"min":2,"max":120}')`,
		legacy.RowHash, stamp,
		cols.allowed, cols.deny, cols.mcpDeny, cols.readGlobs, cols.writeGlobs, cols.rules,
		cols.grants, cols.chatGrants, cols.mcpInventory, cols.spawnAllowlist)
	testutil.FailErr(t, "insert legacy row", err)

	store := NewSQLStore(sqlDB)
	_, err = store.AppendContext(ctx, Context{ID: "current", SessionID: "sess", ProjectID: "proj", AgentType: "implementer", ToolProfile: "implement", ConfigHash: "next"})
	testutil.FailErr(t, "append current row", err)
	rows, err := store.ListContexts(ctx, "sess")
	testutil.FailErr(t, "list chain", err)
	if len(rows) != 2 || rows[0].HashVersion != HashVersion1 || rows[1].HashVersion != HashVersion2 || rows[1].PrevHash != rows[0].RowHash {
		t.Fatalf("chain = %+v", rows)
	}
	if broken, err := Verify(ctx, sqlDB); err != nil || broken != nil {
		t.Fatalf("mixed chain verify = %+v err=%v", broken, err)
	}

	_, err = sqlDB.ExecContext(ctx, `DROP TRIGGER authorization_contexts_immutable`)
	testutil.FailErr(t, "drop trigger", err)
	_, err = sqlDB.ExecContext(ctx, `UPDATE authorization_contexts SET max_tool_loops = 41 WHERE id = 'legacy'`)
	testutil.FailErr(t, "tamper legacy ceiling", err)
	if broken, err := Verify(ctx, sqlDB); err != nil || broken == nil || broken.ContextSeq != 1 {
		t.Fatalf("tampered legacy ceiling verify = %+v err=%v", broken, err)
	}
}
