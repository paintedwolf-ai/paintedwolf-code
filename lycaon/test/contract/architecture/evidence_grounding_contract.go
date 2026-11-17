package contract

// evidenceGroundedWorkerSummaryCodes are the typed worker citation grounding
// codes (guard:worker_summary). Prose placement of an observed token is advisory
// and has no code here; see docs/grounding.md § Evidence-grounded prose.
var evidenceGroundedWorkerSummaryCodes = []string{
	"WORKER_EVIDENCE_HANDLE_UNKNOWN",
	"WORKER_URL_NOT_OBSERVED",
	"WORKER_SCOUT_NO_SURVEY_EVIDENCE",
	"SURFACE_CLAIM_UNGROUNDED",
}

// ambient Build queue grounding codes (guard:ambient_grounding).
// SSOT for ambient_grounding_contract_test.go.
var evidenceGroundedAmbientCodes = []string{
	"AMBIENT_UNGROUNDED_COMPLETION",
	"AMBIENT_GROUNDING_ESCALATED",
}

// coordinator synthesis grounding codes (guard:coordinator_synthesis).
// SSOT for synthesis_guard_contract_test.go.
var evidenceGroundedSynthesisCodes = []string{
	"SYNTH_URL_NOT_OBSERVED",
	"SYNTH_HANDLE_NOT_IN_LEGS",
	"SYNTH_CITATIONS_REQUIRED",
	"SYNTH_CITATION_UNVERIFIABLE",
}

// coordinator investigate grounding codes (guard:coordinator_investigate).
// SSOT for investigate_guard_contract_test.go.
var evidenceGroundedInvestigateCodes = []string{
	"INVEST_URL_NOT_OBSERVED",
	"INVEST_HANDLE_NOT_OBSERVED",
	"INVEST_CITATIONS_REQUIRED",
	"INVEST_CITATION_UNVERIFIABLE",
}

// Worker summary guard codes outside handle-keyed grounding (same emit channel).
var workerSummaryGuardCodesPreEvidenceGrounding = []string{
	"WORKER_SUMMARY_NO_ARTIFACT",
	"WORKER_SUMMARY_TOO_LONG",
	"WORKER_TURN_NO_PROSE",
}
