package history

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type compactionInterleavedStore struct {
	Store
	beforeSuffix func()
}

func (s *compactionInterleavedStore) ReadMessageWindow(ctx context.Context, id string, window messageview.MessageWindow) ([]api.Message, error) {
	s.beforeSuffix()
	return s.Store.ReadMessageWindow(ctx, id, window)
}

func TestCompactionSuffixCannotMaskConcurrentPrefixEdit(t *testing.T) {
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append prefix", mem.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "original", Role: api.MessageRoleAssistant, Content: "original fact"}))
	rows, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read prefix", err)
	last := rows[len(rows)-1]
	testutil.FailErr(t, "publish initial view", mem.PutCompactionView(t.Context(), sess.ID, store.CompactionView{
		Generation: 1, CoveredThroughOrd: last.Ord, CoveredThroughID: last.ID, SourceSeq: last.Seq,
		Messages: []api.Message{{ID: "summary", Role: api.MessageRoleAssistant, Content: "original fact summarized"}},
	}))
	sess, err = mem.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "read generation", err)
	history := &Service{store: &compactionInterleavedStore{Store: mem, beforeSuffix: func() {
		last.Content = "corrected fact"
		_, err := mem.UpdateMessage(t.Context(), sess.ID, last.ID, last)
		testutil.FailErr(t, "edit covered prefix", err)
		testutil.FailErr(t, "append newer suffix", mem.AppendMessages(t.Context(), sess.ID,
			api.Message{ID: "suffix", Role: api.MessageRoleUser, Content: "continue with the correction"}))
	}}}
	if _, _, _, _, _, err := history.loadCompactionPage(t.Context(), sess); err == nil {
		t.Fatal("accepted stale prefix because the newer suffix advanced the source sequence")
	}
	current, err := mem.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "read final generation", err)
	if current.CompactionGeneration != 1 {
		t.Fatalf("stale snapshot advanced generation to %d", current.CompactionGeneration)
	}
}

func TestPersistedCompactionViewRestoresRetainedReasoning(t *testing.T) {
	database := testdbfixture.Open(t, "reasoning-view.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := store.NewSQL(database)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append continuation", st.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "call", Role: api.MessageRoleAssistant, Content: "read", ToolCalls: []api.ToolCall{{ID: "tool", Name: "read"}}, ModelReasoning: &api.ModelReasoning{ProviderID: "p", Model: "m", Text: "private"}},
		api.Message{ID: "result", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "tool", Tool: "read", Content: "source"}}))
	canonical, err := st.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read canonical continuation", err)
	last := canonical[len(canonical)-1]
	testutil.FailErr(t, "store compacted view", st.PutCompactionView(t.Context(), sess.ID, store.CompactionView{Generation: 1, CoveredThroughOrd: last.Ord, CoveredThroughID: last.ID, SourceSeq: last.Seq, Messages: canonical}))
	st = store.NewSQL(database)
	view, ok, err := st.GetCompactionView(t.Context(), sess.ID)
	testutil.FailErr(t, "reload request view", err)
	if !ok || view.Messages[0].ModelReasoning != nil {
		t.Fatal("view duplicated private replay storage")
	}
	sess, err = st.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "reload generation", err)
	history := &Service{store: st}
	sess.CompactionGeneration = 0
	loaded, err := history.Load(t.Context(), sess)
	testutil.FailErr(t, "load retained canonical replay", err)
	if sess.CompactionGeneration != view.Generation {
		t.Fatal("bounded load did not adopt the published generation")
	}
	out := history.ApplyView(t.Context(), sess, loaded)
	if len(out) != 2 || out[0].ModelReasoning == nil || out[0].ModelReasoning.Text != "private" {
		t.Fatal("request view lost canonical continuation")
	}
}

func TestContextScaledCanonicalPagingAndTokenBounds(t *testing.T) {
	largeCfg := compaction.DefaultCompactionConfig()
	largeCfg.ModelContextWindow = 512 * 600 // maxRows = 600 (> UncompactedLimit = 256)
	largeCfg.HardCeilingTokens = 200000
	largeCfg.TargetTokens = 100000
	svc, mem := newCompactionManager(t, largeCfg)
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	rows := make([]api.Message, 320)
	for i := range rows {
		role := api.MessageRoleUser
		if i%2 == 1 {
			role = api.MessageRoleAssistant
		}
		rows[i] = api.Message{ID: fmt.Sprintf("msg-%d", i), Role: role, Content: "short observation"}
	}
	testutil.FailErr(t, "append 320 rows", mem.AppendMessages(t.Context(), sess.ID, rows...))
	sess, err = mem.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "get session", err)
	loaded, err := svc.Load(t.Context(), sess)
	testutil.FailErr(t, "load 320 rows on large receiver", err)
	if len(loaded) != 320 || loaded[0].Ord != 1 || loaded[len(loaded)-1].Ord != 320 {
		t.Fatalf("expected all 320 rows via paged backward windows, got len=%d firstOrd=%d", len(loaded), loaded[0].Ord)
	}

	tightCfg := compaction.DefaultCompactionConfig()
	tightCfg.ModelContextWindow = 16384
	tightCfg.HardCeilingTokens = 600
	tightCfg.TargetTokens = 400
	svc.SetCompactor(compaction.NewSimpleCompactor(tightCfg, compaction.MockSummarizer{Text: "summary"}))
	bounded, err := svc.Load(t.Context(), sess)
	testutil.FailErr(t, "load token-bounded tail on small receiver", err)
	if len(bounded) >= 320 || len(bounded) == 0 || bounded[0].ID != "host-history-coverage" {
		t.Fatalf("expected token-bounded tail with coverage notice, got len=%d", len(bounded))
	}
}

func TestSmallToLargeReceiverRestoresCanonicalHistory(t *testing.T) {
	smallCfg := compaction.DefaultCompactionConfig()
	smallCfg.Enabled = true
	smallCfg.ModelContextWindow = 4000
	smallCfg.HardCeilingTokens = 2000
	smallCfg.TargetTokens = 900
	smallCfg.KeepRecentMessages = 2
	smallCfg.ChunkTokenThreshold = 100000
	svc, mem := newCompactionManager(t, smallCfg)

	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	block := strings.Repeat("word ", 120) // ~150 tokens each
	var seed []api.Message
	for i := 1; i <= 20; i++ {
		role := api.MessageRoleUser
		if i%2 == 0 {
			role = api.MessageRoleAssistant
		}
		seed = append(seed, api.Message{
			ID:      fmt.Sprintf("m-%d", i),
			Role:    role,
			Content: fmt.Sprintf("canonical message %d: %s", i, block),
		})
	}
	testutil.FailErr(t, "append seed", mem.AppendMessages(t.Context(), sess.ID, seed...))
	_, err = svc.ForceCompact(t.Context(), sess.ID)
	testutil.FailErr(t, "force compact on small receiver", err)

	sess, err = mem.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "get compacted session", err)
	smallView := appliedView(t, svc, mem, sess.ID)
	if len(smallView) >= len(seed) {
		t.Fatalf("expected small receiver view to be compacted, got %d rows", len(smallView))
	}

	// Switch to a medium receiver whose TargetTokens fits a larger suffix (partial restoration).
	midCfg := smallCfg
	midCfg.ModelContextWindow = 32000
	midCfg.HardCeilingTokens = 16000
	midCfg.TargetTokens = 1800
	svc.SetCompactor(compaction.NewSimpleCompactor(midCfg, compaction.MockSummarizer{Text: "summary"}))
	midLoaded, err := svc.Load(t.Context(), sess)
	testutil.FailErr(t, "load on medium receiver", err)
	midView := svc.ApplyView(t.Context(), sess, midLoaded)
	if len(midView) <= len(smallView) || len(midView) >= len(seed) {
		t.Fatalf("expected partial canonical restoration on medium receiver: small=%d mid=%d full=%d", len(smallView), len(midView), len(seed))
	}

	// Switch to a large receiver whose TargetTokens fits all 20 canonical messages (full restoration).
	largeCfg := smallCfg
	largeCfg.ModelContextWindow = 200000
	largeCfg.HardCeilingTokens = 150000
	largeCfg.TargetTokens = 50000
	svc.SetCompactor(compaction.NewSimpleCompactor(largeCfg, compaction.MockSummarizer{Text: "summary"}))
	largeLoaded, err := svc.Load(t.Context(), sess)
	testutil.FailErr(t, "load on large receiver", err)
	largeView := svc.ApplyView(t.Context(), sess, largeLoaded)
	if len(largeView) != len(seed) || largeView[0].ID != "m-1" || largeView[len(largeView)-1].ID != "m-20" {
		t.Fatalf("expected full canonical restoration on large receiver, got len=%d first=%s", len(largeView), largeView[0].ID)
	}
}
