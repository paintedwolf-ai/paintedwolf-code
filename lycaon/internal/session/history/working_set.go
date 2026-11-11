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

// draftExchangeBounds indexes each assistant draft that has draft-recovery rows
// to its canonical draft row and the highest ordinal in its recovery group.
type draftExchangeBounds struct {
	draftByID map[string]api.Message
	maxOrd    map[string]int64
}

func indexDraftExchanges(rows []api.Message) draftExchangeBounds {
	var b draftExchangeBounds
	for _, row := range rows {
		if target := api.DraftRecoveryTarget(row); target != "" {
			if b.maxOrd == nil {
				b.maxOrd = make(map[string]int64)
			}
			b.maxOrd[target] = max(b.maxOrd[target], row.Ord)
		}
	}
	if len(b.maxOrd) == 0 {
		return b
	}
	b.draftByID = make(map[string]api.Message, len(b.maxOrd))
	for _, row := range rows {
		if row.Role == api.MessageRoleAssistant && row.ID != "" {
			if _, ok := b.maxOrd[row.ID]; ok {
				b.draftByID[row.ID] = row
				b.maxOrd[row.ID] = max(b.maxOrd[row.ID], row.Ord)
			}
		}
	}
	return b
}

// minOrd returns the exchange start ordinal for row, aligning a draft-recovery
// row to its target draft's ordinal when that draft is present.
func (b draftExchangeBounds) minOrd(row api.Message) int64 {
	if target := api.DraftRecoveryTarget(row); target != "" {
		if draft, ok := b.draftByID[target]; ok && draft.Ord > 0 && (row.Ord <= 0 || draft.Ord < row.Ord) {
			return draft.Ord
		}
	}
	return row.Ord
}

// maxOrdFor returns the highest ordinal in row's draft-recovery group so a
// view splice never replaces only one side of a draft-recovery pair.
func (b draftExchangeBounds) maxOrdFor(row api.Message) int64 {
	if target := api.DraftRecoveryTarget(row); target != "" {
		if hi, ok := b.maxOrd[target]; ok {
			return max(row.Ord, hi)
		}
	}
	if row.Role == api.MessageRoleAssistant && row.ID != "" {
		if hi, ok := b.maxOrd[row.ID]; ok {
			return max(row.Ord, hi)
		}
	}
	return row.Ord
}

// mark returns the canonical cutoff identity for row's exchange start.
func (b draftExchangeBounds) mark(row api.Message) cutoffMark {
	if target := api.DraftRecoveryTarget(row); target != "" {
		if draft, ok := b.draftByID[target]; ok && draft.Ord > 0 {
			return markRow(draft)
		}
	}
	return markRow(row)
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
	bounds := indexDraftExchanges(rows)
	start, advanced := selectCutoff(rows, alloc, held)
	return workingSetBoundary{startOrd: bounds.minOrd(rows[start]), fullTurn: !advanced && start == first, advanced: advanced}
}

// completeExchangeStart returns the first index at or after from whose suffix
// holds every tool result with its call and every draft with its recovery rows.
func completeExchangeStart(rows []api.Message, from int) int {
	start := from
	for {
		for start < len(rows) && (rows[start].Role == api.MessageRoleTool || rows[start].Ord <= 0) {
			start++
		}
		priorDrafts := make(map[string]bool)
		priorTargets := make(map[string]bool)
		for i := 0; i < start && i < len(rows); i++ {
			if rows[i].Role == api.MessageRoleAssistant && rows[i].ID != "" {
				priorDrafts[rows[i].ID] = true
			}
			if target := api.DraftRecoveryTarget(rows[i]); target != "" {
				priorTargets[target] = true
			}
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
			if rows[i].Role == api.MessageRoleAssistant && rows[i].ID != "" && priorTargets[rows[i].ID] {
				orphan = i
			}
			if target := api.DraftRecoveryTarget(rows[i]); target != "" && priorDrafts[target] {
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
	bounds := indexDraftExchanges(rows)
	for i, row := range rows {
		if bounds.minOrd(row) > ord {
			if start := completeExchangeStart(rows, i); start < len(rows) {
				return bounds.minOrd(rows[start])
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
