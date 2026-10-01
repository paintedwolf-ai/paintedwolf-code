package session

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompactionUsesObservedTokenOverhead(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.ModelContextWindow = 100000
	cfg.BudgetTriggerPct = 70
	cfg.HardCeilingTokens = 85000
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	block := strings.Repeat("tok ", 16000)
	transcript := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI([]api.Message{{Content: block}}))
	if transcript >= cfg.HardCeilingTokens {
		t.Fatalf("transcript = %d must stay below hard ceiling %d for fixture", transcript, cfg.HardCeilingTokens)
	}
	overhead := cfg.HardCeilingTokens - transcript + 1000
	mgr.RecordCompactionTokenObservation(sess.ID, transcript+overhead, transcript)

	got := mgr.compactionBudgetTokens(sess.ID, transcript)
	if got < cfg.HardCeilingTokens {
		t.Fatalf("corrected budget = %d want >= hard ceiling %d", got, cfg.HardCeilingTokens)
	}
	if compaction.ShouldCompactSession(got, cfg.ModelContextWindow, cfg) {
		return
	}
	t.Fatalf("corrected budget %d should cross compaction trigger/ceiling", got)
}

// Cold-start estimates include the system prompt and tool schemas.
func TestCompactionColdStartReservesStaticStack(t *testing.T) {
	mgr, store := newCompactionManager(t, compaction.DefaultCompactionConfig())
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	block := strings.Repeat("x ", 1000)
	transcript := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI([]api.Message{{Content: block}}))
	got := mgr.compactionBudgetTokens(sess.ID, transcript)
	if got <= transcript {
		t.Fatalf("cold-start budget = %d, want above the transcript estimate %d", got, transcript)
	}
	want := transcript + compaction.ResolveColdStartOverhead("")
	if got != want {
		t.Fatalf("cold-start budget = %d want %d", got, want)
	}

	// Once an observation exists the ratio supersedes the flat reserve, and it
	// tracks growth instead of staying constant.
	mgr.RecordCompactionTokenObservation(sess.ID, transcript*2, transcript)
	if scaled := mgr.compactionBudgetTokens(sess.ID, transcript); scaled != transcript*2 {
		t.Fatalf("calibrated budget = %d want %d", scaled, transcript*2)
	}
	if grown := mgr.compactionBudgetTokens(sess.ID, transcript*2); grown != transcript*4 {
		t.Fatalf("grown calibrated budget = %d want %d (scaled, not additive)", grown, transcript*4)
	}
}

func TestSessionContextUsesCorrectedEstimate(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.ModelContextWindow = 100000
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	block := strings.Repeat("a ", 2000)
	if err := store.AppendMessages(ctx, sess.ID,
		api.Message{ID: "u1", Role: api.MessageRoleUser, Content: block},
	); err != nil {
		t.Fatal(err)
	}
	transcript := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI([]api.Message{{Content: block}}))
	mgr.RecordCompactionTokenObservation(sess.ID, transcript+15000, transcript)

	resp, err := mgr.SessionContext(ctx, sess.ID)
	testutil.FailErr(t, "SessionContext", err)
	want := compaction.PromptTokenCalibration{
		ReportedPromptTokens: transcript + 15000, TranscriptEstimate: transcript,
	}.ProjectBilled(transcript, 15000)
	if resp.EstimatedTokens < want {
		t.Fatalf("EstimatedTokens = %d want >= corrected %d", resp.EstimatedTokens, want)
	}
}

func TestCompactionFitThenReplace(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 50000
	cfg.TargetTokens = 8000
	cfg.ChunkTokenThreshold = 500
	cfg.ChunkMinSavingsTokens = 100
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	log := strings.Repeat("ERROR: build failed line\n", 8000)
	if err := store.AppendMessages(ctx, sess.ID,
		api.Message{ID: "log", Role: api.MessageRoleUser, Content: log},
		api.Message{ID: "ack", Role: api.MessageRoleAssistant, Content: "seen"},
	); err != nil {
		t.Fatal(err)
	}
	transcript := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI([]api.Message{{Content: log}, {Content: "seen"}}))
	mgr.RecordCompactionTokenObservation(sess.ID, transcript+25000, transcript)

	_, err = mgr.Prompt(ctx, sess.ID, "what failed?")
	testutil.FailErr(t, "mgr.Prompt", err)
	mgr.waitForCompaction()

	canonical, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "canonical GetMessages", err)
	if canonical[0].Content != log {
		t.Fatal("canonical history must stay full-fidelity after background pass")
	}

	sess, err = store.Get(ctx, sess.ID)
	testutil.FailErr(t, "store.Get after compaction", err)
	if sess.CompactionGeneration == 0 {
		t.Fatal("expected compaction generation bump after background pass")
	}
	view := appliedView(t, mgr, store, sess.ID)
	viewEst := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(view))
	if viewEst > cfg.TargetTokens {
		t.Fatalf("compacted view estimate = %d want <= target %d", viewEst, cfg.TargetTokens)
	}
}
