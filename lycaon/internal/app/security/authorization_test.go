package security

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type mcpInventoryFixture struct{}

func (mcpInventoryFixture) EnabledProviderIDs() []string { return []string{"fixture-provider"} }

func TestAuthorizationRuntimeSealsActualToolAndSpawnInventoryAndDetectsDrift(t *testing.T) {
	database := testdbfixture.Open(t, "runtime-authorization.db")
	root := t.TempDir()
	testdbseed.InsertSessionWithRoot(t, database, "chat", testdbseed.DefaultProjectID, root)
	sess, err := store.NewSQL(database).Get(t.Context(), "chat")
	testutil.FailErr(t, "load authorization chat", err)
	runtime := &Runtime{database: database}
	roster := []string{"implementer"}
	runtime.BindSpawnAgents(func(_ context.Context, id string) []string {
		if id != sess.ID {
			t.Fatalf("spawn roster asked for another chat:%q", id)
		}
		return roster
	})
	err = runtime.BuildAuthorization("", []sandbox.ToolProfile{{ID: "fixture", Tools: map[string]bool{"read": true}, DenyTools: []string{"blocked_*"}}}, nil, func() hitl.ApprovalGate { return nil }, func() []tools.ToolMeta {
		return []tools.ToolMeta{{Name: "read"}, {Name: "mcp_fixture_query"}, {Name: "blocked_write"}}
	}, func(context.Context, *api.Session) sandbox.ToolAccess { return sandbox.ToolAccessAll }, func(path string) spawn.WorkerToolBudget {
		if path != root {
			t.Fatalf("worker budget attributed to another root:%q", path)
		}
		return spawn.WorkerToolBudget{Default: 7, Min: 2, Max: 9}
	})
	testutil.FailErr(t, "build runtime authorization", err)
	runtime.BindMCPInventory(mcpInventoryFixture{})
	for range 2 {
		testutil.FailErr(t, "seal unchanged runtime facts", runtime.Authority.Sealer.Seal(t.Context(), sess, "fixture", ""))
	}
	roster = []string{"implementer", "reviewer"}
	testutil.FailErr(t, "seal changed spawn inventory", runtime.Authority.Sealer.Seal(t.Context(), sess, "fixture", ""))
	rows, err := runtime.Authority.Store.ListContexts(t.Context(), sess.ID)
	testutil.FailErr(t, "read runtime authorization chain", err)
	if len(rows) != 2 || rows[0].ConfigHash == rows[1].ConfigHash || rows[1].PrevHash != rows[0].RowHash {
		t.Fatalf("runtime fact drift did not form a single chain:%+v", rows)
	}
	current := rows[1]
	if !slices.Equal(current.AllowedTools, []string{"mcp_fixture_query", "read"}) || !slices.Equal(current.MCPInventory.ProviderIDs, []string{"fixture-provider"}) || !slices.Equal(current.SpawnAllowlist, roster) || current.WorkerToolBudget.Default != 7 || len(current.ChatGrants) != 0 {
		t.Fatalf("sealed runtime facts departed from actual inventory:%+v", current)
	}
	if broken := authzcontext.VerifyContexts(sess.ID, rows); broken != nil {
		t.Fatalf("authorization chain did not verify:%+v", broken)
	}
}
