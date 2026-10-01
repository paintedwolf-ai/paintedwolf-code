package gotreesitter

// Closed convergence vocabulary is shared by production seam call sites and
// the gts_workcount recorder. Keeping it in one build-independent file avoids
// drift without putting diagnostic implementation into production builds.
const (
	workCountConvergencePhaseReduceWindowSelect  = "reduce_window_select"
	workCountConvergencePhasePostReducePrimary   = "post_reduce_primary"
	workCountConvergencePhasePostReducePending   = "post_reduce_pending"
	workCountConvergencePhaseBoundaryGSS         = "boundary_gss"
	workCountConvergencePhaseBoundaryEquivalence = "boundary_equivalence"
	workCountConvergencePhaseBoundaryCull        = "boundary_cull"
	workCountConvergencePhasePendingWork         = "pending_work"
	workCountConvergencePhaseFinalExpand         = "final_expand"
	workCountConvergencePhaseFinalSelect         = "final_select"
	workCountConvergencePhaseTerminalAccept      = "terminal_accept"

	workCountConvergenceOutcomeCandidate          = "candidate"
	workCountConvergenceOutcomePacked             = "packed"
	workCountConvergenceOutcomeRejected           = "rejected"
	workCountConvergenceOutcomePendingQueued      = "pending_queued"
	workCountConvergenceOutcomePendingDrained     = "pending_drained"
	workCountConvergenceOutcomePendingDiscarded   = "pending_discarded"
	workCountConvergenceOutcomeAcceptedHead       = "accepted_head"
	workCountConvergenceOutcomePackedRootEmitted  = "packed_root_emitted"
	workCountConvergenceOutcomeMutationPartial    = "mutation_partial"
	workCountConvergenceOutcomeCapHit             = "cap_hit"
	workCountConvergenceOutcomeObservationUnknown = "observation_unknown"
	workCountConvergenceOutcomeSelected           = "selected"

	workCountConvergenceReasonNone          = ""
	workCountConvergenceReasonStatus        = "status"
	workCountConvergenceReasonScore         = "score"
	workCountConvergenceReasonShifted       = "shifted"
	workCountConvergenceReasonErrorCost     = "error_cost"
	workCountConvergenceReasonNotGSS        = "not_gss"
	workCountConvergenceReasonNotClean      = "not_clean"
	workCountConvergenceReasonDistinctShape = "distinct_shape"
	workCountConvergenceReasonPreflight     = "preflight"
	workCountConvergenceReasonCull          = "cull"
)

type workCountConvergenceShape struct {
	Links            uint16
	LinkCountCapped  bool
	PackedPaths      uint16
	PathCountCapped  bool
	PathCountUnknown bool
	CycleSeen        bool
}

func workCountParserFromMergeScratch(scratch *glrMergeScratch) *Parser {
	if scratch == nil {
		return nil
	}
	return scratch.parser
}

func workCountBoundedLinkCount(value int) (uint16, bool) {
	if value < 0 {
		return 0, true
	}
	if uint64(value) > uint64(^uint16(0)) {
		return ^uint16(0), true
	}
	return uint16(value), false
}
