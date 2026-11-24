package history

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/limits"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

type countingSynthesisModel struct {
	calls atomic.Int32
}

func (m *countingSynthesisModel) Summarize(ctx context.Context, _, _ string, _ int) (string, error) {
	m.calls.Add(1)
	if compaction.SummaryFormatRequested(ctx) {
		return `{"facts":["Verified canonical fact"],"completed":[],"pending":[],"reacquire":[],"constraints":[]}`, nil
	}
	return "Short summary", nil
}

func TestContinuationSurvivesBackgroundPassesAndEnrichesOnce(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.ModelContextWindow = 16000
	cfg.HardCeilingTokens = 7000
	cfg.TargetTokens = 6500
	cfg.BudgetTriggerPct = 80
	cfg.KeepRecentMessages = 2
	cfg.MessageSlice = 16
	cfg.SessionMinSavingsTokens = 1000
	cfg.ChunkStrategyDefault = "truncate"

	svc, st := newCompactionManager(t, cfg)
	svc.recallAvailable = func(context.Context, *api.Session) bool { return true }
	synthModel := &countingSynthesisModel{}
	svc.SetCompactor(compaction.NewSimpleCompactor(cfg, synthModel))

	sess, err := st.Sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	var seed []api.Message
	exitCode := 0
	for i := 1; i <= 14; i++ {
		handle := fmt.Sprintf("command#%d", i)
		callID := fmt.Sprintf("call-%d", i)
		seed = append(seed,
			api.Message{
				ID:        fmt.Sprintf("asst-%d", i),
				Role:      api.MessageRoleAssistant,
				Content:   fmt.Sprintf("Turn %d observation: %s", i, strings.Repeat("detailed evidence ", 70)),
				ToolCalls: []api.ToolCall{{ID: callID, Name: "command"}},
			},
			api.Message{
				ID:              fmt.Sprintf("tool-%d", i),
				Role:            api.MessageRoleTool,
				EvidenceHandles: []string{handle},
				ToolResult: &api.ToolResult{
					ToolCallID:     callID,
					Tool:           "command",
					Outcome:        api.ToolResultOutcomeCompleted,
					Content:        fmt.Sprintf("Output %d: %s", i, strings.Repeat("test passed ", 70)),
					Invocation:     &api.InvocationReceipt{ID: fmt.Sprintf("rcpt-%d", i), ToolCallID: callID, Invoked: true, Status: "completed", SourceVerdict: "passed"},
					CommandProgram: &api.CommandProgramReceipt{ID: fmt.Sprintf("prog-%d", i), ParentReceiptID: fmt.Sprintf("rcpt-%d", i), State: "completed", StatusCode: &exitCode},
				},
			},
		)
	}
	testutil.FailErr(t, "append seed history", st.Transcript.AppendMessages(t.Context(), sess.ID, seed...))

	// Pass 1: background admission + controlled synthesis completion.
	svc.Schedule(t.Context(), sess)
	svc.Wait()
	testutil.FailErr(t, "wait synthesis pass 1", svc.Runner.WaitContext(t.Context()))

	view1, ok, err := st.Compaction.GetCompactionView(t.Context(), sess.ID)
	testutil.FailErr(t, "get view after pass 1", err)
	if !ok || view1.Continuation == nil {
		t.Fatalf("expected published continuation view, got ok=%v view=%+v", ok, view1)
	}
	if view1.Continuation.SummaryThroughOrd == 0 {
		t.Fatalf("expected enrichment to advance SummaryThroughOrd, got %+v", view1.Continuation)
	}
	hasHandle := false
	for _, src := range view1.Continuation.Sources {
		if len(src.Handles) > 0 {
			hasHandle = true
			break
		}
	}
	if !hasHandle {
		t.Fatalf("expected evidence handles in continuation sources, got %+v", view1.Continuation.Sources)
	}

	// Ensure the continuation message itself exceeds the 1,800-token chunk threshold.
	longSummary := "## Continuation\n" + strings.Repeat("Preserved continuation detail with handle:command#1. ", 160)
	if tok := tokenest.EstimateDefault(longSummary); tok < cfg.ChunkTokenThreshold {
		t.Fatalf("longSummary tokens = %d, want >= %d", tok, cfg.ChunkTokenThreshold)
	}
	for i := range view1.Messages {
		if len(view1.Messages[i].ContentParts) == 1 && view1.Messages[i].ContentParts[0].Source == "compaction_summary" {
			view1.Messages[i].Content = longSummary
			view1.Messages[i].ContentParts[0].Content = longSummary
		}
	}
	view1.Generation++
	testutil.FailErr(t, "publish oversized continuation view", st.Compaction.PutCompactionView(t.Context(), sess.ID, *view1))

	// Pass 2: only the oversized continuation is in the view (plus protected tail) -> zero generic replacements.
	sess, err = st.Sessions.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "reload session before pass 2", err)
	genBeforeOnlyPass := sess.CompactionGeneration
	svc.Schedule(t.Context(), sess)
	svc.Wait()
	testutil.FailErr(t, "wait synthesis pass 2", svc.Runner.WaitContext(t.Context()))
	sessAfterOnlyPass, err := st.Sessions.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "reload session after continuation-only pass", err)
	if sessAfterOnlyPass.CompactionGeneration != genBeforeOnlyPass {
		t.Fatalf("continuation-only pass advanced generation from %d to %d", genBeforeOnlyPass, sessAfterOnlyPass.CompactionGeneration)
	}

	// Pass 3: append an independently oversized ordinary assistant row (quoting a continuation heading) + protected recent tail.
	quotedOrdinary := "## Continuation\n" + strings.Repeat("Ordinary assistant response quoting a heading. ", 220)
	testutil.FailErr(t, "append ordinary oversized row and protected tail", st.Transcript.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "ordinary-oversized", Role: api.MessageRoleAssistant, Content: quotedOrdinary},
		api.Message{ID: "tail-user", Role: api.MessageRoleUser, Content: "latest user question"},
		api.Message{ID: "tail-asst", Role: api.MessageRoleAssistant, Content: "latest assistant reply"},
	))
	svc.Schedule(t.Context(), sessAfterOnlyPass)
	svc.Wait()
	testutil.FailErr(t, "wait synthesis pass 3", svc.Runner.WaitContext(t.Context()))

	view3, ok, err := st.Compaction.GetCompactionView(t.Context(), sess.ID)
	testutil.FailErr(t, "get view after pass 3", err)
	if !ok || view3.Continuation == nil {
		t.Fatal("expected view after pass 3")
	}
	var foundSummary, foundOrdinaryCompacted bool
	for _, msg := range view3.Messages {
		if len(msg.ContentParts) == 1 && msg.ContentParts[0].Source == "compaction_summary" {
			foundSummary = true
			if msg.CompactedChunk != nil {
				t.Fatalf("typed continuation row was rewritten by chunk pass: %+v", msg.CompactedChunk)
			}
		}
		if msg.ID == "ordinary-oversized" {
			if msg.CompactedChunk == nil || len(msg.ContentParts) != 1 || msg.ContentParts[0].Source != "compacted_chunk" {
				t.Fatalf("ordinary oversized row was not chunk-compacted: %+v", msg)
			}
			foundOrdinaryCompacted = true
		}
	}
	if !foundSummary || !foundOrdinaryCompacted {
		t.Fatalf("foundSummary=%v foundOrdinaryCompacted=%v", foundSummary, foundOrdinaryCompacted)
	}

	// Verify canonical transcript and evidence handles remain retrievable.
	canonical, err := st.Transcript.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read canonical transcript", err)
	if len(canonical) != 31 {
		t.Fatalf("canonical message count = %d, want 31", len(canonical))
	}

	// Exercise synthesis rejections: newer generation, changed receiver, stale source.
	enriched := messageview.CloneContinuation(view3.Continuation)
	enriched.SummaryThroughOrd = view3.Continuation.ThroughOrd + 1

	staleGen := *view3
	staleGen.Generation--
	if err := svc.publishSynthesis(t.Context(), compaction.SessionInfo{ID: sess.ID}, staleGen, enriched); !errors.Is(err, synthesisStale) {
		t.Fatalf("expected synthesisStale for older generation, got %v", err)
	}
	if err := svc.publishSynthesis(t.Context(), compaction.SessionInfo{ID: sess.ID, ProviderID: "other", Model: "other"}, *view3, enriched); !errors.Is(err, synthesisModelChanged) {
		t.Fatalf("expected synthesisModelChanged for changed receiver, got %v", err)
	}
	first := canonical[0]
	first.Content = "edited canonical row"
	_, err = st.Transcript.UpdateMessage(t.Context(), sess.ID, first.ID, first)
	testutil.FailErr(t, "edit canonical row for stale check", err)
	if err := svc.publishSynthesis(t.Context(), compaction.SessionInfo{ID: sess.ID}, *view3, enriched); !errors.Is(err, synthesisStale) {
		t.Fatalf("expected synthesisStale for changed canonical source, got %v", err)
	}
}

func TestReopenDamagedRewrittenViewRepairsOnce(t *testing.T) {
	for _, enrichmentAvailable := range []bool{true, false} {
		name := "with_enrichment"
		if !enrichmentAvailable {
			name = "without_enrichment"
		}
		t.Run(name, func(t *testing.T) {
			guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
			database := testdbfixture.Open(t, "reopen-damaged-"+name+".db")
			testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
			st := store.NewSQL(database)

			sess, err := st.Sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)

			var seed []api.Message
			for i := 1; i <= 12; i++ {
				seed = append(seed,
					api.Message{
						ID:              fmt.Sprintf("asst-%d", i),
						Role:            api.MessageRoleAssistant,
						Content:         fmt.Sprintf("Observation %d: %s", i, strings.Repeat("canonical detail ", 60)),
						EvidenceHandles: []string{fmt.Sprintf("read#%d", i)},
					},
					api.Message{
						ID:      fmt.Sprintf("user-%d", i),
						Role:    api.MessageRoleUser,
						Content: fmt.Sprintf("Question %d", i),
					},
				)
			}
			testutil.FailErr(t, "append canonical history", st.Transcript.AppendMessages(t.Context(), sess.ID, seed...))
			rows, err := st.Transcript.GetMessages(t.Context(), sess.ID)
			testutil.FailErr(t, "read canonical history", err)
			last := rows[len(rows)-1]

			// Seed a damaged Format=1 view where the continuation summary was rewritten to compacted_chunk.
			damaged := store.CompactionView{
				Generation:        1,
				CoveredThroughOrd: last.Ord,
				CoveredThroughID:  last.ID,
				SourceSeq:         last.Seq,
				Continuation: &messageview.Continuation{
					Format:     messageview.ContinuationFormat,
					ThroughOrd: rows[len(rows)-3].Ord,
					Reason:     "references_only",
				},
				Messages: []api.Message{
					{
						ID:                   "cp-1",
						Role:                 api.MessageRoleUser,
						Content:              "Prior conversation compacted.",
						CompactionCheckpoint: true,
						ContentParts:         []api.MessageContentPart{{Content: "Prior conversation compacted.", Source: "compaction"}},
					},
					{
						ID:             "sum-damaged",
						Role:           api.MessageRoleAssistant,
						Content:        "damaged chunked continuation",
						ContentParts:   []api.MessageContentPart{{Content: "damaged chunked continuation", Source: "compacted_chunk"}},
						CompactedChunk: &api.CompactedChunkMeta{OriginalTokens: 2500, CompactedTokens: 200, Strategy: "truncate"},
					},
				},
			}
			testutil.FailErr(t, "seed damaged view", st.Compaction.PutCompactionView(t.Context(), sess.ID, damaged))

			cfg := compaction.DefaultCompactionConfig()
			cfg.Enabled = true
			cfg.ModelContextWindow = 8000
			cfg.HardCeilingTokens = 4000
			cfg.TargetTokens = 1200
			cfg.KeepRecentMessages = 2
			cfg.MessageSlice = 4

			newService := func(repo *store.Repositories) *Service {
				workspace := sessionscope.New(repo.Sessions)
				svc := New(repo.Sessions, repo.Transcript, repo.Compaction, lifecycle.New(repo.Sessions), workspace, limits.New(settings.DefaultSessionLimits(), workspace), nil, func(context.Context, *api.Session) bool { return true })
				svc.SetDataDir(t.TempDir())
				var summarizer compaction.Summarizer = compaction.UnavailableSummarizer{}
				if enrichmentAvailable {
					summarizer = &countingSynthesisModel{}
				}
				svc.SetCompactor(compaction.NewSimpleCompactor(cfg, summarizer))
				t.Cleanup(svc.Wait)
				return svc
			}

			// First reopen: Load falls back to canonical history; Schedule repairs the derived view once.
			st1 := store.NewSQL(database)
			svc1 := newService(st1)
			reopenedSess, err := st1.Sessions.Get(t.Context(), sess.ID)
			testutil.FailErr(t, "load reopened session", err)

			loaded1, err := svc1.Load(t.Context(), reopenedSess)
			testutil.FailErr(t, "load canonical fallback", err)
			if len(loaded1) == 0 || loaded1[0].ID == "cp-1" {
				t.Fatalf("expected canonical fallback on damaged view, got first=%+v", loaded1[0])
			}

			svc1.Schedule(t.Context(), reopenedSess)
			svc1.Wait()
			testutil.FailErr(t, "wait synthesis on first reopen", svc1.Runner.WaitContext(t.Context()))

			repairedView, ok, err := st1.Compaction.GetCompactionView(t.Context(), sess.ID)
			testutil.FailErr(t, "read repaired view", err)
			if !ok || repairedView.Continuation == nil {
				t.Fatalf("expected repaired view, got ok=%v view=%+v", ok, repairedView)
			}
			if repairedView.Generation <= damaged.Generation {
				t.Fatalf("expected generation > %d, got %d", damaged.Generation, repairedView.Generation)
			}
			var hasTypedSummary bool
			for _, msg := range repairedView.Messages {
				if len(msg.ContentParts) == 1 && msg.ContentParts[0].Source == "compaction_summary" && msg.CompactedChunk == nil {
					hasTypedSummary = true
				}
			}
			if !hasTypedSummary {
				t.Fatalf("repaired view missing typed compaction_summary part: %+v", repairedView.Messages)
			}

			// Second reopen: valid view is reused without rebuilding again.
			st2 := store.NewSQL(database)
			svc2 := newService(st2)
			sess2, err := st2.Sessions.Get(t.Context(), sess.ID)
			testutil.FailErr(t, "load session on second reopen", err)
			genAfterFirstRepair := sess2.CompactionGeneration

			view2, ok, err := st2.Compaction.GetCompactionView(t.Context(), sess.ID)
			testutil.FailErr(t, "get view on second reopen", err)
			if !ok || view2.Generation != genAfterFirstRepair {
				t.Fatalf("expected valid view on second reopen, ok=%v gen=%d want=%d", ok, view2.Generation, genAfterFirstRepair)
			}
			svc2.Schedule(t.Context(), sess2)
			svc2.Wait()
			testutil.FailErr(t, "wait synthesis on second reopen", svc2.Runner.WaitContext(t.Context()))

			sessAfterSecond, err := st2.Sessions.Get(t.Context(), sess.ID)
			testutil.FailErr(t, "reload session after second reopen", err)
			if sessAfterSecond.CompactionGeneration != genAfterFirstRepair {
				t.Fatalf("second reopen rebuilt view: generation changed from %d to %d", genAfterFirstRepair, sessAfterSecond.CompactionGeneration)
			}
		})
	}
}
