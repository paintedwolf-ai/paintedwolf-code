package authzcontext_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAssemble_sortsAndHashes(t *testing.T) {
	profile := sandbox.ToolProfile{
		ID:         "implement",
		Tools:      map[string]bool{"write": true, "read": true, "command": true, "mcp_foo_bar": true},
		DenyTools:  []string{"task"},
		MCPDeny:    []string{"mcp_x"},
		ReadGlobs:  []string{"**/*"},
		WriteGlobs: []string{"src/**"},
	}
	c := authzcontext.Assemble(authzcontext.AssembleInput{
		Session: &api.Session{
			ID:              "sess-1",
			ProjectID:       "proj-1",
			AgentType:       "implementer",
			Posture:         api.SessionPostureBuild,
			ParentSessionID: "parent-1",
			MaxToolLoops:    40,
		},
		ProfileID: "implement",
		Profile:   profile,
		Perms: settings.ApprovalConfig{
			Posture: gate.PostureBalanced,
			Grants:  []settings.ApprovalGrant{{ID: "grant-z"}, {ID: "grant-a"}},
			Rules: []settings.ApprovalRule{{
				Category: settings.ApprovalCategoryCommand,
				Pattern:  "rm *",
				Effect:   settings.ApprovalEffectAsk,
			}},
		},
		WorkerJobID: "job-1",
		MCPInventory: authzcontext.MCPInventory{
			ProviderIDs: []string{"foo"},
		},
		SpawnAllowlist:   []string{"implementer", "repo-researcher"},
		WorkerToolBudget: spawn.DefaultWorkerToolBudget(),
	})
	if c.SessionID != "sess-1" || c.ToolProfile != "implement" {
		t.Fatalf("identity = %+v", c)
	}
	if len(c.MCPInventory.Tools) != 0 {
		t.Fatalf("Assemble does not merge profile MCP tools; got %v", c.MCPInventory.Tools)
	}
	tools := authzcontext.MCPToolsFromProfile(profile)
	if len(tools) != 1 || tools[0] != "mcp_foo_bar" {
		t.Fatalf("MCPToolsFromProfile = %v", tools)
	}
	if c.ConfigHash == "" {
		t.Fatal("ConfigHash empty")
	}
	if len(c.Grants) != 2 || c.Grants[0].ID != "grant-a" || c.Grants[1].ID != "grant-z" {
		t.Fatalf("grant snapshot = %+v", c.Grants)
	}
	again := authzcontext.Assemble(authzcontext.AssembleInput{
		Session:   &api.Session{ID: "other", ProjectID: "proj-1", AgentType: "implementer", Posture: api.SessionPostureBuild, MaxToolLoops: 40},
		ProfileID: "implement",
		Profile: sandbox.ToolProfile{
			ID: "implement", Tools: map[string]bool{"write": true, "read": true, "command": true},
			DenyTools: []string{"task"}, MCPDeny: []string{"mcp_x"},
			ReadGlobs: []string{"**/*"}, WriteGlobs: []string{"src/**"},
		},
		Perms: settings.ApprovalConfig{
			Posture: gate.PostureBalanced,
			Rules: []settings.ApprovalRule{{
				Category: settings.ApprovalCategoryCommand,
				Pattern:  "rm *",
				Effect:   settings.ApprovalEffectAsk,
			}},
		},
		SpawnAllowlist:   []string{"implementer", "repo-researcher"},
		WorkerToolBudget: spawn.DefaultWorkerToolBudget(),
	})
	if again.ConfigHash == c.ConfigHash {
		t.Fatal("expected distinct config_hash for different session id")
	}
}

func TestSeal_idempotentAndDrift(t *testing.T) {
	mem := authzcontext.NewMemoryStore()
	sealer := &authzcontext.Sealer{Store: mem}
	sess := &api.Session{ID: "sess-a", ProjectID: "p1", AgentType: "implementer", Posture: api.SessionPostureBuild}
	if err := sealer.Seal(context.Background(), sess, "implement", ""); err != nil {
		testutil.FailErr(t, "first seal", err)
	}
	if err := sealer.Seal(context.Background(), sess, "implement", ""); err != nil {
		testutil.FailErr(t, "second seal same hash", err)
	}
	rows, err := mem.ListContexts(context.Background(), "sess-a")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d err = %v", len(rows), err)
	}
	sess.MaxToolLoops = 99
	if err := sealer.Seal(context.Background(), sess, "implement", ""); err != nil {
		testutil.FailErr(t, "drift seal", err)
	}
	rows, err = mem.ListContexts(context.Background(), "sess-a")
	if err != nil || len(rows) != 2 {
		t.Fatalf("after drift rows = %d err = %v", len(rows), err)
	}
	if rows[1].PrevHash != rows[0].RowHash {
		t.Fatalf("chain link broken: prev=%q want=%q", rows[1].PrevHash, rows[0].RowHash)
	}
	if br := authzcontext.VerifyContexts("sess-a", rows); br != nil {
		t.Fatalf("verify: %+v", br)
	}
}

func TestSealOpenWorldSnapshotsRegisteredTools(t *testing.T) {
	mem := authzcontext.NewMemoryStore()
	sealer := &authzcontext.Sealer{
		Store: mem,
		Profiles: map[string]sandbox.ToolProfile{
			"implement": {ID: "implement", Tools: map[string]bool{"read": true}, DenyTools: []string{"blocked_*"}},
		},
		ToolAccess:      func(context.Context, *api.Session) sandbox.ToolAccess { return sandbox.ToolAccessAll },
		RegisteredTools: func() []string { return []string{"read", "future_tool", "mcp_github_list", "blocked_tool"} },
	}
	sess := &api.Session{ID: "sess-open", AgentType: "implementer"}
	if err := sealer.Seal(context.Background(), sess, "implement", ""); err != nil {
		t.Fatalf("seal open-world context: %v", err)
	}
	rows, err := mem.ListContexts(context.Background(), sess.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("contexts = %d err = %v", len(rows), err)
	}
	if got := rows[0].AllowedTools; len(got) != 3 || got[0] != "future_tool" || got[1] != "mcp_github_list" || got[2] != "read" {
		t.Fatalf("allowed tools = %v", got)
	}
	if got := rows[0].MCPInventory.Tools; len(got) != 1 || got[0] != "mcp_github_list" {
		t.Fatalf("MCP tools = %v", got)
	}
}

func TestSeal_failClosed(t *testing.T) {
	sealer := &authzcontext.Sealer{Store: authzcontext.FailStore{}}
	sess := &api.Session{ID: "sess-fail"}
	err := sealer.Seal(context.Background(), sess, "implement", "")
	if err == nil || !errors.Is(err, authzledger.ErrSealFailed) {
		t.Fatalf("want ErrSealFailed, got %v", err)
	}
}

func TestSQLStore_chainAndTamper(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))

	store := authzcontext.NewSQLStore(sqlDB)
	insertSession(t, sqlDB, "sess-sql", time.Now().UTC())
	c := authzcontext.Assemble(authzcontext.AssembleInput{
		Session:   &api.Session{ID: "sess-sql", ProjectID: "p1"},
		ProfileID: "implement",
		Profile:   sandbox.ToolProfile{ID: "implement", Tools: map[string]bool{"read": true}},
		Perms:     settings.ApprovalConfig{Posture: gate.PostureBalanced},
	})
	if _, err := store.AppendContext(context.Background(), c); err != nil {
		testutil.FailErr(t, "append", err)
	}
	br, err := authzcontext.Verify(context.Background(), sqlDB)
	if err != nil || br != nil {
		t.Fatalf("verify before tamper br=%+v err=%v", br, err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), `DROP TRIGGER authorization_contexts_immutable`); err != nil {
		testutil.FailErr(t, "drop trigger", err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), `UPDATE authorization_contexts SET row_hash = '00' WHERE session_id = ?`, "sess-sql"); err != nil {
		testutil.FailErr(t, "tamper", err)
	}
	br, err = authzcontext.Verify(context.Background(), sqlDB)
	if err != nil {
		testutil.FailErr(t, "verify after tamper", err)
	}
	if br == nil {
		t.Fatal("expected verify break after tamper")
	}
}

func TestAuthorizationContextsFollowSessionDelete(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))

	store := authzcontext.NewSQLStore(sqlDB)
	insertSession(t, sqlDB, "sess-del", time.Now().UTC())
	c := authzcontext.Assemble(authzcontext.AssembleInput{
		Session:   &api.Session{ID: "sess-del", ProjectID: "p1"},
		ProfileID: "implement",
		Profile:   sandbox.ToolProfile{ID: "implement", Tools: map[string]bool{"read": true}},
	})
	if _, err := store.AppendContext(context.Background(), c); err != nil {
		testutil.FailErr(t, "append", err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), `DELETE FROM sessions WHERE id = ?`, "sess-del"); err != nil {
		testutil.FailErr(t, "delete session", err)
	}
	got, err := store.LatestContext(context.Background(), "sess-del")
	if err != nil || got != nil {
		t.Fatalf("authorization_contexts must be removed with their session: context=%+v err=%v", got, err)
	}
}
