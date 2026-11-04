package history

import (
	"slices"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// ActiveTurnWorkingSetRows is the ordinary recent-row target for a long turn.
const ActiveTurnWorkingSetRows = 128

const activeTurnRecentTokens = 16384

// workingSetAllocation bounds the recent rows kept canonical inside the active turn.
type workingSetAllocation struct {
	rows, tokens, bytes int
}

// activeTurnAllocation caps the recent-token target by the history share, so a
// small model does not inherit the ordinary floor.
func activeTurnAllocation(cfg compaction.CompactionConfig) workingSetAllocation {
	tokens := max(activeTurnRecentTokens, cfg.TargetTokens/2)
	historyShare := cfg.HardCeilingTokens - cfg.ColdStartOverhead(compaction.ResolveColdStartOverhead(""))
	if historyShare <= 0 {
		historyShare = cfg.HardCeilingTokens
	}
	if historyShare > 0 {
		tokens = min(tokens, historyShare)
	}
	return workingSetAllocation{rows: ActiveTurnWorkingSetRows, tokens: tokens, bytes: min(4<<20, tokens*4)}
}

type workingSetUsage struct {
	rows, tokens, bytes int
}

func (u *workingSetUsage) add(msg api.Message) {
	u.rows++
	u.tokens += estimateSingleMessageTokens(msg)
	u.bytes += estimateSingleMessageBytes(msg)
}

func (u workingSetUsage) exceeds(alloc workingSetAllocation) bool {
	return u.rows > alloc.rows || u.tokens > alloc.tokens || u.bytes > alloc.bytes
}

// workingSetBoundary names the first row of the canonical recent suffix.
type workingSetBoundary struct {
	startOrd int64
	fullTurn bool
	advanced bool
}

// selectWorkingSetBoundary chooses a complete-exchange start from the rows after
// the human opener, keeping held while it satisfies alloc. The latest exchange
// stays whole even when it alone exceeds the allocation; later fitting bounds
// its tool bodies.
func selectWorkingSetBoundary(rows []api.Message, alloc workingSetAllocation, held int64) workingSetBoundary {
	first := slices.IndexFunc(rows, func(row api.Message) bool { return row.Ord > 0 })
	if first < 0 {
		return workingSetBoundary{fullTurn: true}
	}
	start, advanced := selectCutoff(rows, alloc, held)
	return workingSetBoundary{startOrd: rows[start].Ord, fullTurn: !advanced && start == first, advanced: advanced}
}

// completeExchangeStart returns the first index at or after from whose suffix
// holds every tool result with its call, including parallel and interleaved results.
func completeExchangeStart(rows []api.Message, from int) int {
	start := from
	for {
		for start < len(rows) && (rows[start].Role == api.MessageRoleTool || rows[start].Ord <= 0) {
			start++
		}
		calls := make(map[string]bool)
		orphan := -1
		for i := start; i < len(rows); i++ {
			for _, call := range rows[i].ToolCalls {
				calls[call.ID] = true
			}
			if result := rows[i].ToolResult; rows[i].Role == api.MessageRoleTool && result != nil && result.ToolCallID != "" && !calls[result.ToolCallID] {
				orphan = i
			}
		}
		if orphan < 0 {
			return start
		}
		start = orphan + 1
	}
}

func latestExchangeStart(rows []api.Message) int {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Role != api.MessageRoleTool && rows[i].Ord > 0 && completeExchangeStart(rows, i) == i {
			return i
		}
	}
	return 0
}

// activeWorkingSetStart returns the ordinal summary pages must stop before, or
// zero when no active human instruction exists.
func activeWorkingSetStart(rows []api.Message, alloc workingSetAllocation, held int64) int64 {
	idx := api.UserIntentBoundary(rows) - 1
	if idx < 0 || rows[idx].Ord <= 0 {
		return 0
	}
	if start := selectWorkingSetBoundary(rows[idx+1:], alloc, held).startOrd; start > 0 {
		return start
	}
	return rows[idx].Ord + 1
}

// viewSplice is the ordinal range a valid published view may replace.
type viewSplice struct {
	intentIdx       int
	intentOrd       int64
	replacedThrough int64
	openerFirst     bool
}

// planViewSplice replaces only intent < ord <= min(covered, workingSetStart-1).
// A summary range spanning the boundary is never clipped: the canonical suffix
// starts after it, so summarized exchanges are not presented twice.
func planViewSplice(history []api.Message, view *store.CompactionView, start int64) viewSplice {
	plan := viewSplice{intentIdx: -1, replacedThrough: view.CoveredThroughOrd}
	idx := api.UserIntentBoundary(history) - 1
	if idx < 0 || history[idx].Ord <= 0 {
		return plan
	}
	after := history[idx+1:]
	summarized := int64(0)
	if view.Continuation != nil {
		summarized = view.Continuation.ThroughOrd
	}
	if summarized >= start {
		start = firstExchangeAfter(after, summarized)
	}
	plan.intentIdx, plan.intentOrd = idx, history[idx].Ord
	plan.replacedThrough = min(view.CoveredThroughOrd, start-1)
	plan.openerFirst = summarized > plan.intentOrd
	return plan
}

func firstExchangeAfter(rows []api.Message, ord int64) int64 {
	for i, row := range rows {
		if row.Ord > ord {
			if start := completeExchangeStart(rows, i); start < len(rows) {
				return rows[start].Ord
			}
			break
		}
	}
	return ord + 1
}

// viewTailStop ends a backward read once the working set is settled and the rest
// is covered. Uncovered rows continue while hard bounds allow.
func viewTailStop(covered int64, alloc workingSetAllocation) func(api.Message) bool {
	var used workingSetUsage
	settled := false
	return func(row api.Message) bool {
		if row.Ord <= covered && settled {
			return true
		}
		used.add(row)
		settled = settled || used.exceeds(alloc) || api.IsUserIntentMessage(row)
		return false
	}
}
