package evidence

// GateType names the workflow gate a gate record answers. The strings persist
// as Record.GateType.
type GateType string

const (
	GateTypeVerify           GateType = "verify"
	GateTypeReview           GateType = "review"
	GateTypeTest             GateType = "test"
	GateTypeSecurity         GateType = "security"
	GateTypePlanReview       GateType = "plan_review"
	GateTypePlanReviewAlt    GateType = "plan_review_alt"
	GateTypeSurveyClaims     GateType = "survey_claims"
	GateTypeSurveyChallenged GateType = "survey_challenged"
	GateTypeOptionsJudge     GateType = "options_judge"
)

var allGateTypes = []GateType{
	GateTypeVerify,
	GateTypeReview,
	GateTypeTest,
	GateTypeSecurity,
	GateTypePlanReview,
	GateTypePlanReviewAlt,
	GateTypeSurveyClaims,
	GateTypeSurveyChallenged,
	GateTypeOptionsJudge,
}

// AllGateTypes returns every GateType in declaration order.
func AllGateTypes() []GateType {
	return append([]GateType(nil), allGateTypes...)
}

// GateVerdict is the outcome a gate record states. The strings persist as
// Record.GateVerdict.
type GateVerdict string

const (
	GateVerdictPassed GateVerdict = "passed"
	GateVerdictFailed GateVerdict = "failed"
	// GateVerdictUnverifiable means the attempt reached no verdict about the
	// work: the boundary stopped the run, or it never produced one. Failed is
	// a result; this is its absence.
	GateVerdictUnverifiable           GateVerdict = "unverifiable"
	GateVerdictApproved               GateVerdict = "approved"
	GateVerdictNeedsChanges           GateVerdict = "needs_changes"
	GateVerdictNeedsChangesOverridden GateVerdict = "needs_changes_overridden"
)

// gateRecommendedAgents maps each gate type to the worker agent id that runs it.
// Model selection for subagents comes from model_policy.agent_pool.
var gateRecommendedAgents = map[GateType]string{
	GateTypeVerify:           "implementer",
	GateTypeTest:             "implementer",
	GateTypeSecurity:         "security-reviewer",
	GateTypeReview:           "code-reviewer",
	GateTypePlanReview:       "plan-reviewer",
	GateTypePlanReviewAlt:    "plan-reviewer-alt",
	GateTypeSurveyClaims:     "security-reviewer",
	GateTypeSurveyChallenged: "skeptic",
	GateTypeOptionsJudge:     "skeptic",
}

// GateRecommendedAgent returns the recommended worker agent id for a gate type.
func GateRecommendedAgent(gate GateType) (string, bool) {
	id, ok := gateRecommendedAgents[gate]
	return id, ok
}

// GateRecommendedAgents returns a copy of the gate type → agent id mapping.
func GateRecommendedAgents() map[GateType]string {
	out := make(map[GateType]string, len(gateRecommendedAgents))
	for gate, agentID := range gateRecommendedAgents {
		out[gate] = agentID
	}
	return out
}
