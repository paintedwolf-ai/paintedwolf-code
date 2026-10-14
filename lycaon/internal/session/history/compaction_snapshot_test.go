package history

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSynthesisKeepsRecoveryAddressesOutsideItsCapturedPage(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	record := &messageview.Continuation{Format: 1, ThroughOrd: 11, SummaryThroughOrd: 1,
		Sources: []messageview.ContinuationSource{
			{MessageID: "original-result", Ord: 1, Tool: "read", Handles: []string{"read#1"}, Disposition: "summary_and_reference"},
			{MessageID: "new-observation", Ord: 11, Disposition: "reference"},
		}}
	sc := compaction.NewSimpleCompactor(compaction.DefaultCompactionConfig(), compaction.MockSummarizer{Text: "New observation"})
	enriched, err := sc.EnrichContinuation(t.Context(), compaction.SessionInfo{PriorContinuation: record},
		[]api.Message{{ID: "new-observation", Ord: 11, Role: api.MessageRoleAssistant, Content: "New observation"}}, record)
	testutil.FailErr(t, "enrich captured page", err)
	if len(enriched.Sources) != 2 || enriched.Sources[0].MessageID != "original-result" || len(enriched.Sources[0].Handles) != 1 || enriched.SummaryThroughOrd != 11 {
		t.Fatal("summary enrichment displaced an earlier canonical recovery address")
	}
}

func TestDelayedSynthesisPreservesNewerCoverage(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	for _, newerSummary := range []bool{false, true} {
		t.Run(fmt.Sprint(newerSummary), func(t *testing.T) {
			mem := store.NewMemory()
			sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			testutil.FailErr(t, "append sources", mem.AppendMessages(t.Context(), sess.ID,
				api.Message{ID: "one", Role: api.MessageRoleTool, Content: "first observation"},
				api.Message{ID: "two", Role: api.MessageRoleTool, Content: "second observation"}))
			rows, err := mem.GetMessages(t.Context(), sess.ID)
			testutil.FailErr(t, "read sources", err)
			snapshot := store.CompactionView{CoveredThroughOrd: rows[0].Ord, CoveredThroughID: rows[0].ID, SourceSeq: rows[0].Seq}
			record := &messageview.Continuation{Format: messageview.ContinuationFormat, ThroughOrd: rows[1].Ord, Window: 8192,
				Sources: []messageview.ContinuationSource{{MessageID: "two", Ord: rows[1].Ord, Disposition: "reference"}}}
			if newerSummary {
				record.SummaryThroughOrd = rows[1].Ord
				record.Summary.Facts = []string{"newer accepted clue"}
			}
			testutil.FailErr(t, "publish newer baseline", mem.PutCompactionView(t.Context(), sess.ID, store.CompactionView{
				Generation: 1, CoveredThroughOrd: rows[1].Ord, CoveredThroughID: rows[1].ID, SourceSeq: rows[1].Seq, Continuation: record,
				Messages: []api.Message{{ID: "summary", Role: api.MessageRoleAssistant, ContentParts: []api.MessageContentPart{{Source: "compaction_summary"}}}},
			}))
			enriched := &messageview.Continuation{ThroughOrd: rows[0].Ord, SummaryThroughOrd: rows[0].Ord,
				Summary: messageview.ContinuationSummary{Facts: []string{"delayed clue"}}}
			history := &Service{store: mem}
			testutil.FailErr(t, "publish delayed synthesis", history.publishSynthesis(t.Context(), compaction.SessionInfo{ID: sess.ID}, snapshot, enriched))
			view, _, err := mem.GetCompactionView(t.Context(), sess.ID)
			testutil.FailErr(t, "read enriched view", err)
			if view.CoveredThroughOrd != rows[1].Ord || view.Continuation.Sources[0].MessageID != "two" {
				t.Fatal("delayed synthesis replaced newer recovery coordinates")
			}
			want, generation := "delayed clue", 2
			if newerSummary {
				want, generation = "newer accepted clue", 1
			}
			if view.Generation != generation || view.Continuation.Summary.Facts[0] != want {
				t.Fatalf("accepted summary = %+v, generation = %d", view.Continuation, view.Generation)
			}
		})
	}
}

func TestDelayedSynthesisRejectsChangedCanonicalSource(t *testing.T) {
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append source", mem.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "source", Role: api.MessageRoleAssistant, Content: "original fact"}))
	rows, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read source", err)
	source := rows[0]
	snapshot := store.CompactionView{Generation: 1, CoveredThroughOrd: source.Ord,
		CoveredThroughID: source.ID, SourceSeq: source.Seq,
		Continuation: &messageview.Continuation{ThroughOrd: source.Ord}}
	testutil.FailErr(t, "publish baseline", mem.PutCompactionView(t.Context(), sess.ID, snapshot))
	source.Content = "corrected fact"
	_, err = mem.UpdateMessage(t.Context(), sess.ID, source.ID, source)
	testutil.FailErr(t, "correct source", err)
	enriched := &messageview.Continuation{ThroughOrd: source.Ord, SummaryThroughOrd: source.Ord,
		Summary: messageview.ContinuationSummary{Facts: []string{"original fact"}}}
	testutil.FailErr(t, "reject stale synthesis", (&Service{store: mem}).publishSynthesis(t.Context(),
		compaction.SessionInfo{ID: sess.ID}, snapshot, enriched))
	current, err := mem.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "read generation", err)
	if current.CompactionGeneration != 1 {
		t.Fatal("stale synthesis published after canonical correction")
	}
	canonical, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read correction", err)
	if canonical[0].Content != "corrected fact" {
		t.Fatal("synthesis replaced the canonical correction")
	}
}

type compactionInterleavedStore struct {
	Store
	beforeSuffix func()
}

func (s *compactionInterleavedStore) GetMessagesAfterOrd(ctx context.Context, id string, after int64, limit int) ([]api.Message, error) {
	s.beforeSuffix()
	return s.Store.GetMessagesAfterOrd(ctx, id, after, limit)
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
	out := (&Service{store: st}).ApplyView(t.Context(), sess, canonical)
	if len(out) != 2 || out[0].ModelReasoning == nil || out[0].ModelReasoning.Text != "private" {
		t.Fatal("request view lost canonical continuation")
	}
}
