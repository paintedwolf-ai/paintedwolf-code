package history

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// ordExchange is one assistant call and its result at explicit ordinals.
func ordExchange(ord int64, body string) []api.Message {
	call := fmt.Sprintf("call-%d", ord)
	assistant := fmt.Sprintf("a-%d", ord)
	return []api.Message{
		{ID: assistant, Ord: ord, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: call, Name: "read"}}},
		{ID: fmt.Sprintf("r-%d", ord), Ord: ord + 1, Role: api.MessageRoleTool, Content: body, ToolResult: &api.ToolResult{Tool: "read", ToolCallID: call, Content: body}},
	}
}

type cutoffStep struct {
	last  int64
	start int64
}

// cutoffSequence appends exchanges from ord first until last and records every move.
func cutoffSequence(first, last int64, alloc workingSetAllocation) ([]cutoffStep, int, int) {
	var rows []api.Message
	var held int64
	var moves []cutoffStep
	stable, changed := 0, 0
	for ord := first; ord+1 <= last; ord += 2 {
		rows = append(rows, ordExchange(ord, "x")...)
		boundary := selectWorkingSetBoundary(rows, alloc, held)
		switch {
		case held == 0:
		case boundary.startOrd == held:
			stable++
		default:
			changed++
			moves = append(moves, cutoffStep{ord + 1, boundary.startOrd})
		}
		held = boundary.startOrd
	}
	return moves, stable, changed
}

func TestUniformExchangesHoldAnchorsAC_KV_03(t *testing.T) {
	ample := workingSetAllocation{rows: ActiveTurnWorkingSetRows, tokens: 1 << 30, bytes: 1 << 30}
	t.Run("exact sequence", func(t *testing.T) {
		for _, tc := range []struct {
			first int64
			want  []cutoffStep
		}{
			{1, []cutoffStep{{130, 33}, {162, 65}, {194, 97}, {226, 129}}},
			{2, []cutoffStep{{131, 32}, {161, 64}, {193, 96}, {225, 128}}},
			{16, []cutoffStep{{145, 64}, {193, 96}, {225, 128}}},
		} {
			got, _, _ := cutoffSequence(tc.first, 240, ample)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("first row %d: moves %v, want %v", tc.first, got, tc.want)
			}
		}
	})
	t.Run("every anchor phase settles on a 32-row cycle", func(t *testing.T) {
		for phase := int64(1); phase <= PrefixGridRowStep; phase++ {
			// Skip the first advance, whose reserve depends on the phase.
			settled := phase + 130 + 2*PrefixGridRowStep
			moves, _, _ := cutoffSequence(phase, settled+4*PrefixGridRowStep, ample)
			var cycle []cutoffStep
			for _, move := range moves {
				if move.last >= settled {
					cycle = append(cycle, move)
				}
			}
			if len(cycle) < 3 {
				t.Fatalf("phase %d: moves %v", phase, moves)
			}
			for i := 1; i < len(cycle); i++ {
				if cycle[i].start-cycle[i-1].start != PrefixGridRowStep || cycle[i].last-cycle[i-1].last != PrefixGridRowStep {
					t.Fatalf("phase %d: not a 32-row cycle: %v", phase, cycle)
				}
			}
			for _, move := range cycle {
				if offset := move.start % PrefixGridRowStep; offset > 1 {
					t.Fatalf("phase %d: start %d is off the anchor grid", phase, move.start)
				}
				// Rounding leaves 30 free rows here; only the hard budget is required.
				if rows := move.last - move.start + 1; rows > ActiveTurnWorkingSetRows {
					t.Fatalf("phase %d: %d rows after an advance", phase, rows)
				}
			}
		}
	})
	t.Run("one change in every sixteen transitions", func(t *testing.T) {
		// A settled start on the grid, then one full cycle of sixteen exchanges.
		rows := []api.Message{}
		for ord := int64(33); ord < 33+ActiveTurnWorkingSetRows; ord += 2 {
			rows = append(rows, ordExchange(ord, "x")...)
		}
		held := int64(33)
		stable, changed := 0, 0
		for i := range 16 {
			rows = append(rows, ordExchange(33+ActiveTurnWorkingSetRows+int64(2*i), "x")...)
			boundary := selectWorkingSetBoundary(rows, ample, held)
			if boundary.startOrd == held {
				stable++
			} else {
				changed++
			}
			held = boundary.startOrd
		}
		if stable != 15 || changed != 1 {
			t.Fatalf("transitions: %d stable, %d changed; want 15 and 1", stable, changed)
		}
	})
}

func TestBoundaryConstraintsAC_KV_06(t *testing.T) {
	body := func(n int) string { return strings.Repeat("x", n) }
	t.Run("variable sizes and ordinal gaps keep complete stable boundaries", func(t *testing.T) {
		var rows []api.Message
		alloc := workingSetAllocation{rows: 128, tokens: 1 << 30, bytes: 40_000}
		var held int64
		ord := int64(10)
		var moves []int64
		for i := range 80 {
			rows = append(rows, ordExchange(ord, body(200+(i%7)*300))...)
			ord += 2 + int64(i%3)*5 // gaps between exchanges
			next := selectWorkingSetBoundary(rows, alloc, held)
			if next.startOrd < held {
				t.Fatalf("cutoff moved backward: %d to %d", held, next.startOrd)
			}
			if next.startOrd != held {
				moves = append(moves, next.startOrd)
			}
			held = next.startOrd
			at := firstAtOrAfter(rows, held)
			var used workingSetUsage
			for _, row := range rows[at:] {
				used.add(row)
			}
			if used.exceeds(alloc) {
				t.Fatalf("exchange %d: suffix from %d exceeds %+v: %+v", i, held, alloc, used)
			}
			if rows[at].Role == api.MessageRoleTool {
				t.Fatalf("exchange %d: cutoff %d starts on a tool row", i, held)
			}
		}
		if len(moves) >= 20 {
			t.Fatalf("cutoff moved %d times in 80 appends: %v", len(moves), moves)
		}
	})
	t.Run("parallel calls and pending results stay whole", func(t *testing.T) {
		rows := []api.Message{
			{ID: "old", Ord: 30, Role: api.MessageRoleAssistant, Content: body(5000)},
			{ID: "a", Ord: 31, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "p1"}, {ID: "p2"}}},
			{ID: "r1", Ord: 32, Role: api.MessageRoleTool, Content: body(5000), ToolResult: &api.ToolResult{ToolCallID: "p1"}},
			{ID: "s", Ord: 33, Role: api.MessageRoleSystem, Content: "host row"},
			{ID: "r2", Ord: 34, Role: api.MessageRoleTool, Content: "small", ToolResult: &api.ToolResult{ToolCallID: "p2"}},
			{ID: "b", Ord: 35, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "p3"}}},
		}
		got := selectWorkingSetBoundary(rows, workingSetAllocation{rows: 128, tokens: 1 << 30, bytes: 6000}, 0)
		if got.startOrd != 35 {
			t.Fatalf("start = %d, want the pending call at 35", got.startOrd)
		}
		if at := firstAtOrAfter(rows, got.startOrd); completeExchangeStart(rows, at) != at {
			t.Fatalf("start %d strands a result", got.startOrd)
		}
	})
	t.Run("oversized latest exchange leaves no orphan", func(t *testing.T) {
		rows := append(ordExchange(40, "small"), ordExchange(42, body(200_000))...)
		got := selectWorkingSetBoundary(rows, workingSetAllocation{rows: 128, tokens: 100, bytes: 1000}, 0)
		if got.startOrd != 42 {
			t.Fatalf("start = %d, want the oversized exchange at 42", got.startOrd)
		}
	})
}

// tailStarts replays appends through Load and records where the canonical tail begins.
func tailStarts(t *testing.T, svc *Service, mem *store.Memory, sess *api.Session, appends int, first int, body string) []int64 {
	t.Helper()
	var starts []int64
	for i := range appends {
		testutil.FailErr(t, "append exchange", mem.AppendMessages(t.Context(), sess.ID, turnExchange(first+i, body)...))
		rows, err := svc.Load(t.Context(), sess)
		testutil.FailErr(t, "load", err)
		assertPairedExchanges(t, rows)
		// Rows ahead of a notice are the retained view and opener, not the tail.
		from := max(0, indexOfID(rows, historyCoverageNoticeID))
		at := slices.IndexFunc(rows[from:], func(row api.Message) bool { return row.Ord > 2 })
		starts = append(starts, rows[from+at].Ord)
	}
	return starts
}

func distinctInOrder(values []int64) []int64 {
	return slices.Compact(slices.Clone(values))
}

func burstConfig() compaction.CompactionConfig {
	cfg := compaction.DefaultCompactionConfig()
	cfg.ModelContextWindow, cfg.HardCeilingTokens, cfg.TargetTokens = 196608, 100000, 50000
	return cfg
}

// stableTurnSession publishes nothing in the background, so a cutoff sequence
// depends only on the appends a test makes.
func stableTurnSession(t *testing.T, cfg compaction.CompactionConfig) (*Service, *store.Memory, *api.Session) {
	t.Helper()
	svc, mem, sess := newTurnSession(t, cfg)
	svc.Runner = nil
	return svc, mem, sess
}

// appendEditBurst fills the tail near its byte bound, then appends one batch of
// eight ~35 KB tool results that crosses it.
func appendEditBurst(t *testing.T, mem *store.Memory, sessionID string) {
	t.Helper()
	for i := range 120 {
		testutil.FailErr(t, "append exchange", mem.AppendMessages(t.Context(), sessionID, turnExchange(100+i, strings.Repeat("o", 1000))...))
	}
	call := []api.ToolCall{}
	for i := range 8 {
		call = append(call, api.ToolCall{ID: fmt.Sprintf("edit-%d", i), Name: "edit"})
	}
	batch := []api.Message{{ID: "edits", Role: api.MessageRoleAssistant, ToolCalls: call}}
	for i := range 8 {
		content := strings.Repeat("e", 35_000)
		batch = append(batch, api.Message{ID: fmt.Sprintf("edit-result-%d", i), Role: api.MessageRoleTool, Content: content,
			ToolResult: &api.ToolResult{Tool: "edit", ToolCallID: call[i].ID, AssistantMessageID: "edits", Content: content, Outcome: api.ToolResultOutcomeCompleted}})
	}
	testutil.FailErr(t, "append edit batch", mem.AppendMessages(t.Context(), sessionID, batch...))
}

func TestEightEditBurstDoesNotSlideTheTailAC_KV_06(t *testing.T) {
	t.Run("without a view", func(t *testing.T) {
		svc, mem, sess := stableTurnSession(t, burstConfig())
		seedTurn(t, mem, sess.ID, 0, "")
		appendEditBurst(t, mem, sess.ID)
		starts := tailStarts(t, svc, mem, sess, 60, 1000, strings.Repeat("o", 1000))
		moves := distinctInOrder(starts)
		t.Logf("tail starts: %v", moves)
		if !slices.Equal(moves, burstTailStarts) {
			t.Fatalf("tail starts %v, want %v", moves, burstTailStarts)
		}
	})
	t.Run("behind a published view", func(t *testing.T) {
		svc, mem, sess := stableTurnSession(t, burstConfig())
		canonical := seedTurn(t, mem, sess.ID, 4, "short")
		publishCoverage(t, mem, sess.ID, canonical, canonical[len(canonical)-1].Ord, 1, 196608)
		appendEditBurst(t, mem, sess.ID)
		starts := tailStarts(t, svc, mem, sess, 60, 2000, strings.Repeat("o", 1000))
		moves := distinctInOrder(starts)
		t.Logf("tail starts: %v", moves)
		if !slices.Equal(moves, burstTailStarts) {
			t.Fatalf("tail starts behind a view %v, want %v", moves, burstTailStarts)
		}
	})
}

// Oversized bodies keep one projection across requests instead of rewriting a
// row on each append, and the request stays under its ceiling.
func TestOversizedBodiesHoldTheirProjectionAC_KV_06(t *testing.T) {
	cfg := burstConfig()
	cfg.HardCeilingTokens, cfg.TargetTokens = 40000, 20000
	svc, mem, sess := stableTurnSession(t, cfg)
	svc.recallAvailable = func(context.Context, *api.Session) bool { return true }
	seedTurn(t, mem, sess.ID, 0, "")
	appendEditBurst(t, mem, sess.ID)
	budgets := fixedBudgets{window: cfg.ModelContextWindow}
	frame := strings.Repeat("Standing instructions. ", 400)
	var previous []api.Message
	held, moved := 0, 0
	for step := range 64 {
		testutil.FailErr(t, "append exchange", mem.AppendMessages(t.Context(), sess.ID, turnExchange(5000+step, strings.Repeat("o", 1000))...))
		history, err := svc.Load(t.Context(), sess)
		testutil.FailErr(t, "load", err)
		fitted, err := svc.FitRequest(t.Context(), sess, history, requestFor(sess, frame, history), budgets)
		testutil.FailErr(t, "fit", err)
		assertPairedExchanges(t, fitted.Messages)
		if input := modelcall.EstimateRequestInput(fitted); input > cfg.HardCeilingTokens {
			t.Fatalf("step %d: input %d over ceiling %d", step, input, cfg.HardCeilingTokens)
		}
		if previous != nil {
			if commonRows(previous, fitted.Messages) == len(previous) {
				held++
			} else {
				moved++
			}
		}
		previous = fitted.Messages
	}
	t.Logf("held %d, moved %d", held, moved)
	if moved > 6 {
		t.Fatalf("request prefix moved on %d of %d requests", moved, moved+held)
	}
}

// burstTailStarts is the exact sequence over the burst fixture: one anchored
// advance when the batch crosses the bound, then a hold until the next crossing.
var burstTailStarts = []int64{65, 129, 193}

type fixedBudgets struct{ window int }

func (b fixedBudgets) RequestBudget(_ context.Context, _ string, req modelcall.CompletionRequest) modelcall.RequestBudget {
	return modelcall.RequestBudget{Window: b.window, Input: modelcall.EstimateRequestInput(req), Output: 4096, Headroom: 1024}
}

// firstRetainedOrd skips pinned rows to report the fitted history's real start.
func firstRetainedOrd(rows []api.Message) int64 {
	for _, row := range rows {
		if row.Ord > 0 && !row.ContextPinned && row.ID != "opener" && row.ID != "before" {
			return row.Ord
		}
	}
	return 0
}

// commonRows counts the leading messages two requests share by role and content.
func commonRows(a, b []api.Message) int {
	n := 0
	for n < len(a) && n < len(b) && a[n].Role == b[n].Role && a[n].Content == b[n].Content && a[n].ID == b[n].ID {
		n++
	}
	return n
}

func TestTighterFitFeedsLaterProjectionAC_KV_04(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.ModelContextWindow, cfg.HardCeilingTokens, cfg.TargetTokens = 65536, 40000, 20000
	svc, mem, sess := stableTurnSession(t, cfg)
	body := strings.Repeat("recorded observation ", 12)
	seedTurn(t, mem, sess.ID, 40, body)
	// A frame far above the cold-start estimate makes the final fit the tight bound.
	frame := strings.Repeat("Standing instructions. ", 5000)
	budgets := fixedBudgets{window: cfg.ModelContextWindow}
	var previous []api.Message
	var starts []int64
	fits, stableRequests := 0, 0
	for step := range 260 {
		testutil.FailErr(t, "append exchange", mem.AppendMessages(t.Context(), sess.ID, turnExchange(1000+step, body)...))
		history, err := svc.Load(t.Context(), sess)
		testutil.FailErr(t, "load", err)
		fitted, err := svc.FitRequest(t.Context(), sess, history, requestFor(sess, frame, history), budgets)
		testutil.FailErr(t, "fit", err)
		assertPairedExchanges(t, fitted.Messages)
		if input := modelcall.EstimateRequestInput(fitted); input > cfg.HardCeilingTokens {
			t.Fatalf("step %d: input %d over ceiling %d", step, input, cfg.HardCeilingTokens)
		}
		if countID(fitted.Messages, "opener") != 1 {
			t.Fatalf("step %d: opener present %d times", step, countID(fitted.Messages, "opener"))
		}
		start := firstRetainedOrd(fitted.Messages)
		if len(starts) == 0 || starts[len(starts)-1] != start {
			starts = append(starts, start)
			fits++
		} else {
			// Between advances every earlier row of the previous request is unchanged.
			if common := commonRows(previous, fitted.Messages); common != len(previous) {
				t.Fatalf("step %d: request differs at row %d of %d (%s) with the cutoff held at %d", step, common, len(previous), previous[common].ID, start)
			}
			stableRequests++
		}
		previous = fitted.Messages
	}
	t.Logf("fit starts: %v; %d held requests", starts, stableRequests)
	if !slices.Equal(starts, tightFitStarts) {
		t.Fatalf("fit starts %v, want %v", starts, tightFitStarts)
	}
	for i := 2; i < len(starts); i++ {
		if starts[i]-starts[i-1] < PrefixGridRowStep {
			t.Fatalf("advance of %d rows from %d: an independent sliding cutoff", starts[i]-starts[i-1], starts[i-1])
		}
		if starts[i]%PrefixGridRowStep > 1 {
			t.Fatalf("start %d is off the anchor grid", starts[i])
		}
	}
	if facts := svc.cutoffs.takeFacts(sess.ID); facts.FitOrd != starts[len(starts)-1] {
		t.Fatalf("final start %d did not come from the fit: %+v", starts[len(starts)-1], facts)
	}
	if held := svc.cutoffs.heldWorkingSet(sess.ID); held < starts[len(starts)-1] {
		t.Fatalf("working set %d stayed below the fit cut %d", held, starts[len(starts)-1])
	}
}

var tightFitStarts = []int64{3, 65, 129, 193, 257, 321, 385, 449}

func TestSecondCollapseSignatureHoldsBehindAViewAC_KV_07(t *testing.T) {
	// Fixture shape only: a stable prefix of view rows, then a long active turn.
	cfg := compaction.DefaultCompactionConfig()
	cfg.ModelContextWindow, cfg.HardCeilingTokens, cfg.TargetTokens = 196608, 40000, 20000
	svc, mem, sess := stableTurnSession(t, cfg)
	body := strings.Repeat("recorded observation ", 40)
	canonical := seedTurn(t, mem, sess.ID, 80, body)
	publishCoverage(t, mem, sess.ID, canonical, canonical[20].Ord, 1, cfg.ModelContextWindow)
	frame := strings.Repeat("Standing instructions. ", 400)
	budgets := fixedBudgets{window: cfg.ModelContextWindow}
	var previous []api.Message
	moved, held := 0, 0
	for step := range 120 {
		testutil.FailErr(t, "append exchange", mem.AppendMessages(t.Context(), sess.ID, turnExchange(3000+step, body)...))
		history, err := svc.Load(t.Context(), sess)
		testutil.FailErr(t, "load", err)
		fitted, err := svc.FitRequest(t.Context(), sess, history, requestFor(sess, frame, history), budgets)
		testutil.FailErr(t, "fit", err)
		if previous != nil {
			if common := commonRows(previous, fitted.Messages); common == len(previous) {
				held++
			} else {
				moved++
				if fitted.Debug.HistoryCut.Change == "" {
					t.Fatalf("step %d: first difference at %s with no cutoff change", step, previous[common].ID)
				}
			}
		}
		previous = fitted.Messages
	}
	t.Logf("held %d, moved %d", held, moved)
	if moved*16 > (moved+held)*2 {
		t.Fatalf("prefix moved on %d of %d requests", moved, moved+held)
	}
}

func TestCutoffStateInvalidatesAndRebuildsAC_KV_08(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	setup := func(t *testing.T) (*Service, *store.Memory, *api.Session) {
		t.Helper()
		svc, mem, sess := stableTurnSession(t, cfg)
		seedTurn(t, mem, sess.ID, 100, "recorded observation")
		return svc, mem, sess
	}
	project := func(t *testing.T, svc *Service, sess *api.Session) (int64, modelcall.HistoryCutFacts) {
		t.Helper()
		history, err := svc.Load(t.Context(), sess)
		testutil.FailErr(t, "load", err)
		start := svc.cutoffs.holdWorkingSet(svc.cutoffKey(t.Context(), sess, svc.compactionConfig(t.Context(), sess), nil), sess.ID, history, activeTurnAllocation(svc.compactionConfig(t.Context(), sess)))
		return start, svc.cutoffs.takeFacts(sess.ID)
	}
	t.Run("cache loss is one explained reset then stable appends", func(t *testing.T) {
		svc, mem, sess := setup(t)
		first, _ := project(t, svc, sess)
		svc.ForgetSessionState(sess.ID)
		rebuilt, facts := project(t, svc, sess)
		if facts.Change != cutoffRebuilt || rebuilt != first {
			t.Fatalf("rebuilt start %d (first %d), change %q", rebuilt, first, facts.Change)
		}
		for i := range 8 {
			testutil.FailErr(t, "append exchange", mem.AppendMessages(t.Context(), sess.ID, turnExchange(4000+i, "recorded observation")...))
			held, facts := project(t, svc, sess)
			if held != rebuilt || facts.Change != "" {
				t.Fatalf("append %d: start %d, change %q", i, held, facts.Change)
			}
		}
	})
	t.Run("a rewound or replaced row is another lineage", func(t *testing.T) {
		svc, _, sess := setup(t)
		key := svc.cutoffKey(t.Context(), sess, cfg, nil)
		rows := ordExchange(40, "x")
		svc.cutoffs.update(sess.ID, key, rows, func(st *cutoffState) { st.fit = markRow(rows[0]) })
		_ = svc.cutoffs.takeFacts(sess.ID)
		replaced := []api.Message{{ID: "other", Ord: 40, Role: api.MessageRoleAssistant}, {ID: "next", Ord: 41, Role: api.MessageRoleAssistant}}
		if held, _ := svc.cutoffs.heldFit(key, sess.ID, replaced); held != 0 {
			t.Fatalf("held %d across a replaced row", held)
		}
		if facts := svc.cutoffs.takeFacts(sess.ID); facts.Change != cutoffLineageChanged {
			t.Fatalf("change = %q", facts.Change)
		}
		svc.cutoffs.update(sess.ID, key, rows, func(st *cutoffState) { st.fit = markRow(rows[0]) })
		if held, _ := svc.cutoffs.heldFit(key, sess.ID, []api.Message{{ID: "early", Ord: 39, Role: api.MessageRoleAssistant}}); held != 0 {
			t.Fatalf("held %d past a rewound tail", held)
		}
	})
	t.Run("window change and published view reset the key", func(t *testing.T) {
		svc, _, sess := setup(t)
		key := svc.cutoffKey(t.Context(), sess, cfg, nil)
		rows := ordExchange(40, "x")
		svc.cutoffs.update(sess.ID, key, rows, func(st *cutoffState) { st.fit = markRow(rows[0]) })
		wider := key
		wider.window *= 2
		if held, _ := svc.cutoffs.heldFit(wider, sess.ID, rows); held != 0 {
			t.Fatalf("held %d across a window change", held)
		}
		if facts := svc.cutoffs.takeFacts(sess.ID); facts.Change != cutoffConfigChanged {
			t.Fatalf("change = %q", facts.Change)
		}
		published := wider
		published.generation++
		svc.cutoffs.update(sess.ID, published, rows, func(*cutoffState) {})
		if facts := svc.cutoffs.takeFacts(sess.ID); facts.Change != cutoffViewChanged {
			t.Fatalf("change = %q", facts.Change)
		}
	})
	t.Run("a stale view stops supplying summary coverage", func(t *testing.T) {
		svc, mem, sess := newTurnSession(t, cfg)
		canonical := seedTurn(t, mem, sess.ID, 100, "recorded observation")
		publishCoverage(t, mem, sess.ID, canonical, 40, 20, cfg.ModelContextWindow)
		if countID(loadProjected(t, svc, sess), "summary") != 1 {
			t.Fatal("fixture summary absent")
		}
		edited := canonical[5]
		edited.Content = "edited observation"
		_, err := mem.UpdateMessage(t.Context(), sess.ID, edited.ID, edited)
		testutil.FailErr(t, "edit covered row", err)
		if countID(loadProjected(t, svc, sess), "summary") != 0 {
			t.Fatal("summary survived its source being replaced")
		}
	})
	t.Run("a small context inherits no recent-token floor", func(t *testing.T) {
		small := compaction.DefaultCompactionConfig()
		small.ModelContextWindow, small.HardCeilingTokens, small.TargetTokens = 4096, 3000, 1400
		alloc := activeTurnAllocation(small)
		if alloc.tokens >= 16384 || alloc.tokens > small.HardCeilingTokens {
			t.Fatalf("allocation %+v on a 4K window", alloc)
		}
		var rows []api.Message
		for ord := int64(2); ord < 200; ord += 2 {
			rows = append(rows, ordExchange(ord, strings.Repeat("x", 800))...)
		}
		boundary := selectWorkingSetBoundary(rows, alloc, 0)
		var used workingSetUsage
		for _, row := range rows[firstAtOrAfter(rows, boundary.startOrd):] {
			used.add(row)
		}
		if used.exceeds(alloc) {
			t.Fatalf("suffix %+v exceeds %+v", used, alloc)
		}
	})
}
