package history

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSynthesisCanPublishWithoutAnAcceptedBaseline(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.TargetTokens, cfg.KeepRecentMessages, cfg.SessionMinSavingsTokens = 10000, 1, 1
	cfg.ChunkTokenThreshold = 1_000_000
	svc, mem := newCompactionManager(t, cfg)
	defer func() {
		svc.Runner.Stop()
		testutil.FailErr(t, "drain synthesis", svc.Runner.WaitContext(t.Context()))
	}()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	prior := &messageview.Continuation{Format: 1, Window: cfg.ModelContextWindow, ThroughOrd: 1, SummaryThroughOrd: 1}
	for range 8 {
		prior.Summary.Facts = append(prior.Summary.Facts, strings.Repeat("earlier clue ", 12))
	}
	text, err := compaction.RenderContinuation(t.Context(), prior, false, 0)
	testutil.FailErr(t, "render previous continuation", err)
	testutil.FailErr(t, "append captured page", mem.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "previous", Role: "assistant", Content: text, ContentParts: []api.MessageContentPart{{Source: "compaction_summary", Content: text}}},
		api.Message{ID: "observation", Role: "assistant", Content: strings.Repeat("new clue ", 12)},
		api.Message{ID: "request", Role: "user", Content: "Continue.", ContextPinned: true}))
	rows, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read captured page", err)
	info := compaction.SessionInfo{ID: sess.ID, PriorContinuation: prior, Attempts: mem, LatestUserRequestID: "request"}
	prepared := compaction.ContextMessagesFromAPI(rows)
	sc := svc.Compactor.(*compaction.SimpleCompactor)
	_, baseline, err := sc.Baseline().Compact(t.Context(), info, prepared, 1)
	testutil.FailErr(t, "evaluate deterministic baseline", err)
	if baseline.SessionCompacted || baseline.Reason != "insufficient_savings" {
		t.Fatalf("fixture must require synthesis: %+v", baseline)
	}
	last := rows[len(rows)-1]
	snapshot := store.CompactionView{CoveredThroughOrd: last.Ord, CoveredThroughID: last.ID, SourceSeq: last.Seq}
	svc.scheduleProjectionSynthesis(t.Context(), sc, info, rows, prepared, baseline, snapshot)
	testutil.FailErr(t, "wait for optional synthesis", svc.Runner.Synthesis.WaitContext(t.Context()))
	view, ok, err := mem.GetCompactionView(t.Context(), sess.ID)
	testutil.FailErr(t, "read synthesized projection", err)
	if !ok || view.Continuation == nil || view.Continuation.SummaryThroughOrd < rows[1].Ord || view.TokensAfter >= view.TokensBefore {
		t.Fatalf("synthesis did not publish a useful captured page: %+v", view)
	}
}
