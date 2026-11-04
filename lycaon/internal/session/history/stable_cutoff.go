package history

import (
	"context"
	"slices"
	"sync"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/pkg/api"
)

// PrefixGridRowStep is the preferred spacing of cutoffs on canonical ordinals.
// Ordinals may have gaps and exchanges vary, so it is a preference, not a promise.
const PrefixGridRowStep = 32

// Reasons a cutoff changed, reported with each request's history facts.
const (
	cutoffRebuilt        = "state_rebuilt"
	cutoffViewChanged    = "view_changed"
	cutoffConfigChanged  = "config_changed"
	cutoffLineageChanged = "lineage_changed"
	cutoffTailAdvanced   = "tail_advanced"
	cutoffWorkAdvanced   = "working_set_advanced"
	cutoffFitAdvanced    = "fit_advanced"
)

// lessReserve shrinks an allocation by the room a forced advance aims to free.
// A small allocation keeps most of itself rather than collapsing to its last exchange.
func (a workingSetAllocation) lessReserve() workingSetAllocation {
	return workingSetAllocation{
		rows:   a.rows - min(PrefixGridRowStep, a.rows/2),
		tokens: a.tokens - fitReserve(a.tokens),
		bytes:  a.bytes - min(max(8192, a.bytes/16), a.bytes/4),
	}
}

// fitReserve is the token room a required advance aims to leave free.
func fitReserve(tokens int) int { return min(max(2048, tokens/16), tokens/4) }

// fittingStart returns the first index whose suffix stays within alloc.
func fittingStart(rows []api.Message, alloc workingSetAllocation) int {
	var used workingSetUsage
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Ord <= 0 {
			continue
		}
		used.add(rows[i])
		if used.exceeds(alloc) {
			return i + 1
		}
	}
	return 0
}

// firstAtOrAfter returns the index of the first canonical row at or after ord.
func firstAtOrAfter(rows []api.Message, ord int64) int {
	for i := range rows {
		if rows[i].Ord > 0 && rows[i].Ord >= ord {
			return i
		}
	}
	return len(rows)
}

// advanceCutoff moves past floor toward the reserve target, preferring the
// nearest anchor. Hard budgets are met by floor; the reserve is best effort, and
// the latest exchange stays whole however little room that leaves.
func advanceCutoff(rows []api.Message, alloc workingSetAllocation, floor int) int {
	protected := latestExchangeStart(rows)
	pick := max(floor, fittingStart(rows, alloc.lessReserve()))
	if pick < len(rows) {
		anchor := firstAtOrAfter(rows, (rows[pick].Ord+PrefixGridRowStep/2)/PrefixGridRowStep*PrefixGridRowStep)
		if anchor <= floor && floor < len(rows) {
			anchor = firstAtOrAfter(rows, (rows[floor].Ord/PrefixGridRowStep+1)*PrefixGridRowStep)
		}
		if anchor <= protected {
			pick = anchor
		}
	}
	return min(completeExchangeStart(rows, pick), protected)
}

// selectCutoff keeps a held ordinal while the rows from it fit, and otherwise
// advances; it never recomputes a minimal cutoff per request.
func selectCutoff(rows []api.Message, alloc workingSetAllocation, held int64) (int, bool) {
	feasible := fittingStart(rows, alloc)
	if from := firstAtOrAfter(rows, held); from >= feasible && from < len(rows) {
		return min(completeExchangeStart(rows, from), latestExchangeStart(rows)), false
	}
	return advanceCutoff(rows, alloc, feasible), true
}

// cutoffKey scopes held cutoffs to one published view and effective fit config.
type cutoffKey struct {
	generation           int
	provider, model      string
	window, hard, target int
}

// cutoffMark names a retained row by stable identity, not slice position.
type cutoffMark struct {
	ord int64
	id  string
}

func markRow(row api.Message) cutoffMark { return cutoffMark{ord: row.Ord, id: row.ID} }

// cutoffState is rebuildable projection state, never coverage or authority.
type cutoffState struct {
	key             cutoffKey
	tail, work, fit cutoffMark
	// bodies is the last ordinal whose recorded tool body later requests replay
	// as a recoverable projection.
	bodies int64
	change string
}

// note records why a cutoff moved without hiding a pending reset reason.
func (st *cutoffState) note(reason string) {
	if st.change == "" {
		st.change = reason
	}
}

func (st cutoffState) lineageHolds(rows []api.Message) bool {
	last := int64(0)
	for _, row := range rows {
		last = max(last, row.Ord)
	}
	if last == 0 {
		return true
	}
	return !slices.ContainsFunc([]cutoffMark{st.tail, st.work, st.fit}, func(mark cutoffMark) bool {
		if mark.ord == 0 {
			return false
		}
		return mark.ord > last || slices.ContainsFunc(rows, func(row api.Message) bool { return row.Ord == mark.ord && row.ID != mark.id })
	})
}

// cutoffTracker holds one bounded cutoff record per session. Losing a record
// costs one explained reset; selection rebuilds from canonical anchors.
type cutoffTracker struct {
	mu       sync.Mutex
	sessions scopedstore.LRU[cutoffState]
}

func (t *cutoffTracker) update(sessionID string, key cutoffKey, rows []api.Message, apply func(*cutoffState)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, found := t.sessions.Load(sessionID)
	switch {
	case !found:
		st = cutoffState{key: key, change: cutoffRebuilt}
	case st.key.generation != key.generation:
		st = cutoffState{key: key, change: cutoffViewChanged}
	case st.key != key:
		st = cutoffState{key: key, change: cutoffConfigChanged}
	case !st.lineageHolds(rows):
		st = cutoffState{key: key, change: cutoffLineageChanged}
	}
	apply(&st)
	t.sessions.Store(sessionID, st)
}

func (t *cutoffTracker) heldWorkingSet(sessionID string) int64 {
	st, _ := t.sessions.Load(sessionID)
	return st.work.ord
}

// takeFacts reports the current cutoffs and consumes the pending change reason.
func (t *cutoffTracker) takeFacts(sessionID string) modelcall.HistoryCutFacts {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, found := t.sessions.Load(sessionID)
	if !found {
		return modelcall.HistoryCutFacts{}
	}
	facts := modelcall.HistoryCutFacts{Generation: st.key.generation, TailOrd: st.tail.ord,
		WorkingSetOrd: st.work.ord, FitOrd: st.fit.ord, BodiesOrd: st.bodies, Change: st.change}
	st.change = ""
	t.sessions.Store(sessionID, st)
	return facts
}

func (m *Service) cutoffKey(ctx context.Context, sess *api.Session, cfg compaction.CompactionConfig, generation int) cutoffKey {
	provider, model := m.Model(ctx, sess)
	return cutoffKey{generation: generation, provider: provider, model: model,
		window: cfg.ModelContextWindow, hard: cfg.HardCeilingTokens, target: cfg.TargetTokens}
}

// holdCanonicalTail keeps the bounded tail's first row across appends. A read
// that hard bounds cut below the held row forces one anchored advance.
func (m *Service) holdCanonicalTail(key cutoffKey, sessionID string, rows []api.Message, truncated bool, bounds tailWindowBounds) ([]api.Message, bool) {
	if len(rows) == 0 {
		return rows, truncated
	}
	alloc := workingSetAllocation{rows: bounds.maxRows, tokens: bounds.maxTokens, bytes: bounds.maxBytes}
	out, cut := rows, truncated
	m.cutoffs.update(sessionID, key, rows, func(st *cutoffState) {
		if truncated && st.tail.ord < rows[0].Ord {
			st.tail = markRow(rows[advanceCutoff(rows, alloc, 0)])
			st.note(cutoffTailAdvanced)
		}
		if st.tail.ord > rows[0].Ord {
			if at := firstAtOrAfter(rows, st.tail.ord); at < len(rows) {
				out, cut = rows[at:], true
			}
		}
	})
	return out, cut
}

// holdWorkingSet returns the first ordinal of the canonical recent suffix.
func (m *Service) holdWorkingSet(key cutoffKey, sessionID string, rows []api.Message, alloc workingSetAllocation) int64 {
	idx := api.UserIntentBoundary(rows) - 1
	if idx < 0 || rows[idx].Ord <= 0 {
		return 0
	}
	start := rows[idx].Ord + 1
	m.cutoffs.update(sessionID, key, rows, func(st *cutoffState) {
		boundary := selectWorkingSetBoundary(rows[idx+1:], alloc, st.work.ord)
		if boundary.startOrd == 0 {
			return
		}
		start = boundary.startOrd
		if boundary.advanced {
			st.note(cutoffWorkAdvanced)
		}
		if start > st.work.ord {
			st.work = markRow(rows[idx+1:][firstAtOrAfter(rows[idx+1:], start)])
		}
	})
	return start
}

// noteFitCut feeds a tighter final fit back into later projection: the working
// set follows it, so summary publication can still cover what the fit omitted.
func (m *Service) noteFitCut(key cutoffKey, sessionID string, rows, fitted []api.Message) {
	_, through := omittedOrdinals(rows, compaction.ContextMessagesFromAPI(fitted))
	bodies := lastProjectedBody(rows, fitted)
	if through == 0 && bodies == 0 {
		return
	}
	at := slices.IndexFunc(fitted, func(row api.Message) bool { return row.Ord > through && !row.ContextPinned })
	m.cutoffs.update(sessionID, key, rows, func(st *cutoffState) {
		if bodies > st.bodies {
			st.bodies = bodies
			st.note(cutoffFitAdvanced)
		}
		if through == 0 || at < 0 || fitted[at].Ord <= st.fit.ord {
			return
		}
		st.fit = markRow(fitted[at])
		st.note(cutoffFitAdvanced)
		if st.fit.ord > st.work.ord {
			st.work = st.fit
		}
	})
}

// lastProjectedBody finds the newest tool row whose body the fit replaced.
func lastProjectedBody(rows, fitted []api.Message) int64 {
	original := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.Role == api.MessageRoleTool {
			original[row.ID] = row.Content
		}
	}
	last := int64(0)
	for _, row := range fitted {
		if body, ok := original[row.ID]; ok && body != row.Content {
			last = max(last, row.Ord)
		}
	}
	return last
}

// heldFit returns the held fit and body-projection ordinals. It creates no
// record, so a request that never fitted leaves no state behind.
func (m *Service) heldFit(key cutoffKey, sessionID string, rows []api.Message) (held, bodies int64) {
	if _, found := m.cutoffs.sessions.Load(sessionID); !found {
		return 0, 0
	}
	m.cutoffs.update(sessionID, key, rows, func(st *cutoffState) { held, bodies = st.fit.ord, st.bodies })
	return held, bodies
}
