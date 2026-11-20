package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools/toolstest"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/pkg/api"
)

// Compaction and abort reshape what the model sees; the stored transcript
// keeps every message row.
func TestSessionCompactionAndAbortKeepEveryMessageRow(t *testing.T) {
	ctx := t.Context()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	database := testdbfixture.Open(t, "lifecycle.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := store.NewSQL(database)
	mockConfig, err := llm.LoadMockConfig()
	testutil.FailErr(t, "load mock config", err)
	host := session.NewHost(st, session.Models{Client: llm.NewMockProvider(mockConfig), Limits: settings.DefaultSessionLimits()}, toolstest.NewRegistry())
	host.SetDataDir(t.TempDir())
	oartest.InstallCloseoutPolicy(t, host)
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 100000
	cfg.TargetTokens = 100
	cfg.ChunkTokenThreshold = 50
	cfg.ChunkStrategyDefault = "truncate"
	cfg.ChunkMinSavingsTokens = 100
	cfg.ChunkTargetTokens = 30
	host.Runner.History.SetCompactor(compaction.NewSimpleCompactor(cfg, compaction.MockSummarizer{Text: "summary"}))

	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append transcript", st.AppendMessages(ctx, sess.ID,
		api.Message{ID: "detail", Role: api.MessageRoleAssistant, Content: strings.Repeat("x\n", 4000)},
		api.Message{ID: "request", Role: api.MessageRoleUser, Content: "continue"},
	))
	before := storedMessageIDs(t, ctx, database, sess.ID)
	if len(before) != 2 {
		t.Fatalf("stored message rows = %v, want the two appended", before)
	}

	report, err := host.Runner.History.ForceCompact(ctx, sess.ID)
	testutil.FailErr(t, "force compaction", err)
	if report.ChunksCompacted == 0 && report.TokensAfter >= report.TokensBefore {
		t.Fatalf("compaction changed nothing: %+v", report)
	}
	requireMessageRowsKept(t, ctx, database, sess.ID, before, "compaction")

	testutil.FailErr(t, "mark busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
	testutil.FailErr(t, "abort", host.Stops.Abort(ctx, sess.ID, ""))
	got, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read session", err)
	if got.Status != api.SessionStatusIdle {
		t.Fatalf("status after abort = %q, want idle", got.Status)
	}
	requireMessageRowsKept(t, ctx, database, sess.ID, before, "abort")
}

func storedMessageIDs(t *testing.T, ctx context.Context, database *db.Store, sessionID string) map[string]bool {
	t.Helper()
	rows, err := database.QueryContext(ctx, `SELECT id FROM messages WHERE session_id = ?`, sessionID)
	testutil.FailErr(t, "query message rows", err)
	defer func() { _ = rows.Close() }()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		testutil.FailErr(t, "scan message row", rows.Scan(&id))
		ids[id] = true
	}
	testutil.FailErr(t, "iterate message rows", rows.Err())
	return ids
}

func requireMessageRowsKept(t *testing.T, ctx context.Context, database *db.Store, sessionID string, before map[string]bool, step string) {
	t.Helper()
	after := storedMessageIDs(t, ctx, database, sessionID)
	for id := range before {
		if !after[id] {
			t.Errorf("%s removed message row %s", step, id)
		}
	}
}
