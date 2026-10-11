package session

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/history"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/zstdcodec"
	"github.com/lycaon/lycaon/pkg/api"
)

func newCompactionManager(t *testing.T, cfg compaction.CompactionConfig) (*Host, *store.Memory) {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: llm.NewMockProvider(testMockConfig(t)), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetDataDir(t.TempDir())
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(cfg, compaction.MockSummarizer{Text: "Continue from compacted context."}))
	return mgr, store
}

// Overlay the persisted compaction view on canonical messages.
func appliedView(t *testing.T, mgr *Host, store Store, sessID string) []api.Message {
	t.Helper()
	ctx := context.Background()
	sess, err := store.Get(ctx, sessID)
	testutil.FailErr(t, "store.Get", err)
	msgs, err := store.GetMessages(ctx, sessID)
	testutil.FailErr(t, "store.GetMessages", err)
	return mgr.Runner.History.ApplyView(ctx, sess, msgs)
}

func TestHugePasteCompaction(t *testing.T) {
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
	testutil.FailErr(t, "create session in store", err)

	log := strings.Repeat("ERROR: build failed line\n", 8000) // ~200k tokens
	if err := store.AppendMessages(ctx, sess.ID,
		api.Message{ID: "log", Role: api.MessageRoleUser, Content: log},
		api.Message{ID: "ack", Role: api.MessageRoleAssistant, Content: "seen"},
	); err != nil {
		t.Fatal(err)
	}

	_, err = mgr.Submissions.Prompt(ctx, sess.ID, "what failed?")
	testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	mgr.Runner.History.Wait()

	// The transcript stays canonical: GetMessages returns the originals untouched,
	// so Den renders the real text and FTS indexes it.
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	for _, m := range msgs {
		if m.ID == "log" {
			if m.Content != log {
				t.Fatal("canonical log message content was mutated by compaction")
			}
			if m.CompactedChunk != nil {
				t.Fatal("canonical message must not carry compaction metadata")
			}
		}
	}
	// The model-facing view is the compacted one, applied non-destructively.
	view := appliedView(t, mgr, store, sess.ID)
	est := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(view))
	if est > cfg.TargetTokens {
		t.Fatalf("compacted view estimated tokens = %d, want <= %d", est, cfg.TargetTokens)
	}
	compacted := false
	for _, m := range view {
		if m.CompactedChunk != nil {
			compacted = true
		}
	}
	if !compacted {
		t.Fatal("expected at least one compacted chunk in the view")
	}
}

func TestLongSessionCompaction(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 2000
	cfg.ModelContextWindow = 4000
	cfg.BudgetTriggerPct = 50
	cfg.TargetTokens = 1500
	cfg.ChunkTokenThreshold = 10000
	cfg.KeepRecentMessages = 4
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	block := strings.Repeat("word ", 120) // ~150 tokens each
	var seed []api.Message
	for i := 0; i < 40; i++ {
		role := api.MessageRoleUser
		if i%2 == 1 {
			role = api.MessageRoleAssistant
		}
		seed = append(seed, api.Message{
			ID:      fmt.Sprintf("m%d", i),
			Role:    role,
			Content: block,
		})
	}
	if err := store.AppendMessages(ctx, sess.ID, seed...); err != nil {
		testutil.FailErr(t, "store.AppendMessages failed", err)
	}

	_, err = mgr.Submissions.Prompt(ctx, sess.ID, "continue")
	testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	mgr.Runner.History.Wait()

	// Canonical history is not collapsed — the 40 seeded messages remain in the store.
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	if len(msgs) < 40 {
		t.Fatalf("canonical history shrank to %d messages; compaction must not mutate messages", len(msgs))
	}
	view := appliedView(t, mgr, store, sess.ID)
	est := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(view))
	if est > cfg.TargetTokens {
		t.Fatalf("compacted view estimated tokens = %d, want <= %d", est, cfg.TargetTokens)
	}
	sess, err = store.Get(ctx, sess.ID)
	testutil.FailErr(t, "store.Get failed", err)
	if sess.CompactionGeneration == 0 {
		t.Fatal("expected session compaction generation bump")
	}
}

func TestCompactionViewSplicesNewMessages(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 2000
	cfg.ModelContextWindow = 4000
	cfg.BudgetTriggerPct = 50
	cfg.TargetTokens = 1500
	cfg.ChunkTokenThreshold = 10000
	cfg.KeepRecentMessages = 4
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	block := strings.Repeat("word ", 120)
	var seed []api.Message
	for i := 0; i < 40; i++ {
		role := api.MessageRoleUser
		if i%2 == 1 {
			role = api.MessageRoleAssistant
		}
		seed = append(seed, api.Message{ID: fmt.Sprintf("seed%d", i), Role: role, Content: block})
	}
	testutil.FailErr(t, "store.AppendMessages failed", store.AppendMessages(ctx, sess.ID, seed...))

	_, err = mgr.Submissions.Prompt(ctx, sess.ID, "continue")
	testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	mgr.Runner.History.Wait()

	sess, err = store.Get(ctx, sess.ID)
	testutil.FailErr(t, "store.Get failed", err)
	if sess.CompactionGeneration == 0 {
		t.Fatal("expected a compaction view to be written")
	}

	// A message appended after the snapshot must be spliced onto the applied view.
	marker := "post-compaction marker message"
	testutil.FailErr(t, "store.AppendMessages marker", store.AppendMessages(ctx, sess.ID,
		api.Message{ID: "marker", Role: api.MessageRoleUser, Content: marker}))

	view := appliedView(t, mgr, store, sess.ID)
	found := false
	for _, m := range view {
		if m.ID == "marker" && m.Content == marker {
			found = true
		}
	}
	if !found {
		t.Fatal("post-compaction message was not spliced onto the compacted view")
	}
}

func TestPromptHistorySeeksAfterCompactionWatermark(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	oartest.InstallCloseoutPolicy(t, mgr)
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append covered messages", mem.AppendMessages(ctx, sess.ID,
		api.Message{ID: "covered-user", Role: api.MessageRoleUser, Content: "old request"},
		api.Message{ID: "covered-answer", Role: api.MessageRoleAssistant, Content: "old answer"},
	))
	covered, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "load covered messages", err)
	watermark := covered[len(covered)-1].Ord
	testutil.FailErr(t, "put compaction view", mem.PutCompactionView(ctx, sess.ID, store.CompactionView{
		Generation:        1,
		CoveredThroughOrd: watermark,
		CoveredThroughID:  covered[len(covered)-1].ID,
		SourceSeq:         covered[len(covered)-1].Seq,
		Messages: []api.Message{{
			ID: "summary", Role: api.MessageRoleAssistant, Content: "old request summarized",
		}},
	}))
	testutil.FailErr(t, "advance compaction generation", mem.UpdateSession(ctx, sess.ID, func(current *api.Session) {
		current.CompactionGeneration = 1
	}))
	testutil.FailErr(t, "append uncovered message", mem.AppendMessages(ctx, sess.ID,
		api.Message{ID: "new-user", Role: api.MessageRoleUser, Content: "new request"},
	))
	sess, err = mem.Get(ctx, sess.ID)
	testutil.FailErr(t, "reload session", err)

	suffix, err := mgr.Runner.History.Load(ctx, sess)
	testutil.FailErr(t, "load prompt suffix", err)
	if len(suffix) != 1 || suffix[0].ID != "new-user" {
		t.Fatalf("prompt suffix = %+v, want only new-user", suffix)
	}
	assembled := mgr.Runner.History.ApplyView(ctx, sess, suffix)
	if len(assembled) != 2 || assembled[0].ID != "summary" || assembled[1].ID != "new-user" {
		t.Fatalf("assembled history = %+v, want summary plus new-user", assembled)
	}
	covered[0].Content = "edited old request"
	_, err = mem.UpdateMessage(ctx, sess.ID, covered[0].ID, covered[0])
	testutil.FailErr(t, "edit covered message", err)
	canonical, err := mgr.Runner.History.Load(ctx, sess)
	testutil.FailErr(t, "load after covered edit", err)
	if len(canonical) != 3 || canonical[0].Content != "edited old request" {
		t.Fatalf("history after covered edit = %+v, want canonical facts", canonical)
	}
}

type promptHistoryReadProbe struct {
	Store
	fullReads int
}

func (p *promptHistoryReadProbe) GetMessages(context.Context, string) ([]api.Message, error) {
	p.fullReads++
	return nil, fmt.Errorf("unbounded read must not run")
}

func (p *promptHistoryReadProbe) GetMessagesAfterOrd(context.Context, string, int64, int) ([]api.Message, error) {
	return make([]api.Message, history.UncompactedLimit+1), nil
}

func TestPromptHistoryOverflowFailsWithoutUnboundedRead(t *testing.T) {
	mem := store.NewMemory()
	probe := &promptHistoryReadProbe{Store: mem}
	mgr := NewHost(probe, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	oartest.InstallCloseoutPolicy(t, mgr)
	_, err := mgr.Runner.History.Load(t.Context(), &api.Session{ID: "large-session"})
	if err == nil || !strings.Contains(err.Error(), "unprojected rows") {
		t.Fatalf("loadPromptHistory error = %v, want bounded projection lag", err)
	}
	if probe.fullReads != 0 {
		t.Fatalf("full transcript reads = %d, want 0", probe.fullReads)
	}
}

func TestNoCompactionBetweenTargetAndTrigger(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.ModelContextWindow = 10000
	cfg.BudgetTriggerPct = 70 // trigger at 7000 tokens
	cfg.HardCeilingTokens = 7000
	cfg.TargetTokens = 4000 // history will sit between target and trigger
	cfg.ChunkTokenThreshold = 100000
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	block := strings.Repeat("word ", 400) // ~500 tokens each
	var seed []api.Message
	for i := 0; i < 10; i++ { // ~5000 tokens: above target, below trigger
		role := api.MessageRoleUser
		if i%2 == 1 {
			role = api.MessageRoleAssistant
		}
		seed = append(seed, api.Message{ID: fmt.Sprintf("m%d", i), Role: role, Content: block})
	}
	if err := store.AppendMessages(ctx, sess.ID, seed...); err != nil {
		testutil.FailErr(t, "store.AppendMessages failed", err)
	}

	// A ratio of one isolates the compaction trigger from cold-start reserves.
	mgr.Runner.History.ObserveTokens(sess.ID, 1000, 1000)

	_, err = mgr.Submissions.Prompt(ctx, sess.ID, "continue")
	testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	for _, m := range msgs {
		if m.CompactionCheckpoint {
			t.Fatal("history below trigger must not be session-compacted")
		}
		if m.CompactedChunk != nil {
			t.Fatalf("history below trigger must not chunk-compact %s", m.ID)
		}
	}
	sess, err = store.Get(ctx, sess.ID)
	testutil.FailErr(t, "store.Get failed", err)
	if sess.CompactionGeneration != 0 {
		t.Fatal("compaction generation must stay 0 below trigger")
	}
}

func TestWorkerSpawnDoesNotGrowParentMessages(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	parent, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if err := store.AppendMessages(ctx, parent.ID, api.Message{
		ID: "u0", Role: api.MessageRoleUser, Content: "start",
	}); err != nil {
		t.Fatal(err)
	}

	transcript := strings.Repeat("tool output line\n", 2000)
	for i := 0; i < 5; i++ {
		child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
			AgentType: "implement",
			Prompt:    "do work",
		})
		testutil.FailErr(t, "mgr.Workers.SpawnChild failed", err)
		if child.ParentSessionID != parent.ID {
			t.Fatalf("child parent = %q, want %q", child.ParentSessionID, parent.ID)
		}
		if err := store.AppendMessages(ctx, child.ID,
			api.Message{ID: "tool1", Role: api.MessageRoleTool, Content: transcript},
			api.Message{ID: "tool2", Role: api.MessageRoleTool, Content: transcript},
			api.Message{ID: "a1", Role: api.MessageRoleAssistant, Content: "done details " + transcript},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
			Summary: "worker finished task " + string(rune('A'+i)), JobID: "job-" + child.ID,
			ChildSessionID: child.ID, AgentType: "implement",
		}); err != nil {
			testutil.FailErr(t, "mgr.AppendWorkerSummary failed", err)
		}
	}

	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	if len(msgs) > 20 {
		t.Fatalf("parent messages = %d, want <= 20 (summaries only)", len(msgs))
	}
	childMsgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	for _, m := range childMsgs {
		if strings.Contains(m.Content, "tool output line") {
			t.Fatal("parent should not contain child tool transcript")
		}
	}
}

func TestForceCompactHTTP(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 100000
	cfg.TargetTokens = 100
	cfg.ChunkTokenThreshold = 50
	cfg.ChunkStrategyDefault = "truncate"
	cfg.ChunkMinSavingsTokens = 100
	cfg.ChunkTargetTokens = 30
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	content := strings.Repeat("x\n", 4000)
	testutil.FailErr(t, "append historical detail and current request", store.AppendMessages(ctx, sess.ID,
		api.Message{ID: "big", Role: api.MessageRoleUser, Content: content},
		api.Message{ID: "current", Role: api.MessageRoleUser, Content: "Continue the task"},
	))

	report, err := mgr.Runner.History.ForceCompact(ctx, sess.ID)
	testutil.FailErr(t, "mgr.Runner.History.ForceCompact failed", err)
	if report.ChunksCompacted == 0 && report.TokensAfter >= report.TokensBefore {
		t.Fatalf("expected compaction shrink: before=%d after=%d chunks=%d", report.TokensBefore, report.TokensAfter, report.ChunksCompacted)
	}
}

func TestCompactOversizedSplitReadsInSession(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	readA := strings.Repeat("a", 34000)
	readB := strings.Repeat("b", 34000)
	if err := store.AppendMessages(ctx, sess.ID,
		api.Message{ID: "u1", Role: api.MessageRoleUser, Content: "physics feel flat"},
		api.Message{ID: "a1", Role: api.MessageRoleAssistant, Content: "Let me survey the repo."},
		api.Message{ID: "t1", Role: api.MessageRoleTool, Content: `{"entries":[]}`},
		api.Message{ID: "t2", Role: api.MessageRoleTool, Content: readA},
		api.Message{ID: "t3", Role: api.MessageRoleTool, Content: readB},
		api.Message{ID: "a2", Role: api.MessageRoleAssistant, Content: "Now I have what I need."},
	); err != nil {
		t.Fatal(err)
	}

	if err := mgr.Runner.History.ScheduleChunks(ctx, sess); err != nil {
		testutil.FailErr(t, "compactOversizedToolResultsInSession failed", err)
	}
	mgr.Runner.History.Wait()

	// Canonical reads are untouched in the store — full bytes stay searchable.
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	for _, m := range msgs {
		if (m.ID == "t2" && m.Content != readA) || (m.ID == "t3" && m.Content != readB) {
			t.Fatalf("canonical read %s was mutated by compaction", m.ID)
		}
		if (m.ID == "t2" || m.ID == "t3") && m.CompactedChunk != nil {
			t.Fatalf("canonical read %s must not carry compaction metadata", m.ID)
		}
	}
	// The compacted view indexes the oversized reads.
	view := appliedView(t, mgr, store, sess.ID)
	compactedReads := 0
	for _, m := range view {
		if m.ID != "t2" && m.ID != "t3" {
			continue
		}
		if m.CompactedChunk == nil {
			t.Fatalf("read tool %s not compacted in view", m.ID)
		}
		if m.CompactedChunk.Strategy != "spill_index" {
			t.Fatalf("read tool %s strategy=%q want spill_index", m.ID, m.CompactedChunk.Strategy)
		}
		refs := tooloutput.SpillPaths(m.Content)
		if len(refs) != 1 {
			t.Fatalf("read tool %s must retain one exact recovery reference: %v", m.ID, refs)
		}
		stored, err := os.ReadFile(tooloutput.DiskPath(mgr.Workspace.HostDataDir(sess.ProjectID), refs[0]))
		testutil.FailErr(t, "read retained observation", err)
		body, err := zstdcodec.Decompress(stored)
		testutil.FailErr(t, "decode retained observation", err)
		want := readA
		if m.ID == "t3" {
			want = readB
		}
		if string(body) != want {
			t.Fatalf("read tool %s recovery changed original bytes", m.ID)
		}
		compactedReads++
	}
	if compactedReads != 2 {
		t.Fatalf("compacted reads = %d want 2", compactedReads)
	}
}

func TestCompactOversizedPreservesBoundedReadPagesInSession(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	readPage := fmt.Sprintf(`{"mode":"content","path":"game.py","content":%q,"total_lines":454,"truncated":true,"receipt":{"tool":"read"}}`,
		strings.Repeat("1|line of python code here\n", 400))
	if err := store.AppendMessages(ctx, sess.ID,
		api.Message{ID: "u1", Role: api.MessageRoleUser, Content: "run the game"},
		api.Message{ID: "a1", Role: api.MessageRoleAssistant, Content: "Let me read the sources."},
		api.Message{ID: "t1", Role: api.MessageRoleTool, Content: readPage},
		api.Message{ID: "t2", Role: api.MessageRoleTool, Content: readPage},
	); err != nil {
		t.Fatal(err)
	}

	if err := mgr.Runner.History.ScheduleChunks(ctx, sess); err != nil {
		testutil.FailErr(t, "compactOversizedToolResultsInSession failed", err)
	}
	mgr.Runner.History.Wait()

	view := appliedView(t, mgr, store, sess.ID)
	for _, m := range view {
		if m.ID != "t1" && m.ID != "t2" {
			continue
		}
		if m.CompactedChunk != nil {
			t.Fatalf("bounded read %s unexpectedly compacted", m.ID)
		}
		if m.Content != readPage {
			t.Fatalf("bounded read %s content mutated", m.ID)
		}
	}
}

func TestWorkerChildSessionDietsOversizedToolResultsInView(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	parent, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent session", err)
	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: "implementer",
		Prompt:    "fix pacifism.py",
	})
	testutil.FailErr(t, "spawn worker child", err)

	readA := strings.Repeat("a", 34000)
	readB := strings.Repeat("b", 34000)
	if err := store.AppendMessages(ctx, child.ID,
		api.Message{ID: "u1", Role: api.MessageRoleUser, Content: "fix crash"},
		api.Message{ID: "a1", Role: api.MessageRoleAssistant, Content: "survey"},
		api.Message{ID: "t1", Role: api.MessageRoleTool, Content: readA},
		api.Message{ID: "t2", Role: api.MessageRoleTool, Content: readB},
		api.Message{ID: "a2", Role: api.MessageRoleAssistant, Content: "aged past hot"},
	); err != nil {
		t.Fatal(err)
	}

	if err := mgr.Runner.History.ScheduleChunks(ctx, child); err != nil {
		testutil.FailErr(t, "compactOversizedToolResultsInSession", err)
	}
	mgr.Runner.History.Wait()

	// Canonical rows stay full-fidelity in the store.
	msgs, err := store.GetMessages(ctx, child.ID)
	testutil.FailErr(t, "store.GetMessages", err)
	for _, m := range msgs {
		if m.ID == "t1" && m.Content != readA {
			t.Fatalf("canonical worker tool t1 mutated")
		}
		if m.ID == "t2" && m.Content != readB {
			t.Fatalf("canonical worker tool t2 mutated")
		}
		if (m.ID == "t1" || m.ID == "t2") && m.CompactedChunk != nil {
			t.Fatalf("canonical worker tool %s must not carry compaction metadata", m.ID)
		}
	}

	// Assembled/compaction view diets aged oversized tool rows (same gate as coordinator).
	view := appliedView(t, mgr, store, child.ID)
	compacted := 0
	for _, m := range view {
		if m.ID != "t1" && m.ID != "t2" {
			continue
		}
		if m.CompactedChunk == nil {
			t.Fatalf("worker tool %s not compacted in view", m.ID)
		}
		if m.Content == readA || m.Content == readB {
			t.Fatalf("worker tool %s view content must shrink", m.ID)
		}
		compacted++
	}
	if compacted != 2 {
		t.Fatalf("compacted worker tools = %d want 2", compacted)
	}
}
