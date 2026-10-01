package grammargen

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

const grammargenTracePhasesEnv = "GTS_GRAMMARGEN_TRACE_PHASES"

type phaseTrace struct {
	enabled bool
	name    string
}

type conflictResolutionStats struct {
	AugmentStatesScanned            int
	AugmentActionEntriesScanned     int
	AugmentCandidates               int
	AugmentCandidateChecks          int
	AugmentLookaheadsAdded          int
	AugmentRepeatStartCacheHits     int
	AugmentRepeatStartCacheMisses   int
	AugmentMaxCandidatesPerState    int
	AugmentMaxTerminalEntriesState  int
	AugmentSecondPassShiftEntries   int
	AugmentSecondPassReduceOnly     int
	AugmentStatesWithoutShiftTarget int
	StatesScanned                   int
	ActionEntriesScanned            int
	ConflictsResolved               int
	MaxActionsPerConflict           int
	RHSBeginCalls                   int
	RHSBeginHits                    int
	RHSBeginMisses                  int
	RHSEndCalls                     int
	RHSContinueCalls                int
	RepeatHelperReduceShiftCalls    int
	RepeatHelperContinueCalls       int
	ParentSuffixLookaheadCalls      int
	ConflictGroupShiftReduceCalls   int
	ConflictGroupShiftReduceHits    int
	ConflictGroupShiftReduceMisses  int
	ConflictGroupReduceLHSCalls     int
	ConflictGroupReduceLHSHits      int
	ConflictGroupReduceLHSMisses    int
}

func (s *conflictResolutionStats) add(other conflictResolutionStats) {
	s.AugmentStatesScanned += other.AugmentStatesScanned
	s.AugmentActionEntriesScanned += other.AugmentActionEntriesScanned
	s.AugmentCandidates += other.AugmentCandidates
	s.AugmentCandidateChecks += other.AugmentCandidateChecks
	s.AugmentLookaheadsAdded += other.AugmentLookaheadsAdded
	s.AugmentRepeatStartCacheHits += other.AugmentRepeatStartCacheHits
	s.AugmentRepeatStartCacheMisses += other.AugmentRepeatStartCacheMisses
	s.AugmentSecondPassShiftEntries += other.AugmentSecondPassShiftEntries
	s.AugmentSecondPassReduceOnly += other.AugmentSecondPassReduceOnly
	s.AugmentStatesWithoutShiftTarget += other.AugmentStatesWithoutShiftTarget
	s.StatesScanned += other.StatesScanned
	s.ActionEntriesScanned += other.ActionEntriesScanned
	s.ConflictsResolved += other.ConflictsResolved
	s.RHSBeginCalls += other.RHSBeginCalls
	s.RHSBeginHits += other.RHSBeginHits
	s.RHSBeginMisses += other.RHSBeginMisses
	s.RHSEndCalls += other.RHSEndCalls
	s.RHSContinueCalls += other.RHSContinueCalls
	s.RepeatHelperReduceShiftCalls += other.RepeatHelperReduceShiftCalls
	s.RepeatHelperContinueCalls += other.RepeatHelperContinueCalls
	s.ParentSuffixLookaheadCalls += other.ParentSuffixLookaheadCalls
	s.ConflictGroupShiftReduceCalls += other.ConflictGroupShiftReduceCalls
	s.ConflictGroupShiftReduceHits += other.ConflictGroupShiftReduceHits
	s.ConflictGroupShiftReduceMisses += other.ConflictGroupShiftReduceMisses
	s.ConflictGroupReduceLHSCalls += other.ConflictGroupReduceLHSCalls
	s.ConflictGroupReduceLHSHits += other.ConflictGroupReduceLHSHits
	s.ConflictGroupReduceLHSMisses += other.ConflictGroupReduceLHSMisses
	if other.MaxActionsPerConflict > s.MaxActionsPerConflict {
		s.MaxActionsPerConflict = other.MaxActionsPerConflict
	}
	if other.AugmentMaxCandidatesPerState > s.AugmentMaxCandidatesPerState {
		s.AugmentMaxCandidatesPerState = other.AugmentMaxCandidatesPerState
	}
	if other.AugmentMaxTerminalEntriesState > s.AugmentMaxTerminalEntriesState {
		s.AugmentMaxTerminalEntriesState = other.AugmentMaxTerminalEntriesState
	}
}

func (s conflictResolutionStats) traceFields() map[string]any {
	return map[string]any{
		"augment_states_scanned":              s.AugmentStatesScanned,
		"augment_action_entries_scanned":      s.AugmentActionEntriesScanned,
		"augment_candidates":                  s.AugmentCandidates,
		"augment_candidate_checks":            s.AugmentCandidateChecks,
		"augment_lookaheads_added":            s.AugmentLookaheadsAdded,
		"augment_repeat_start_cache_hits":     s.AugmentRepeatStartCacheHits,
		"augment_repeat_start_cache_misses":   s.AugmentRepeatStartCacheMisses,
		"augment_max_candidates_per_state":    s.AugmentMaxCandidatesPerState,
		"augment_max_terminal_entries_state":  s.AugmentMaxTerminalEntriesState,
		"augment_second_pass_shift_entries":   s.AugmentSecondPassShiftEntries,
		"augment_second_pass_reduce_only":     s.AugmentSecondPassReduceOnly,
		"augment_states_without_shift_target": s.AugmentStatesWithoutShiftTarget,
		"states_scanned":                      s.StatesScanned,
		"action_entries_scanned":              s.ActionEntriesScanned,
		"conflicts_resolved":                  s.ConflictsResolved,
		"max_actions_per_conflict":            s.MaxActionsPerConflict,
		"rhs_begin_calls":                     s.RHSBeginCalls,
		"rhs_begin_hits":                      s.RHSBeginHits,
		"rhs_begin_misses":                    s.RHSBeginMisses,
		"rhs_end_calls":                       s.RHSEndCalls,
		"rhs_continue_calls":                  s.RHSContinueCalls,
		"repeat_helper_reduce_shift_calls":    s.RepeatHelperReduceShiftCalls,
		"repeat_helper_continue_calls":        s.RepeatHelperContinueCalls,
		"parent_suffix_lookahead_calls":       s.ParentSuffixLookaheadCalls,
		"conflict_group_shift_reduce_calls":   s.ConflictGroupShiftReduceCalls,
		"conflict_group_shift_reduce_hits":    s.ConflictGroupShiftReduceHits,
		"conflict_group_shift_reduce_misses":  s.ConflictGroupShiftReduceMisses,
		"conflict_group_reduce_lhs_calls":     s.ConflictGroupReduceLHSCalls,
		"conflict_group_reduce_lhs_hits":      s.ConflictGroupReduceLHSHits,
		"conflict_group_reduce_lhs_misses":    s.ConflictGroupReduceLHSMisses,
	}
}

func (s conflictResolutionStats) augmentTraceFields() map[string]any {
	return map[string]any{
		"augment_states_scanned":              s.AugmentStatesScanned,
		"augment_action_entries_scanned":      s.AugmentActionEntriesScanned,
		"augment_candidates":                  s.AugmentCandidates,
		"augment_candidate_checks":            s.AugmentCandidateChecks,
		"augment_lookaheads_added":            s.AugmentLookaheadsAdded,
		"augment_repeat_start_cache_hits":     s.AugmentRepeatStartCacheHits,
		"augment_repeat_start_cache_misses":   s.AugmentRepeatStartCacheMisses,
		"augment_max_candidates_per_state":    s.AugmentMaxCandidatesPerState,
		"augment_max_terminal_entries_state":  s.AugmentMaxTerminalEntriesState,
		"augment_second_pass_shift_entries":   s.AugmentSecondPassShiftEntries,
		"augment_second_pass_reduce_only":     s.AugmentSecondPassReduceOnly,
		"augment_states_without_shift_target": s.AugmentStatesWithoutShiftTarget,
	}
}

func (s conflictResolutionStats) actionTraceFields() map[string]any {
	return map[string]any{
		"states_scanned":                     s.StatesScanned,
		"action_entries_scanned":             s.ActionEntriesScanned,
		"conflicts_resolved":                 s.ConflictsResolved,
		"max_actions_per_conflict":           s.MaxActionsPerConflict,
		"rhs_begin_calls":                    s.RHSBeginCalls,
		"rhs_begin_hits":                     s.RHSBeginHits,
		"rhs_begin_misses":                   s.RHSBeginMisses,
		"rhs_end_calls":                      s.RHSEndCalls,
		"rhs_continue_calls":                 s.RHSContinueCalls,
		"repeat_helper_reduce_shift_calls":   s.RepeatHelperReduceShiftCalls,
		"repeat_helper_continue_calls":       s.RepeatHelperContinueCalls,
		"parent_suffix_lookahead_calls":      s.ParentSuffixLookaheadCalls,
		"conflict_group_shift_reduce_calls":  s.ConflictGroupShiftReduceCalls,
		"conflict_group_shift_reduce_hits":   s.ConflictGroupShiftReduceHits,
		"conflict_group_shift_reduce_misses": s.ConflictGroupShiftReduceMisses,
		"conflict_group_reduce_lhs_calls":    s.ConflictGroupReduceLHSCalls,
		"conflict_group_reduce_lhs_hits":     s.ConflictGroupReduceLHSHits,
		"conflict_group_reduce_lhs_misses":   s.ConflictGroupReduceLHSMisses,
	}
}

func newPhaseTrace(g *Grammar) phaseTrace {
	if os.Getenv(grammargenTracePhasesEnv) != "1" {
		return phaseTrace{}
	}
	name := ""
	if g != nil {
		name = g.Name
	}
	return phaseTrace{enabled: true, name: traceValue(name)}
}

func (t phaseTrace) start(phase string, fields map[string]any) func(map[string]any) {
	if !t.enabled {
		return func(map[string]any) {}
	}
	start := time.Now()
	t.log(phase, "start", 0, fields)
	return func(endFields map[string]any) {
		t.log(phase, "end", time.Since(start), endFields)
	}
}

func (t phaseTrace) log(phase, event string, dur time.Duration, fields map[string]any) {
	fmt.Fprintf(os.Stderr, "GRAMMARGEN_PHASE name=%s phase=%s event=%s", t.name, traceValue(phase), event)
	if dur > 0 {
		fmt.Fprintf(os.Stderr, " dur=%s", dur)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(os.Stderr, " %s=%v", traceValue(key), fields[key])
	}
	fmt.Fprintln(os.Stderr)
}

func traceValue(s string) string {
	if s == "" {
		return "-"
	}
	replacer := strings.NewReplacer(
		" ", "_",
		"\t", "_",
		"\n", "_",
		"\r", "_",
		"=", "_",
	)
	return replacer.Replace(s)
}

func (t phaseTrace) grammarCounters(g *Grammar) map[string]any {
	if !t.enabled {
		return nil
	}
	if g == nil {
		return nil
	}
	return map[string]any{
		"rules":     len(g.RuleOrder),
		"extras":    len(g.Extras),
		"externals": len(g.Externals),
		"conflicts": len(g.Conflicts),
	}
}

func (t phaseTrace) normalizedCounters(ng *NormalizedGrammar) map[string]any {
	if !t.enabled {
		return nil
	}
	if ng == nil {
		return nil
	}
	return map[string]any{
		"symbols":     len(ng.Symbols),
		"productions": len(ng.Productions),
		"terminals":   len(ng.Terminals),
		"extras":      len(ng.ExtraSymbols),
	}
}

func (t phaseTrace) lrCounters(tables *LRTables) map[string]any {
	if !t.enabled {
		return nil
	}
	if tables == nil {
		return nil
	}
	actionEntries := 0
	actions := 0
	for _, bySym := range tables.ActionTable {
		actionEntries += len(bySym)
		for _, acts := range bySym {
			actions += len(acts)
		}
	}
	return map[string]any{
		"states":         tables.StateCount,
		"action_entries": actionEntries,
		"lr_actions":     actions,
	}
}
