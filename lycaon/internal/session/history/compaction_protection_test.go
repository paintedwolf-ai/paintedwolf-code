package history

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerCharterSurvivesBackgroundAndForcedCompaction(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(map[bool]string{false: "forced", true: "background"}[background], func(t *testing.T) {
			cfg := compaction.DefaultCompactionConfig()
			cfg.KeepRecentMessages = 1
			cfg.ChunkTokenThreshold = 100
			cfg.ChunkTargetTokens = 40
			cfg.ChunkMinSavingsTokens = 20
			cfg.ChunkStrategyDefault = "truncate"
			cfg.TargetTokens = 100
			mgr, st := newCompactionManager(t, cfg)
			sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create worker", err)
			testutil.FailErr(t, "set worker identity", st.UpdateSession(t.Context(), sess.ID, func(s *api.Session) { s.ParentSessionID = "parent" }))
			preamble := guidance.MarkerWorkerTaskPreamble + strings.Repeat("Assigned worker role.\n", 500)
			charter := guidance.MarkerWorkerTaskAssignment + strings.Repeat("Preserve the assigned scope.\n", 500)
			testutil.FailErr(t, "append worker history", st.AppendMessages(t.Context(), sess.ID,
				api.Message{ID: "preamble", Role: api.MessageRoleUser, Content: preamble},
				api.Message{ID: "charter", Role: api.MessageRoleUser, Content: charter},
				api.Message{ID: "evidence", Role: api.MessageRoleAssistant, Content: strings.Repeat("Prior evidence\n", 1000)},
				api.Message{ID: "recent", Role: api.MessageRoleAssistant, Content: "Continue"}))
			if background {
				mgr.runBackgroundCompactionPage(t.Context(), sess.ID, mgr.Compactor.(*compaction.SimpleCompactor))
			} else {
				_, err = mgr.ForceCompact(t.Context(), sess.ID)
				testutil.FailErr(t, "compact worker", err)
			}
			view := appliedView(t, mgr, st, sess.ID)
			protected := map[string]string{"preamble": preamble, "charter": charter}
			var reduced bool
			for _, msg := range view {
				if expected, ok := protected[msg.ID]; ok {
					if msg.Content != expected || msg.CompactedChunk != nil {
						t.Fatalf("worker instruction %s was summarized", msg.ID)
					}
					delete(protected, msg.ID)
				}
				if msg.ID == "evidence" && msg.CompactedChunk != nil {
					reduced = true
				}
			}
			if len(protected) != 0 || !reduced {
				t.Fatalf("protected=%v evidence reduced=%v", protected, reduced)
			}

		})
	}
}

func TestForceCompactDoesNotPublishUnchangedGeneration(t *testing.T) {
	mgr, st := newCompactionManager(t, compaction.DefaultCompactionConfig())
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append short history", st.AppendMessages(t.Context(), sess.ID, api.Message{ID: "short", Role: api.MessageRoleUser, Content: "Hello"}))
	_, err = mgr.ForceCompact(t.Context(), sess.ID)
	testutil.FailErr(t, "compact short history", err)
	got, err := st.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "read generation", err)
	if got.CompactionGeneration != 0 {
		t.Fatal("unchanged history advanced generation")
	}
}

func TestForceCompactReportsStoredGenerationForChunksAndNoop(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.TargetTokens = 200
	cfg.KeepRecentMessages = 1
	cfg.ChunkTokenThreshold = 300
	cfg.ChunkMinSavingsTokens = 100
	cfg.ChunkTargetTokens = 50
	cfg.ChunkStrategyDefault = "truncate"
	mgr, st := newCompactionManager(t, cfg)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append history", st.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "old", Role: api.MessageRoleAssistant, Content: strings.Repeat("Earlier observations.\n", 3000)},
		api.Message{ID: "request", Role: api.MessageRoleUser, Content: "Continue."}))
	for attempt := range 2 {
		report, err := mgr.ForceCompact(t.Context(), sess.ID)
		testutil.FailErr(t, "force compact", err)
		current, err := st.Get(t.Context(), sess.ID)
		testutil.FailErr(t, "stored generation", err)
		if report.CompactionGeneration != current.CompactionGeneration || current.CompactionGeneration != 1 {
			t.Fatalf("attempt %d: report=%+v stored=%d", attempt, report, current.CompactionGeneration)
		}
		if attempt == 0 && (report.ChunksCompacted != 1 || report.SessionCompacted) {
			t.Fatalf("expected chunk-only report: %+v", report)
		}
	}
}
