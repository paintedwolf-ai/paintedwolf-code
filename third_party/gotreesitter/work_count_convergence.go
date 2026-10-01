//go:build gts_workcount

package gotreesitter

import (
	"fmt"
	"math"
)

const (
	workCountConvergenceCapability      = "go_only_detailed_convergence"
	workCountConvergenceMaxEvents       = 256
	workCountConvergenceMaxPackedPaths  = maxStacksPerMergeKey + 1
	workCountConvergencePathParseBudget = 16 * 1024
	workCountConvergencePathWalkBudget  = 256
	workCountConvergencePathDepthBudget = 128
)

var workCountConvergencePhases = [...]string{
	workCountConvergencePhaseReduceWindowSelect,
	workCountConvergencePhasePostReducePrimary,
	workCountConvergencePhasePostReducePending,
	workCountConvergencePhaseBoundaryGSS,
	workCountConvergencePhaseBoundaryEquivalence,
	workCountConvergencePhaseBoundaryCull,
	workCountConvergencePhasePendingWork,
	workCountConvergencePhaseFinalExpand,
	workCountConvergencePhaseFinalSelect,
	workCountConvergencePhaseTerminalAccept,
}

var workCountConvergenceOutcomes = [...]string{
	workCountConvergenceOutcomeCandidate,
	workCountConvergenceOutcomePacked,
	workCountConvergenceOutcomeRejected,
	workCountConvergenceOutcomePendingQueued,
	workCountConvergenceOutcomePendingDrained,
	workCountConvergenceOutcomePendingDiscarded,
	workCountConvergenceOutcomeAcceptedHead,
	workCountConvergenceOutcomePackedRootEmitted,
	workCountConvergenceOutcomeMutationPartial,
	workCountConvergenceOutcomeCapHit,
	workCountConvergenceOutcomeObservationUnknown,
	workCountConvergenceOutcomeSelected,
}

var workCountConvergenceRejectReasons = [...]string{
	workCountConvergenceReasonStatus,
	workCountConvergenceReasonScore,
	workCountConvergenceReasonShifted,
	workCountConvergenceReasonErrorCost,
	workCountConvergenceReasonNotGSS,
	workCountConvergenceReasonNotClean,
	workCountConvergenceReasonDistinctShape,
	workCountConvergenceReasonPreflight,
	workCountConvergenceReasonCull,
}

// DiagnosticWorkCountConvergence is a bounded Go-only convergence ledger.
// It is additive to the gts-work-count/v2 Go/static-C counter vector; static C
// does not emit comparable events.
type DiagnosticWorkCountConvergence struct {
	Capability            string `json:"capability"`
	MaxEvents             uint16 `json:"max_events"`
	MaxPackedPathCount    uint16 `json:"max_packed_path_count"`
	EventsSeen            uint64 `json:"events_seen"`
	EventTruncated        bool   `json:"event_truncated"`
	ArithmeticOverflow    bool   `json:"arithmetic_overflow"`
	VocabularyViolation   bool   `json:"vocabulary_violation"`
	SnapshotCountCapped   bool   `json:"snapshot_count_capped"`
	SnapshotCountUnknown  bool   `json:"snapshot_count_unknown"`
	PathWalkWorkBudget    uint32 `json:"path_walk_work_budget"`
	PathWalkWorkUsed      uint32 `json:"path_walk_work_used"`
	PathWalkWorkExhausted bool   `json:"path_walk_work_exhausted"`

	Events       []DiagnosticWorkCountConvergenceEvent `json:"events"`
	FirstRejects []DiagnosticWorkCountConvergenceEvent `json:"first_rejects"`
	Aggregates   DiagnosticWorkCountConvergenceTotals  `json:"aggregates"`
}

type DiagnosticWorkCountConvergenceTotals struct {
	Phases     DiagnosticWorkCountConvergencePhaseTotals   `json:"phases"`
	Outcomes   DiagnosticWorkCountConvergenceOutcomeTotals `json:"outcomes"`
	Rejections DiagnosticWorkCountConvergenceRejectTotals  `json:"rejections"`

	CandidateCountBeforeTotal uint64 `json:"candidate_count_before_total"`
	CandidateCountAfterTotal  uint64 `json:"candidate_count_after_total"`
	LinksBeforeTotal          uint64 `json:"links_before_total"`
	LinksAfterTotal           uint64 `json:"links_after_total"`
	PathsBeforeTotal          uint64 `json:"packed_paths_before_total"`
	PathsAfterTotal           uint64 `json:"packed_paths_after_total"`

	MutationPartial uint64 `json:"mutation_partial"`
	CapHits         uint64 `json:"cap_hits"`
	AcceptedHeads   uint64 `json:"accepted_heads"`
	PackedRoots     uint64 `json:"packed_roots_emitted"`
}

type DiagnosticWorkCountConvergencePhaseTotals struct {
	ReduceWindowSelect  uint64 `json:"reduce_window_select"`
	PostReducePrimary   uint64 `json:"post_reduce_primary"`
	PostReducePending   uint64 `json:"post_reduce_pending"`
	BoundaryGSS         uint64 `json:"boundary_gss"`
	BoundaryEquivalence uint64 `json:"boundary_equivalence"`
	BoundaryCull        uint64 `json:"boundary_cull"`
	PendingWork         uint64 `json:"pending_work"`
	FinalExpand         uint64 `json:"final_expand"`
	FinalSelect         uint64 `json:"final_select"`
	TerminalAccept      uint64 `json:"terminal_accept"`
}

type DiagnosticWorkCountConvergenceOutcomeTotals struct {
	Candidate          uint64 `json:"candidate"`
	Packed             uint64 `json:"packed"`
	Rejected           uint64 `json:"rejected"`
	PendingQueued      uint64 `json:"pending_queued"`
	PendingDrained     uint64 `json:"pending_drained"`
	PendingDiscarded   uint64 `json:"pending_discarded"`
	AcceptedHead       uint64 `json:"accepted_head"`
	PackedRoot         uint64 `json:"packed_root_emitted"`
	MutationPartial    uint64 `json:"mutation_partial"`
	CapHit             uint64 `json:"cap_hit"`
	ObservationUnknown uint64 `json:"observation_unknown"`
	Selected           uint64 `json:"selected"`
}

type DiagnosticWorkCountConvergenceRejectTotals struct {
	Status        uint64 `json:"status"`
	Score         uint64 `json:"score"`
	Shifted       uint64 `json:"shifted"`
	ErrorCost     uint64 `json:"error_cost"`
	NotGSS        uint64 `json:"not_gss"`
	NotClean      uint64 `json:"not_clean"`
	DistinctShape uint64 `json:"distinct_shape"`
	Preflight     uint64 `json:"preflight"`
	Cull          uint64 `json:"cull"`
}

type DiagnosticWorkCountConvergenceEvent struct {
	Sequence   uint64 `json:"sequence"`
	Attempt    uint32 `json:"attempt"`
	Iteration  uint64 `json:"iteration"`
	DecisionID uint32 `json:"decision_id"`

	LookaheadSymbol      uint32 `json:"lookahead_symbol"`
	LookaheadStartByte   uint32 `json:"lookahead_start_byte"`
	LookaheadEndByte     uint32 `json:"lookahead_end_byte"`
	LookaheadPresent     bool   `json:"lookahead_present"`
	LookaheadNoLookahead bool   `json:"lookahead_no_lookahead"`
	LookaheadExternal    bool   `json:"lookahead_external"`

	ElectionScannerComparable        bool   `json:"election_scanner_comparable"`
	ElectionScannerCheckpointPresent bool   `json:"election_scanner_checkpoint_present"`
	ElectionScannerCheckpointHash    uint64 `json:"election_scanner_checkpoint_hash"`

	Phase        string `json:"phase"`
	Outcome      string `json:"outcome"`
	RejectReason string `json:"reject_reason,omitempty"`
	Detail       string `json:"detail,omitempty"`

	Target    DiagnosticWorkCountConvergenceHead `json:"target"`
	Candidate DiagnosticWorkCountConvergenceHead `json:"candidate"`

	CandidateCountBefore uint32 `json:"candidate_count_before"`
	CandidateCountAfter  uint32 `json:"candidate_count_after"`
	LinksBefore          uint16 `json:"links_before"`
	LinksAfter           uint16 `json:"links_after"`
	PathsBefore          uint16 `json:"packed_paths_before"`
	PathsAfter           uint16 `json:"packed_paths_after"`

	MutationPartial bool `json:"mutation_partial"`
	CapHit          bool `json:"cap_hit"`
	SnapshotCapped  bool `json:"snapshot_capped"`
	SnapshotUnknown bool `json:"snapshot_unknown"`
}

type DiagnosticWorkCountConvergenceHead struct {
	State               uint32 `json:"state"`
	Byte                uint32 `json:"byte"`
	Status              string `json:"status"`
	HasError            bool   `json:"has_error"`
	Recovering          bool   `json:"recovering"`
	Shifted             bool   `json:"shifted"`
	Score               int64  `json:"score"`
	BranchOrder         uint64 `json:"branch_order"`
	Depth               uint32 `json:"depth"`
	Dead                bool   `json:"dead"`
	Accepted            bool   `json:"accepted"`
	Paused              bool   `json:"paused"`
	NodeBaseline        uint32 `json:"node_baseline"`
	ErrorCost           uint32 `json:"error_cost"`
	ErrorCostComparable bool   `json:"error_cost_comparable"`
	HasGSS              bool   `json:"has_gss"`
	LinkCount           uint16 `json:"link_count"`
	LinkCountCapped     bool   `json:"link_count_capped"`
	HeadContentHash     uint64 `json:"head_content_hash"`
	PredecessorHash     uint64 `json:"predecessor_content_hash"`
}

type workCountConvergenceDecisionKey struct {
	iteration         uint64
	target            *glrStack
	candidate         *glrStack
	candidateBranch   uint64
	candidateHeadHash uint64
	candidateState    StateID
	candidateByte     uint32
	candidateScore    int
}

const workCountConvergenceDecisionSlots = workCountConvergenceMaxEvents

var activeWorkCountConvergence struct {
	attempt           uint32
	iteration         uint64
	electionOrdinal   uint64
	lookahead         Token
	lookaheadPresent  bool
	nextDecision      uint32
	pathWorkRemaining uint32
	decisionKeys      [workCountConvergenceDecisionSlots]workCountConvergenceDecisionKey
	decisionIDs       [workCountConvergenceDecisionSlots]uint32
	decisionCursor    uint16
	mutationEpoch     uint64
	firstReject       [len(workCountConvergenceRejectReasons)]bool
}

func workCountBeginConvergence(c *DiagnosticWorkCount) {
	activeWorkCountConvergence = struct {
		attempt           uint32
		iteration         uint64
		electionOrdinal   uint64
		lookahead         Token
		lookaheadPresent  bool
		nextDecision      uint32
		pathWorkRemaining uint32
		decisionKeys      [workCountConvergenceDecisionSlots]workCountConvergenceDecisionKey
		decisionIDs       [workCountConvergenceDecisionSlots]uint32
		decisionCursor    uint16
		mutationEpoch     uint64
		firstReject       [len(workCountConvergenceRejectReasons)]bool
	}{}
	if c == nil {
		return
	}
	c.Convergence.Capability = workCountConvergenceCapability
	c.Convergence.MaxEvents = workCountConvergenceMaxEvents
	c.Convergence.MaxPackedPathCount = workCountConvergenceMaxPackedPaths
	c.Convergence.PathWalkWorkBudget = workCountConvergencePathParseBudget
	activeWorkCountConvergence.pathWorkRemaining = workCountConvergencePathParseBudget
}

func workCountResetConvergenceAttempt(index uint32) {
	activeWorkCountConvergence.attempt = index
	activeWorkCountConvergence.iteration = 0
	activeWorkCountConvergence.electionOrdinal = 0
	activeWorkCountConvergence.lookahead = Token{}
	activeWorkCountConvergence.lookaheadPresent = false
	activeWorkCountConvergence.nextDecision = 0
	activeWorkCountConvergence.decisionKeys = [workCountConvergenceDecisionSlots]workCountConvergenceDecisionKey{}
	activeWorkCountConvergence.decisionIDs = [workCountConvergenceDecisionSlots]uint32{}
	activeWorkCountConvergence.decisionCursor = 0
}

func workCountEndConvergence() {
	activeWorkCountConvergence = struct {
		attempt           uint32
		iteration         uint64
		electionOrdinal   uint64
		lookahead         Token
		lookaheadPresent  bool
		nextDecision      uint32
		pathWorkRemaining uint32
		decisionKeys      [workCountConvergenceDecisionSlots]workCountConvergenceDecisionKey
		decisionIDs       [workCountConvergenceDecisionSlots]uint32
		decisionCursor    uint16
		mutationEpoch     uint64
		firstReject       [len(workCountConvergenceRejectReasons)]bool
	}{}
}

func workCountSetConvergenceIteration(iteration int) {
	if iteration < 0 {
		workCountConvergenceVocabularyViolation()
		return
	}
	activeWorkCountConvergence.iteration = uint64(iteration)
}

func workCountConvergenceActive() bool {
	return activeDiagnosticWorkCount != nil
}

func workCountSetConvergenceLookahead(tok Token) {
	if activeWorkCountConvergence.electionOrdinal == math.MaxUint64 {
		if c := activeDiagnosticWorkCount; c != nil {
			c.Convergence.ArithmeticOverflow = true
			c.Overflow = true
		}
	} else {
		activeWorkCountConvergence.electionOrdinal++
	}
	activeWorkCountConvergence.lookahead = tok
	activeWorkCountConvergence.lookaheadPresent = true
}

// workCountRefreshConvergenceLookahead replaces the token attached to the
// current election without advancing its ordinal. Relexing changes the
// decision input, not the identity of the parser election that requested it.
func workCountRefreshConvergenceLookahead(tok Token) {
	activeWorkCountConvergence.lookahead = tok
	activeWorkCountConvergence.lookaheadPresent = true
}

func workCountConvergenceShapeOf(stack *glrStack) workCountConvergenceShape {
	if stack == nil {
		return workCountConvergenceShape{}
	}
	if stack.gss.head == nil {
		return workCountConvergenceShape{PackedPaths: 1}
	}
	links, linksCapped := workCountBoundedLinkCount(stack.gss.head.linkCount())
	shape := workCountConvergenceShape{Links: links, LinkCountCapped: linksCapped}
	paths, capped, cycle, budgetHit := workCountCountPackedPaths(stack.gss.head, workCountConvergenceMaxPackedPaths)
	shape.PackedPaths = paths
	shape.PathCountCapped = capped
	shape.PathCountUnknown = budgetHit || cycle
	shape.CycleSeen = cycle
	return shape
}

func workCountCountPackedPaths(head *gssNode, limit uint16) (uint16, bool, bool, bool) {
	if limit == 0 || limit > workCountConvergenceMaxPackedPaths {
		limit = workCountConvergenceMaxPackedPaths
	}
	if activeWorkCountConvergence.pathWorkRemaining == 0 {
		if c := activeDiagnosticWorkCount; c != nil {
			c.Convergence.PathWalkWorkExhausted = true
		}
		return limit, false, false, true
	}
	visiting := make(map[*gssNode]bool)
	capped := false
	cycle := false
	budgetHit := false
	workRemaining := uint32(workCountConvergencePathWalkBudget)
	if activeWorkCountConvergence.pathWorkRemaining < workRemaining {
		workRemaining = activeWorkCountConvergence.pathWorkRemaining
	}
	var count func(*gssNode, int) uint16
	count = func(node *gssNode, depth int) uint16 {
		if node == nil {
			return 1
		}
		if depth >= workCountConvergencePathDepthBudget || workRemaining == 0 {
			budgetHit = true
			if c := activeDiagnosticWorkCount; c != nil {
				c.Convergence.PathWalkWorkExhausted = true
			}
			return limit
		}
		workRemaining--
		activeWorkCountConvergence.pathWorkRemaining--
		if c := activeDiagnosticWorkCount; c != nil {
			c.Convergence.PathWalkWorkUsed++
		}
		if visiting[node] {
			cycle = true
			return 0
		}
		visiting[node] = true
		var total uint16
		for i, links := 0, node.linkCount(); i < links; i++ {
			prev, _ := node.link(i)
			value := count(prev, depth+1)
			if value >= limit || total >= limit-value {
				total = limit
				if !budgetHit {
					capped = true
				}
				break
			}
			total += value
		}
		delete(visiting, node)
		return total
	}
	return count(head, 0), capped, cycle, budgetHit
}

// workCountConvergencePackedResultExpansion measures a pre-expansion source.
// capHit is true only when the bounded walk proves at least limit+1 paths.
func workCountConvergencePackedResultExpansion(stack *glrStack, limit int) (emitted int, capHit, unknown bool) {
	if stack == nil || stack.gss.head == nil || limit <= 0 {
		return 0, false, false
	}
	want := limit + 1
	if want > workCountConvergenceMaxPackedPaths {
		want = workCountConvergenceMaxPackedPaths
	}
	paths, _, cycle, budgetHit := workCountCountPackedPaths(stack.gss.head, uint16(want))
	if cycle || budgetHit {
		return 0, false, true
	}
	emitted = int(paths)
	if emitted > limit {
		emitted = limit
		capHit = true
	}
	return emitted, capHit, false
}

func workCountRecordPairCandidate(p *Parser, phase, detail string, target, candidate *glrStack) {
	if !workCountConvergenceActive() {
		return
	}
	shape := workCountConvergenceShapeOf(target)
	workCountRecordConvergence(p, phase, workCountConvergenceOutcomeCandidate, workCountConvergenceReasonNone, detail, target, candidate, 2, 2, shape, shape, false, false)
}

func workCountRecordGSSReject(p *Parser, phase, reason, detail string, target, candidate *glrStack) {
	if !workCountConvergenceActive() {
		return
	}
	shape := workCountConvergenceShapeOf(target)
	workCountRecordConvergence(p, phase, workCountConvergenceOutcomeRejected, reason, detail, target, candidate, 2, 2, shape, shape, false, false)
}

func workCountRecordGSSScoreShiftReject(p *Parser, phase string, target, candidate *glrStack) {
	if !workCountConvergenceActive() || target == nil || candidate == nil {
		return
	}
	if target.score != candidate.score {
		workCountRecordGSSReject(p, phase, workCountConvergenceReasonScore, "GSS merge score differs", target, candidate)
		return
	}
	if target.shifted != candidate.shifted {
		workCountRecordGSSReject(p, phase, workCountConvergenceReasonShifted, "GSS merge shifted status differs", target, candidate)
	}
}

func workCountRecordGSSCleanReject(p *Parser, phase string, target, candidate *glrStack, clean bool) {
	if clean || !workCountConvergenceActive() {
		return
	}
	workCountRecordGSSReject(p, phase, workCountConvergenceReasonNotClean, "GSS merge requires clean zero-error paths", target, candidate)
}

func workCountGSSMutationEpoch() uint64 {
	return activeWorkCountConvergence.mutationEpoch
}

func workCountRecordGSSMutation() {
	if !workCountConvergenceActive() {
		return
	}
	if activeWorkCountConvergence.mutationEpoch == math.MaxUint64 {
		activeDiagnosticWorkCount.Convergence.ArithmeticOverflow = true
		activeDiagnosticWorkCount.Overflow = true
		return
	}
	activeWorkCountConvergence.mutationEpoch++
}

// workCountMergeGSSObserved performs the production merge in both build
// variants. The tagged variant additionally records its bounded before/after
// shape without making core call sites own diagnostic assembly.
func workCountMergeGSSObserved(p *Parser, scratch *glrMergeScratch, phase, detail string, target, candidate *glrStack) bool {
	if !workCountConvergenceActive() {
		return gssMainMergeWithScratch(scratch, target, candidate)
	}
	before := workCountConvergenceShapeOf(target)
	mutationBefore := workCountGSSMutationEpoch()
	merged := gssMainMergeWithScratch(scratch, target, candidate)
	workCountRecordGSSMergeResult(p, phase, detail, target, candidate, before, mutationBefore, merged)
	return merged
}

func workCountRecordGSSMergeResult(p *Parser, phase, detail string, target, candidate *glrStack, before workCountConvergenceShape, mutationBefore uint64, merged bool) {
	mutated := workCountGSSMutationEpoch() != mutationBefore
	after := workCountConvergenceShapeOf(target)
	outcome := workCountConvergenceOutcomePacked
	reason := workCountConvergenceReasonNone
	detailSuffix := " packed candidate"
	partial := !merged && mutated
	if !merged {
		outcome = workCountConvergenceOutcomeRejected
		reason = workCountConvergenceReasonPreflight
		detailSuffix = " preflight declined candidate"
		if partial {
			outcome = workCountConvergenceOutcomeMutationPartial
			reason = workCountConvergenceReasonNone
			detailSuffix = " mutation changed shape without absorbing every link"
		}
	}
	eventDetail := ""
	if workCountConvergenceRetainsDetail(reason) {
		eventDetail = detail + detailSuffix
	}
	afterCount := 2
	if merged {
		afterCount = 1
	}
	workCountRecordConvergence(p, phase, outcome, reason, eventDetail, target, candidate, 2, afterCount, before, after, partial, false)
}

func workCountConvergenceRetainsDetail(reason string) bool {
	c := activeDiagnosticWorkCount
	if c == nil {
		return false
	}
	if len(c.Convergence.Events) < workCountConvergenceMaxEvents {
		return true
	}
	reasonIndex := workCountConvergenceRejectReasonIndex(reason)
	return reasonIndex >= 0 && !activeWorkCountConvergence.firstReject[reasonIndex]
}

func workCountRecordPostReduceCandidate(p *Parser, phase, detail string, target, candidate *glrStack, rejectReason string) {
	if !workCountConvergenceActive() {
		return
	}
	shape := workCountConvergenceShapeOf(target)
	workCountRecordConvergence(p, phase, workCountConvergenceOutcomeCandidate, workCountConvergenceReasonNone, detail, target, candidate, 2, 2, shape, shape, false, false)
	if rejectReason != workCountConvergenceReasonNone {
		workCountRecordConvergence(p, phase, workCountConvergenceOutcomeRejected, rejectReason, detail, target, candidate, 2, 2, shape, shape, false, false)
	}
}

func workCountRecordPendingQueued(p *Parser, target, candidate *glrStack, before, after int, detail string) {
	if !workCountConvergenceActive() {
		return
	}
	shape := workCountConvergenceShapeOf(candidate)
	workCountRecordConvergence(p, workCountConvergencePhasePendingWork, workCountConvergenceOutcomePendingQueued, workCountConvergenceReasonNone, detail, target, candidate, before, after, shape, shape, false, false)
}

func workCountRecordAcceptedHead(p *Parser, stack *glrStack, detail string) {
	if !workCountConvergenceActive() {
		return
	}
	shape := workCountConvergenceShapeOf(stack)
	workCountRecordConvergence(p, workCountConvergencePhaseTerminalAccept, workCountConvergenceOutcomeAcceptedHead, workCountConvergenceReasonNone, detail, stack, nil, 1, 1, shape, shape, false, false)
}

func workCountRecordReduceWindow(p *Parser, stack *glrStack, paths, work, budget int, capped bool) {
	if !workCountConvergenceActive() {
		return
	}
	outcome := workCountConvergenceOutcomeCandidate
	if capped {
		outcome = workCountConvergenceOutcomeCapHit
	}
	shape := workCountConvergenceShapeOf(stack)
	detail := ""
	if activeDiagnosticWorkCount != nil && len(activeDiagnosticWorkCount.Convergence.Events) < workCountConvergenceMaxEvents {
		detail = fmt.Sprintf("reduce-window paths=%d work=%d budget=%d", paths, work, budget)
	}
	workCountRecordConvergence(p, workCountConvergencePhaseReduceWindowSelect, outcome, workCountConvergenceReasonNone, detail, stack, nil, 1, paths, shape, shape, false, capped)
}

func workCountRecordPendingTransition(p *Parser, stack *glrStack, before, after int, outcome, detail string) {
	if !workCountConvergenceActive() {
		return
	}
	shape := workCountConvergenceShapeOf(stack)
	workCountRecordConvergence(p, workCountConvergencePhasePendingWork, outcome, workCountConvergenceReasonNone, detail, stack, nil, before, after, shape, shape, false, false)
}

func workCountRecordFinalPendingDiscards(p *Parser, pending, frontier []glrStack) {
	if !workCountConvergenceActive() {
		return
	}
	if len(pending) != 0 {
		workCountRecordPendingTransition(p, &pending[0], len(pending), 0, workCountConvergenceOutcomePendingDiscarded, "finalization excludes undrained post-reduce candidates")
	}
	if len(frontier) != 0 {
		workCountRecordPendingTransition(p, &frontier[0], len(frontier), 0, workCountConvergenceOutcomePendingDiscarded, "finalization excludes undrained frontier candidates")
	}
}

func workCountRecordBoundaryCull(p *Parser, before, after int) {
	if !workCountConvergenceActive() || before <= after {
		return
	}
	workCountRecordConvergence(p, workCountConvergencePhaseBoundaryCull, workCountConvergenceOutcomeRejected, workCountConvergenceReasonCull, "global stack cap removed boundary candidates", nil, nil, before, after, workCountConvergenceShape{}, workCountConvergenceShape{}, false, true)
}

func workCountRecordFinalExpand(p *Parser, stack *glrStack, emitted int, capHit, unknown bool) {
	if !workCountConvergenceActive() {
		return
	}
	links, linksCapped := uint16(0), false
	if stack != nil && stack.gss.head != nil {
		links, linksCapped = workCountBoundedLinkCount(stack.gss.head.linkCount())
	}
	before := workCountConvergenceShape{Links: links, LinkCountCapped: linksCapped, PackedPaths: uint16(emitted), PathCountUnknown: unknown}
	after := before
	if unknown {
		workCountRecordConvergence(p, workCountConvergencePhaseFinalExpand, workCountConvergenceOutcomeObservationUnknown, workCountConvergenceReasonNone, "packed result root expansion count unknown", stack, nil, 1, 0, before, after, false, false)
		return
	}
	if capHit {
		before.PackedPaths++
		before.PathCountCapped = true
	}
	after.PackedPaths = uint16(emitted)
	after.PathCountCapped = false
	workCountRecordConvergence(p, workCountConvergencePhaseFinalExpand, workCountConvergenceOutcomePackedRootEmitted, workCountConvergenceReasonNone, fmt.Sprintf("packed result root emitted %d alternatives", emitted), stack, nil, 1, emitted, before, after, false, capHit)
}

func workCountRecordFinalExpansions(p *Parser, stacks []glrStack) {
	if !workCountConvergenceActive() {
		return
	}
	for i := range stacks {
		stack := &stacks[i]
		if len(stack.entries) > 0 || stack.gss.head == nil || !gssInlineChainHasPackedLinks(stack.gss.head) {
			continue
		}
		emitted, capHit, unknown := workCountConvergencePackedResultExpansion(stack, maxStacksPerMergeKey)
		workCountRecordFinalExpand(p, stack, emitted, capHit, unknown)
	}
}

func workCountRecordFinalSelect(p *Parser, stacks []glrStack, selected *glrStack) {
	if !workCountConvergenceActive() {
		return
	}
	shape := workCountConvergenceShapeOf(selected)
	workCountRecordConvergence(p, workCountConvergencePhaseFinalSelect, workCountConvergenceOutcomeSelected, workCountConvergenceReasonNone, "result head selected", selected, nil, len(stacks), 1, shape, shape, false, false)
}

// workCountTryPostReduceMergeObserved performs the real post-reduce merge in
// both build variants and records a successful pack only in tagged runs.
func workCountTryPostReduceMergeObserved(p *Parser, phase, detail string, target, candidate *glrStack) bool {
	if reason := postReduceForkMergePreflight(target, candidate); reason != workCountConvergenceReasonNone {
		workCountRecordGSSReject(p, phase, reason, "post-reduce merge preflight rejected candidate", target, candidate)
		return false
	}
	return tryGSSMainMergeForParserPhase(p, target, candidate, phase, false)
}

func workCountRecordConvergence(p *Parser, phase, outcome, reason, detail string, target, candidate *glrStack, candidateCountBefore, candidateCountAfter int, before, after workCountConvergenceShape, partialMutation, capHit bool) {
	c := activeDiagnosticWorkCount
	if c == nil {
		return
	}
	if !workCountConvergenceStringIn(phase, workCountConvergencePhases[:]) ||
		!workCountConvergenceStringIn(outcome, workCountConvergenceOutcomes[:]) ||
		(reason != workCountConvergenceReasonNone && !workCountConvergenceStringIn(reason, workCountConvergenceRejectReasons[:])) ||
		(outcome == workCountConvergenceOutcomeRejected) != (reason != workCountConvergenceReasonNone) {
		workCountConvergenceVocabularyViolation()
		return
	}
	semanticPhaseTraceRecordDecision(p, phase, outcome, reason, target, candidate, candidateCountBefore, candidateCountAfter)
	if activeWorkCountConvergence.attempt == 0 {
		workCountConvergenceVocabularyViolation()
		return
	}

	sequence := c.Convergence.EventsSeen
	workCountConvergenceSaturatingAdd(c, &c.Convergence.EventsSeen, 1)
	if sequence != math.MaxUint64 {
		sequence++
	}
	candidateCountBeforeValue, candidateCountBeforeCapped := workCountBoundedUint32(candidateCountBefore)
	candidateCountAfterValue, candidateCountAfterCapped := workCountBoundedUint32(candidateCountAfter)
	snapshotCapped := before.PathCountCapped || after.PathCountCapped || before.LinkCountCapped || after.LinkCountCapped || candidateCountBeforeCapped || candidateCountAfterCapped
	snapshotUnknown := before.PathCountUnknown || after.PathCountUnknown || before.CycleSeen || after.CycleSeen
	if snapshotCapped {
		c.Convergence.SnapshotCountCapped = true
	}
	if snapshotUnknown {
		c.Convergence.SnapshotCountUnknown = true
	}

	reasonIndex := workCountConvergenceRejectReasonIndex(reason)
	retainChronological := len(c.Convergence.Events) < workCountConvergenceMaxEvents
	retainFirstReject := reasonIndex >= 0 && !activeWorkCountConvergence.firstReject[reasonIndex]
	event := DiagnosticWorkCountConvergenceEvent{
		Sequence: sequence, Attempt: activeWorkCountConvergence.attempt,
		Iteration: activeWorkCountConvergence.iteration,
		Phase:     phase, Outcome: outcome, RejectReason: reason, Detail: detail,
		CandidateCountBefore: candidateCountBeforeValue, CandidateCountAfter: candidateCountAfterValue,
		LinksBefore: before.Links, LinksAfter: after.Links,
		PathsBefore: before.PackedPaths, PathsAfter: after.PackedPaths,
		MutationPartial: partialMutation, CapHit: capHit,
		SnapshotCapped: snapshotCapped, SnapshotUnknown: snapshotUnknown,
	}
	if retainChronological || retainFirstReject {
		event.DecisionID = workCountConvergenceDecisionID(c, target, candidate)
		event.LookaheadSymbol = uint32(activeWorkCountConvergence.lookahead.Symbol)
		event.LookaheadStartByte = activeWorkCountConvergence.lookahead.StartByte
		event.LookaheadEndByte = activeWorkCountConvergence.lookahead.EndByte
		event.LookaheadPresent = activeWorkCountConvergence.lookaheadPresent
		event.LookaheadNoLookahead = activeWorkCountConvergence.lookahead.NoLookahead
		event.LookaheadExternal = activeWorkCountConvergence.lookahead.ExternalScannerToken
		if p != nil {
			event.ElectionScannerComparable = p.language == nil || p.language.ExternalScanner == nil || p.currentExternalTokenCheckpointValid
			if p.currentExternalTokenCheckpointValid {
				event.ElectionScannerCheckpointPresent = true
				event.ElectionScannerCheckpointHash = workCountCheckpointHash(p.currentExternalTokenCheckpoint)
			}
		}
		event.Target = workCountConvergenceHead(p, target)
		event.Candidate = workCountConvergenceHead(p, candidate)
		if event.Target.LinkCountCapped || event.Candidate.LinkCountCapped {
			event.SnapshotCapped = true
			c.Convergence.SnapshotCountCapped = true
		}
	}
	workCountConvergenceAddAggregates(c, event)
	if retainChronological {
		c.Convergence.Events = append(c.Convergence.Events, event)
	} else {
		c.Convergence.EventTruncated = true
	}
	if retainFirstReject {
		activeWorkCountConvergence.firstReject[reasonIndex] = true
		c.Convergence.FirstRejects = append(c.Convergence.FirstRejects, event)
	}
}

func workCountConvergenceDecisionID(c *DiagnosticWorkCount, target, candidate *glrStack) uint32 {
	if target == nil && candidate == nil {
		return 0
	}
	key := workCountConvergenceDecisionKey{iteration: activeWorkCountConvergence.iteration, target: target, candidate: candidate}
	if candidate != nil {
		key.candidateBranch = candidate.branchOrder
		key.candidateByte = candidate.byteOffset
		key.candidateScore = candidate.score
		if candidate.depth() > 0 {
			key.candidateState = candidate.top().state
		}
		if candidate.gss.head != nil {
			key.candidateHeadHash = candidate.gss.head.hash
		}
	}
	for index, id := range activeWorkCountConvergence.decisionIDs {
		if id != 0 && activeWorkCountConvergence.decisionKeys[index] == key {
			return id
		}
	}
	if activeWorkCountConvergence.nextDecision == math.MaxUint32 {
		c.Convergence.ArithmeticOverflow = true
		c.Overflow = true
		return math.MaxUint32
	}
	activeWorkCountConvergence.nextDecision++
	slot := int(activeWorkCountConvergence.decisionCursor) % workCountConvergenceDecisionSlots
	activeWorkCountConvergence.decisionCursor++
	activeWorkCountConvergence.decisionKeys[slot] = key
	activeWorkCountConvergence.decisionIDs[slot] = activeWorkCountConvergence.nextDecision
	return activeWorkCountConvergence.nextDecision
}

func workCountConvergenceHead(p *Parser, stack *glrStack) DiagnosticWorkCountConvergenceHead {
	if stack == nil {
		return DiagnosticWorkCountConvergenceHead{Status: "absent"}
	}
	head := DiagnosticWorkCountConvergenceHead{
		Byte: stack.byteOffset, Status: "live", HasError: stack.cEverErrored,
		Recovering: stack.cPaused || stack.cRec != nil || stack.cRecoverMissingGroup != nil,
		Shifted:    stack.shifted, Score: int64(stack.score), BranchOrder: stack.branchOrder,
		Dead: stack.dead, Accepted: stack.accepted, Paused: stack.cPaused,
		NodeBaseline: stack.cNodeBaseline,
	}
	depth, _ := workCountBoundedUint32(stack.depth())
	head.Depth = depth
	if p != nil {
		head.ErrorCost = cStackErrorCostForMerge(p.language, stack)
		head.ErrorCostComparable = true
	}
	if stack.dead {
		head.Status = "dead"
	} else if stack.accepted {
		head.Status = "accepted"
	} else if stack.cPaused {
		head.Status = "paused"
	}
	if stack.depth() > 0 {
		head.State = uint32(stack.top().state)
	}
	if stack.gss.head != nil {
		head.HasGSS = true
		head.LinkCount, head.LinkCountCapped = workCountBoundedLinkCount(stack.gss.head.linkCount())
		head.HeadContentHash = stack.gss.head.hash
		if stack.gss.head.prev != nil {
			head.PredecessorHash = stack.gss.head.prev.hash
		}
	}
	return head
}

func workCountCheckpointHash(checkpoint externalScannerCheckpoint) uint64 {
	const (
		offset = uint64(1469598103934665603)
		prime  = uint64(1099511628211)
	)
	hash := offset
	for _, value := range checkpoint.start {
		hash ^= uint64(value)
		hash *= prime
	}
	hash ^= 0xff
	hash *= prime
	for _, value := range checkpoint.end {
		hash ^= uint64(value)
		hash *= prime
	}
	return hash
}

func workCountConvergenceAddAggregates(c *DiagnosticWorkCount, event DiagnosticWorkCountConvergenceEvent) {
	totals := &c.Convergence.Aggregates
	workCountConvergenceIncrementPhase(c, &totals.Phases, event.Phase)
	workCountConvergenceIncrementOutcome(c, &totals.Outcomes, event.Outcome)
	workCountConvergenceIncrementReject(c, &totals.Rejections, event.RejectReason)
	workCountConvergenceSaturatingAdd(c, &totals.CandidateCountBeforeTotal, uint64(event.CandidateCountBefore))
	workCountConvergenceSaturatingAdd(c, &totals.CandidateCountAfterTotal, uint64(event.CandidateCountAfter))
	workCountConvergenceSaturatingAdd(c, &totals.LinksBeforeTotal, uint64(event.LinksBefore))
	workCountConvergenceSaturatingAdd(c, &totals.LinksAfterTotal, uint64(event.LinksAfter))
	workCountConvergenceSaturatingAdd(c, &totals.PathsBeforeTotal, uint64(event.PathsBefore))
	workCountConvergenceSaturatingAdd(c, &totals.PathsAfterTotal, uint64(event.PathsAfter))
	if event.MutationPartial {
		workCountConvergenceSaturatingAdd(c, &totals.MutationPartial, 1)
	}
	if event.CapHit {
		workCountConvergenceSaturatingAdd(c, &totals.CapHits, 1)
	}
	if event.Outcome == workCountConvergenceOutcomeAcceptedHead {
		workCountConvergenceSaturatingAdd(c, &totals.AcceptedHeads, 1)
	}
	if event.Outcome == workCountConvergenceOutcomePackedRootEmitted {
		workCountConvergenceSaturatingAdd(c, &totals.PackedRoots, uint64(event.PathsAfter))
	}
}

func workCountConvergenceIncrementPhase(c *DiagnosticWorkCount, totals *DiagnosticWorkCountConvergencePhaseTotals, value string) {
	var slot *uint64
	switch value {
	case workCountConvergencePhaseReduceWindowSelect:
		slot = &totals.ReduceWindowSelect
	case workCountConvergencePhasePostReducePrimary:
		slot = &totals.PostReducePrimary
	case workCountConvergencePhasePostReducePending:
		slot = &totals.PostReducePending
	case workCountConvergencePhaseBoundaryGSS:
		slot = &totals.BoundaryGSS
	case workCountConvergencePhaseBoundaryEquivalence:
		slot = &totals.BoundaryEquivalence
	case workCountConvergencePhaseBoundaryCull:
		slot = &totals.BoundaryCull
	case workCountConvergencePhasePendingWork:
		slot = &totals.PendingWork
	case workCountConvergencePhaseFinalExpand:
		slot = &totals.FinalExpand
	case workCountConvergencePhaseFinalSelect:
		slot = &totals.FinalSelect
	case workCountConvergencePhaseTerminalAccept:
		slot = &totals.TerminalAccept
	}
	workCountConvergenceSaturatingAdd(c, slot, 1)
}

func workCountConvergenceIncrementOutcome(c *DiagnosticWorkCount, totals *DiagnosticWorkCountConvergenceOutcomeTotals, value string) {
	var slot *uint64
	switch value {
	case workCountConvergenceOutcomeCandidate:
		slot = &totals.Candidate
	case workCountConvergenceOutcomePacked:
		slot = &totals.Packed
	case workCountConvergenceOutcomeRejected:
		slot = &totals.Rejected
	case workCountConvergenceOutcomePendingQueued:
		slot = &totals.PendingQueued
	case workCountConvergenceOutcomePendingDrained:
		slot = &totals.PendingDrained
	case workCountConvergenceOutcomePendingDiscarded:
		slot = &totals.PendingDiscarded
	case workCountConvergenceOutcomeAcceptedHead:
		slot = &totals.AcceptedHead
	case workCountConvergenceOutcomePackedRootEmitted:
		slot = &totals.PackedRoot
	case workCountConvergenceOutcomeMutationPartial:
		slot = &totals.MutationPartial
	case workCountConvergenceOutcomeCapHit:
		slot = &totals.CapHit
	case workCountConvergenceOutcomeObservationUnknown:
		slot = &totals.ObservationUnknown
	case workCountConvergenceOutcomeSelected:
		slot = &totals.Selected
	}
	workCountConvergenceSaturatingAdd(c, slot, 1)
}

func workCountConvergenceIncrementReject(c *DiagnosticWorkCount, totals *DiagnosticWorkCountConvergenceRejectTotals, value string) {
	var slot *uint64
	switch value {
	case workCountConvergenceReasonStatus:
		slot = &totals.Status
	case workCountConvergenceReasonScore:
		slot = &totals.Score
	case workCountConvergenceReasonShifted:
		slot = &totals.Shifted
	case workCountConvergenceReasonErrorCost:
		slot = &totals.ErrorCost
	case workCountConvergenceReasonNotGSS:
		slot = &totals.NotGSS
	case workCountConvergenceReasonNotClean:
		slot = &totals.NotClean
	case workCountConvergenceReasonDistinctShape:
		slot = &totals.DistinctShape
	case workCountConvergenceReasonPreflight:
		slot = &totals.Preflight
	case workCountConvergenceReasonCull:
		slot = &totals.Cull
	}
	workCountConvergenceSaturatingAdd(c, slot, 1)
}

func workCountConvergenceSaturatingAdd(c *DiagnosticWorkCount, slot *uint64, delta uint64) {
	if c == nil || slot == nil || delta == 0 {
		return
	}
	if math.MaxUint64-*slot < delta {
		*slot = math.MaxUint64
		c.Convergence.ArithmeticOverflow = true
		c.Overflow = true
		return
	}
	*slot += delta
}

func workCountConvergenceVocabularyViolation() {
	if c := activeDiagnosticWorkCount; c != nil {
		c.Convergence.VocabularyViolation = true
	}
}

func workCountConvergenceStringIn(value string, values []string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func workCountConvergenceRejectReasonIndex(reason string) int {
	for index, candidate := range workCountConvergenceRejectReasons {
		if reason == candidate {
			return index
		}
	}
	return -1
}

func workCountBoundedUint32(value int) (uint32, bool) {
	if value < 0 {
		return 0, true
	}
	if uint64(value) > math.MaxUint32 {
		return math.MaxUint32, true
	}
	return uint32(value), false
}
