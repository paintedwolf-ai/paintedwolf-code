package history

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func turnExchange(i int, body string) []api.Message {
	callID := fmt.Sprintf("call-%d", i)
	assistant := fmt.Sprintf("assistant-%d", i)
	return []api.Message{
		{ID: assistant, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: callID, Name: "read"}}},
		{ID: fmt.Sprintf("result-%d", i), Role: api.MessageRoleTool, Content: body, ToolResult: &api.ToolResult{
			Tool: "read", ToolCallID: callID, AssistantMessageID: assistant, Content: body, Outcome: api.ToolResultOutcomeCompleted}},
	}
}

// seedTurn appends an older row, the human opener, and exchanges of two rows each.
func seedTurn(t *testing.T, mem *store.Memory, sessionID string, exchanges int, body string) []api.Message {
	t.Helper()
	rows := []api.Message{
		{ID: "before", Role: api.MessageRoleAssistant, Content: "earlier work"},
		{ID: "opener", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, Content: "Finish the migration"},
	}
	for i := range exchanges {
		rows = append(rows, turnExchange(i, body)...)
	}
	testutil.FailErr(t, "append turn", mem.AppendMessages(t.Context(), sessionID, rows...))
	canonical, err := mem.GetMessages(t.Context(), sessionID)
	testutil.FailErr(t, "read turn", err)
	return canonical
}

// publishCoverage stores a view whose copies of retained rows are distinguishable from canonical rows.
func publishCoverage(t *testing.T, mem *store.Memory, sessionID string, canonical []api.Message, covered, summarized int64, window int) {
	t.Helper()
	messages := []api.Message{
		{ID: "checkpoint", Role: api.MessageRoleUser, Origin: api.MessageOriginHost, CompactionCheckpoint: true, Content: "checkpoint"},
		{ID: "summary", Role: api.MessageRoleAssistant, Content: "summary", ContentParts: []api.MessageContentPart{{Source: "compaction_summary", Content: "summary"}}},
	}
	var last api.Message
	for _, row := range canonical {
		if row.Ord > covered {
			break
		}
		last = row
		if row.Ord > summarized || row.ID == "opener" {
			if row.Role == api.MessageRoleTool {
				row.Content = viewCopy
			}
			messages = append(messages, row)
		}
	}
	testutil.FailErr(t, "publish coverage", mem.PutCompactionView(t.Context(), sessionID, store.CompactionView{
		Generation: 1, CoveredThroughOrd: last.Ord, CoveredThroughID: last.ID, SourceSeq: canonical[len(canonical)-1].Seq, Messages: messages,
		Continuation: &messageview.Continuation{Format: messageview.ContinuationFormat, ThroughOrd: summarized, SummaryThroughOrd: summarized,
			Window: window, Reason: "summarized"},
	}))
}

const viewCopy = "view copy"

func loadProjected(t *testing.T, svc *Service, sess *api.Session) []api.Message {
	t.Helper()
	loaded, err := svc.Load(t.Context(), sess)
	testutil.FailErr(t, "load history", err)
	return svc.ApplyView(t.Context(), sess, loaded)
}

func countID(rows []api.Message, id string) int {
	n := 0
	for _, row := range rows {
		if row.ID == id {
			n++
		}
	}
	return n
}

func indexOfID(rows []api.Message, id string) int {
	return slices.IndexFunc(rows, func(row api.Message) bool { return row.ID == id })
}

func assertPairedExchanges(t *testing.T, rows []api.Message) {
	t.Helper()
	calls := make(map[string]bool)
	for _, row := range rows {
		for _, call := range row.ToolCalls {
			calls[call.ID] = true
		}
		if row.Role == api.MessageRoleTool && row.ToolResult != nil && !calls[row.ToolResult.ToolCallID] {
			t.Fatalf("orphan tool result %s", row.ID)
		}
	}
}

func newTurnSession(t *testing.T, cfg compaction.CompactionConfig) (*Service, *store.Memory, *api.Session) {
	t.Helper()
	svc, mem := newCompactionManager(t, cfg)
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	return svc, mem, sess
}

func TestShortActiveTurnStaysCanonicalAC_KV_02(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	svc, mem, sess := newTurnSession(t, cfg)
	canonical := seedTurn(t, mem, sess.ID, 8, "short observation")
	publishCoverage(t, mem, sess.ID, canonical, canonical[len(canonical)-1].Ord, 1, cfg.ModelContextWindow)

	projected := loadProjected(t, svc, sess)

	var ids []string
	for _, row := range projected {
		if row.Ord > 1 {
			ids = append(ids, row.ID)
			if row.Content == viewCopy {
				t.Fatalf("%s took the view copy inside a turn that fits", row.ID)
			}
		}
	}
	want := make([]string, 0, len(canonical))
	for _, row := range canonical[1:] {
		want = append(want, row.ID)
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("active turn = %v, want %v", ids, want)
	}
	if indexOfID(projected, "summary") > indexOfID(projected, "opener") {
		t.Fatal("summary ending before the opener moved after it")
	}
	assertPairedExchanges(t, projected)
	if again := svc.ApplyView(t.Context(), sess, projected); len(again) != len(projected) {
		t.Fatalf("repeated application changed %d rows to %d", len(projected), len(again))
	}
}

func TestLongTurnReplacesOnlyValidatedOlderRangeAC_KV_01(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	seeded := func(t *testing.T) (*Service, *store.Memory, *api.Session, []api.Message) {
		t.Helper()
		svc, mem, sess := newTurnSession(t, cfg)
		return svc, mem, sess, seedTurn(t, mem, sess.ID, 160, "recorded observation")
	}
	_, _, _, canonical := seeded(t)
	start := activeWorkingSetStart(canonical, activeTurnAllocation(cfg), 0)
	if rows := len(canonical) - indexOfID(canonical, canonical[slices.IndexFunc(canonical, func(row api.Message) bool { return row.Ord == start })].ID); rows > ActiveTurnWorkingSetRows || rows < ActiveTurnWorkingSetRows-2*PrefixGridRowStep {
		t.Fatalf("working set holds %d rows, want at most %d with its reserve", rows, ActiveTurnWorkingSetRows)
	}

	t.Run("coverage before the boundary", func(t *testing.T) {
		svc, mem, sess, canonical := seeded(t)
		publishCoverage(t, mem, sess.ID, canonical, start+40, 120, cfg.ModelContextWindow)
		projected := loadProjected(t, svc, sess)
		if countID(projected, "opener") != 1 || indexOfID(projected, "opener") >= indexOfID(projected, "assistant-100") {
			t.Fatalf("opener must appear once before later exchanges: %d", countID(projected, "opener"))
		}
		if indexOfID(projected, "opener") > indexOfID(projected, "summary") {
			t.Fatal("opener must precede a summary reaching into the turn")
		}
		for _, row := range projected {
			if row.Ord <= 0 || row.Role != api.MessageRoleTool {
				continue
			}
			if row.Ord <= 120 {
				t.Fatalf("summarized row %d retained beside its summary", row.Ord)
			}
			if fromView := row.Content == viewCopy; fromView != (row.Ord < start) {
				t.Fatalf("row %d view copy = %v with working set at %d", row.Ord, fromView, start)
			}
		}
		assertPairedExchanges(t, projected)
	})

	t.Run("summary spanning the boundary is not clipped", func(t *testing.T) {
		svc, mem, sess, canonical := seeded(t)
		// Published summaries end on a complete exchange; start is an assistant row.
		summarized := start + 21
		publishCoverage(t, mem, sess.ID, canonical, start+40, summarized, cfg.ModelContextWindow)
		projected := loadProjected(t, svc, sess)
		if opener, summary := indexOfID(projected, "opener"), indexOfID(projected, "summary"); opener < 0 || opener > summary {
			t.Fatalf("opener %d must precede intra-turn continuation %d", opener, summary)
		}
		view, _, err := mem.GetCompactionView(t.Context(), sess.ID)
		testutil.FailErr(t, "read view", err)
		if view.Continuation.ThroughOrd != summarized {
			t.Fatalf("stored coverage changed to %d", view.Continuation.ThroughOrd)
		}
		for _, row := range projected {
			if row.Ord > 0 && row.ID != "opener" && row.Ord <= summarized {
				t.Fatalf("row %d duplicated behind its summary", row.Ord)
			}
		}
		if last := projected[len(projected)-1]; last.ID != canonical[len(canonical)-1].ID {
			t.Fatalf("newest row = %s", last.ID)
		}
		assertPairedExchanges(t, projected)
	})
}

func TestCoverageLagKeepsGapOrNamesOmissionAC_KV_05(t *testing.T) {
	t.Run("uncovered gap stays canonical while it fits", func(t *testing.T) {
		cfg := compaction.DefaultCompactionConfig()
		svc, mem, sess := newTurnSession(t, cfg)
		canonical := seedTurn(t, mem, sess.ID, 100, "recorded observation")
		publishCoverage(t, mem, sess.ID, canonical, 40, 20, cfg.ModelContextWindow)
		projected := loadProjected(t, svc, sess)
		for _, row := range canonical {
			if row.Ord > 40 && countID(projected, row.ID) != 1 {
				t.Fatalf("uncovered row %d present %d times", row.Ord, countID(projected, row.ID))
			}
		}
		if countID(projected, historyCoverageNoticeID) != 0 {
			t.Fatal("omission named when the gap fit")
		}
		assertPairedExchanges(t, projected)
	})

	t.Run("hard limit names the omitted interval without coverage", func(t *testing.T) {
		cfg := compaction.DefaultCompactionConfig()
		svc, mem, sess := newTurnSession(t, cfg)
		canonical := seedTurn(t, mem, sess.ID, 300, "recorded observation")
		publishCoverage(t, mem, sess.ID, canonical, 40, 20, cfg.ModelContextWindow)
		projected := loadProjected(t, svc, sess)
		noticeAt := indexOfID(projected, historyCoverageNoticeID)
		if noticeAt < 0 {
			t.Fatal("omission lacks a notice")
		}
		next := projected[noticeAt+1]
		want, err := svc.omissionNotice(t.Context(), sess, historyCoverageNoticeID, 41, next.Ord-1)
		testutil.FailErr(t, "render expected notice", err)
		if projected[noticeAt].Content != want.Content {
			t.Fatalf("notice does not name interval 41-%d", next.Ord-1)
		}
		view, _, err := mem.GetCompactionView(t.Context(), sess.ID)
		testutil.FailErr(t, "read view", err)
		if view.CoveredThroughOrd != 40 || view.Continuation.ThroughOrd != 20 {
			t.Fatalf("omission advanced coverage: %d/%d", view.CoveredThroughOrd, view.Continuation.ThroughOrd)
		}
		if countID(projected, "opener") != 1 {
			t.Fatal("omission lost the opener")
		}
		for _, row := range projected {
			if row.Ord > 40 && row.Ord < next.Ord && row.ID != "opener" {
				t.Fatalf("row %d inside the named omission", row.Ord)
			}
		}
		assertPairedExchanges(t, projected)
		if again := svc.ApplyView(t.Context(), sess, projected); countID(again, historyCoverageNoticeID) != 1 || len(again) != len(projected) {
			t.Fatal("repeated application changed the omission")
		}
	})

	t.Run("stale source identity discards the view", func(t *testing.T) {
		cfg := compaction.DefaultCompactionConfig()
		svc, mem, sess := newTurnSession(t, cfg)
		canonical := seedTurn(t, mem, sess.ID, 100, "recorded observation")
		publishCoverage(t, mem, sess.ID, canonical, 40, 20, cfg.ModelContextWindow)
		edited := canonical[5]
		edited.Content = "edited observation"
		_, err := mem.UpdateMessage(t.Context(), sess.ID, edited.ID, edited)
		testutil.FailErr(t, "edit covered row", err)
		projected := loadProjected(t, svc, sess)
		if countID(projected, "summary") != 0 || countID(projected, edited.ID) != 1 {
			t.Fatal("stale view still supplied coverage")
		}
	})
}

func TestPublishedSummaryStopsBeforeWorkingSet(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.ModelContextWindow, cfg.HardCeilingTokens, cfg.TargetTokens = 32768, 27000, 2000
	svc, mem, sess := newTurnSession(t, cfg)
	seedTurn(t, mem, sess.ID, 120, strings.Repeat("recorded observation ", 12))
	originals, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read turn", err)
	start := activeWorkingSetStart(originals, activeTurnAllocation(cfg), 0)
	_, err = svc.ForceCompact(t.Context(), sess.ID)
	testutil.FailErr(t, "force compact", err)
	view, ok, err := mem.GetCompactionView(t.Context(), sess.ID)
	testutil.FailErr(t, "read view", err)
	if !ok || view.Continuation == nil || view.Continuation.ThroughOrd == 0 {
		t.Fatal("fixture published no summary")
	}
	if view.Continuation.ThroughOrd >= start {
		t.Fatalf("summary through %d crossed working set %d", view.Continuation.ThroughOrd, start)
	}
}

func TestWorkingSetBoundarySelection(t *testing.T) {
	big := strings.Repeat("x", 80_000)
	parallel := []api.Message{
		{ID: "a", Ord: 10, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "p1"}, {ID: "p2"}}},
		{ID: "r1", Ord: 11, Role: api.MessageRoleTool, Content: big, ToolResult: &api.ToolResult{ToolCallID: "p1"}},
		{ID: "s", Ord: 12, Role: api.MessageRoleSystem, Content: "host row"},
		{ID: "r2", Ord: 13, Role: api.MessageRoleTool, Content: "small", ToolResult: &api.ToolResult{ToolCallID: "p2"}},
		{ID: "b", Ord: 14, Role: api.MessageRoleAssistant, Content: "next"},
	}
	cases := []struct {
		name  string
		rows  []api.Message
		alloc workingSetAllocation
		start int64
		full  bool
	}{
		{name: "all fit", rows: parallel, alloc: workingSetAllocation{rows: 128, tokens: 100000, bytes: 1 << 20}, start: 10, full: true},
		{name: "cut keeps no parallel fragment", rows: parallel, alloc: workingSetAllocation{rows: 128, tokens: 100, bytes: 1 << 20}, start: 14},
		{name: "row target", rows: parallel, alloc: workingSetAllocation{rows: 1, tokens: 100000, bytes: 1 << 20}, start: 14},
		{name: "byte target", rows: parallel, alloc: workingSetAllocation{rows: 128, tokens: 1000000, bytes: 1000}, start: 14},
		{name: "oversized latest exchange stays whole", rows: parallel[:4], alloc: workingSetAllocation{rows: 1, tokens: 1, bytes: 1}, start: 10},
		{name: "pending results keep the call", rows: parallel[:2], alloc: workingSetAllocation{rows: 1, tokens: 1, bytes: 1}, start: 10},
		{name: "no rows", alloc: workingSetAllocation{rows: 1, tokens: 1, bytes: 1}, full: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectWorkingSetBoundary(tc.rows, tc.alloc, 0)
			if got.startOrd != tc.start || got.fullTurn != tc.full {
				t.Fatalf("boundary = %+v, want start %d full %v", got, tc.start, tc.full)
			}
		})
	}
}

func TestActiveTurnAllocationScalesWithHistoryShare(t *testing.T) {
	cases := []struct {
		name         string
		hard, target int
		window       int
		tokens       int
	}{
		{name: "capped budget uses half the target", hard: 167116, target: 68812, window: 196608, tokens: 34406},
		{name: "ordinary floor", hard: 100000, target: 20000, window: 131072, tokens: 16384},
		{name: "small model inherits no floor", hard: 10444, target: 4300, window: 12288},
		{name: "ceiling below the frame reserve", hard: 600, target: 400, window: 16384, tokens: 600},
		{name: "unknown ceiling", target: 400, tokens: 16384},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := compaction.CompactionConfig{HardCeilingTokens: tc.hard, TargetTokens: tc.target, ModelContextWindow: tc.window}
			alloc := activeTurnAllocation(cfg)
			if tc.tokens > 0 && alloc.tokens != tc.tokens {
				t.Fatalf("tokens = %d, want %d", alloc.tokens, tc.tokens)
			}
			if tc.tokens == 0 && alloc.tokens >= activeTurnRecentTokens {
				t.Fatalf("small model inherited floor: %d", alloc.tokens)
			}
			if alloc.rows != ActiveTurnWorkingSetRows || alloc.bytes != alloc.tokens*4 {
				t.Fatalf("allocation = %+v", alloc)
			}
		})
	}
}

type feedPolicy struct{ ref llm.ModelRef }

func (p feedPolicy) GetForProjectRoots([]string) (llm.ModelPolicy, error) {
	return llm.ModelPolicy{Coordinator: p.ref}, nil
}

type feedWindow struct{ window int }

func (f *feedWindow) ContextLength(string, string) int { return f.window }

type feedBudgets struct{ feed *feedWindow }

func (b feedBudgets) RequestBudget(ctx context.Context, _ string, req modelcall.CompletionRequest) modelcall.RequestBudget {
	catalog := modelcall.NewCatalog("fixture", []modelinfo.Entry{{ID: "feed-model", ContextLength: b.feed.window}})
	return catalog.RequestBudget(ctx, req)
}

func requestFor(sess *api.Session, frame string, rows []api.Message) modelcall.CompletionRequest {
	standing := api.Message{ID: "standing", Role: api.MessageRoleSystem, ContextPinned: true, Content: frame}
	return modelcall.CompletionRequest{Model: sess.Model, Messages: append([]api.Message{standing}, rows...)}
}

func TestOneLiveConfigBoundsEveryRequestAC_KV_09(t *testing.T) {
	const providerWindow = 1_048_572
	feed := &feedWindow{}
	setup := func(t *testing.T, recall bool) (*Service, *store.Memory, *api.Session) {
		t.Helper()
		svc, mem := newCompactionManager(t, compaction.DefaultCompactionConfig())
		svc.Limits.SetModelSources(feedPolicy{llm.ModelRef{ProviderID: "fixture", Model: "feed-model"}}, feed)
		svc.recallAvailable = func(context.Context, *api.Session) bool { return recall }
		sess, err := mem.Create(t.Context(), api.CreateSessionRequest{ProviderID: "fixture", Model: "feed-model"}, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "create session", err)
		return svc, mem, sess
	}

	t.Run("feed resolving after load keeps one capped budget", func(t *testing.T) {
		svc, mem, sess := setup(t, true)
		for _, tc := range []struct{ window, live int }{{0, 196608}, {providerWindow, 196608}, {32768, 24576}} {
			feed.window = tc.window
			cfg := svc.compactionConfig(t.Context(), sess)
			if cfg.ModelContextWindow != tc.live || cfg.HardCeilingTokens != tc.live*85/100 {
				t.Fatalf("window %d: live %d hard %d, want %d", tc.window, cfg.ModelContextWindow, cfg.HardCeilingTokens, tc.live)
			}
		}
		feed.window = providerWindow
		seedTurn(t, mem, sess.ID, 120, strings.Repeat("recorded observation ", 200))
		feed.window = 65536
		_, err := svc.ForceCompact(t.Context(), sess.ID)
		testutil.FailErr(t, "force compact", err)
		view, ok, err := mem.GetCompactionView(t.Context(), sess.ID)
		testutil.FailErr(t, "read view", err)
		if !ok || view.Continuation == nil {
			t.Fatal("fixture published no continuation")
		}
		if want := svc.compactionConfig(t.Context(), sess).ModelContextWindow; view.Continuation.Window != want || want != 49152 {
			t.Fatalf("view window %d, live config %d", view.Continuation.Window, want)
		}
	})

	for _, tc := range []struct {
		name   string
		window int
	}{{"million token window", providerWindow}, {"32K window", 32768}} {
		t.Run("replay stays under the ceiling on "+tc.name, func(t *testing.T) {
			svc, mem, sess := setup(t, true)
			feed.window = tc.window
			hard := svc.compactionConfig(t.Context(), sess).HardCeilingTokens
			seedTurn(t, mem, sess.ID, 300, strings.Repeat("recorded observation ", 150))
			frame := strings.Repeat("Standing instructions. ", 400)
			for step := range 4 {
				testutil.FailErr(t, "append exchange", mem.AppendMessages(t.Context(), sess.ID, turnExchange(1000+step, strings.Repeat("recorded observation ", 150))...))
				history, err := svc.Load(t.Context(), sess)
				testutil.FailErr(t, "load", err)
				fitted, err := svc.FitRequest(t.Context(), sess, history, requestFor(sess, frame, history), feedBudgets{feed}, nil)
				testutil.FailErr(t, "fit", err)
				if input := modelcall.EstimateRequestInput(fitted); input > hard {
					t.Fatalf("step %d input %d exceeds hard ceiling %d", step, input, hard)
				}
				if countID(fitted.Messages, "opener") != 1 {
					t.Fatal("request lost the opener")
				}
				assertPairedExchanges(t, fitted.Messages)
			}
		})
	}

	t.Run("protected turn above the ceiling", func(t *testing.T) {
		huge := strings.Repeat("recorded observation ", 60_000)
		frame := strings.Repeat("Standing instructions. ", 400)
		feed.window = providerWindow

		svc, mem, sess := setup(t, true)
		seedTurn(t, mem, sess.ID, 1, huge)
		history, err := svc.Load(t.Context(), sess)
		testutil.FailErr(t, "load", err)
		hard := svc.compactionConfig(t.Context(), sess).HardCeilingTokens
		fitted, err := svc.FitRequest(t.Context(), sess, history, requestFor(sess, frame, history), feedBudgets{feed}, nil)
		testutil.FailErr(t, "bounded representation", err)
		if input := modelcall.EstimateRequestInput(fitted); input > hard {
			t.Fatalf("input %d exceeds hard ceiling %d", input, hard)
		}
		result := fitted.Messages[indexOfID(fitted.Messages, "result-0")]
		if len(result.Content) > 2048 || result.ToolResult.ToolCallID != "call-0" {
			t.Fatalf("result not bounded with its pairing: %d bytes", len(result.Content))
		}
		canonical, err := mem.GetMessages(t.Context(), sess.ID)
		testutil.FailErr(t, "read canonical", err)
		if canonical[len(canonical)-1].Content != huge {
			t.Fatal("bounded representation changed canonical history")
		}

		noRecall, noRecallMem, noRecallSess := setup(t, false)
		seedTurn(t, noRecallMem, noRecallSess.ID, 1, huge)
		history, err = noRecall.Load(t.Context(), noRecallSess)
		testutil.FailErr(t, "load", err)
		_, err = noRecall.FitRequest(t.Context(), noRecallSess, history, requestFor(noRecallSess, frame, history), feedBudgets{feed}, nil)
		var capacity *failure.ProviderContextTooSmallError
		if !errors.As(err, &capacity) || capacity.PromptTokens <= hard {
			t.Fatalf("capacity outcome = %v", err)
		}
	})
}

func TestRestoredCoverageNeverClipsSummaryProse(t *testing.T) {
	prior := &messageview.Continuation{ThroughOrd: 80, SummaryThroughOrd: 60, Reason: "summarized",
		Summary: messageview.ContinuationSummary{Facts: []string{"fact"}},
		Sources: []messageview.ContinuationSource{{MessageID: "kept", Ord: 40}, {MessageID: "restored", Ord: 70}}}
	for _, tc := range []struct {
		name      string
		tailStart int64
		through   int64
		summary   int64
		facts     int
		reason    string
		sources   int
	}{
		{name: "tail after the summary keeps it", tailStart: 71, through: 70, summary: 60, facts: 1, reason: "summarized", sources: 2},
		{name: "tail inside the summary drops prose", tailStart: 50, through: 49, reason: "references_only", sources: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := restoredCoverage(prior, tc.tailStart)
			if got.ThroughOrd != tc.through || got.SummaryThroughOrd != tc.summary || len(got.Summary.Facts) != tc.facts || got.Reason != tc.reason || len(got.Sources) != tc.sources {
				t.Fatalf("restored coverage = %+v", got)
			}
		})
	}
	if prior.ThroughOrd != 80 || len(prior.Summary.Facts) != 1 {
		t.Fatal("restoring changed the published record")
	}
}

func TestRequestInputLimit(t *testing.T) {
	for _, tc := range []struct {
		name              string
		hard, frame, want int
	}{
		{"ceiling binds", 167116, 22000, 167116},
		{"frame near the ceiling keeps history room", 20000, 19000, 24000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestInputLimit(tc.hard, tc.frame); got != tc.want {
				t.Fatalf("limit = %d, want %d", got, tc.want)
			}
		})
	}
	if got := requestInputLimit(0, 100); got <= 100 {
		t.Fatalf("unknown ceiling limited input to %d", got)
	}
}

func TestApplyViewPreservesDeferredUnindexedLiveRows(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	svc, mem, sess := newTurnSession(t, cfg)
	canonical := seedTurn(t, mem, sess.ID, 4, "observation")
	publishCoverage(t, mem, sess.ID, canonical, canonical[0].Ord, canonical[0].Ord, cfg.ModelContextWindow)

	deferred := api.Message{
		ID:      "deferred-nudge",
		Role:    api.MessageRoleUser,
		Origin:  api.MessageOriginHost,
		Content: "Repair closeout citations before finishing",
	}
	input := append(append([]api.Message(nil), canonical...), deferred)
	projected := svc.ApplyView(t.Context(), sess, input)
	if indexOfID(projected, "deferred-nudge") != len(projected)-1 {
		t.Fatalf("deferred nudge missing from projected tail: %+v", projected)
	}
	again := svc.ApplyView(t.Context(), sess, projected)
	if len(again) != len(projected) || indexOfID(again, "deferred-nudge") != len(again)-1 {
		t.Fatalf("repeated ApplyView dropped or duplicated rows: got %d want %d", len(again), len(projected))
	}
}
