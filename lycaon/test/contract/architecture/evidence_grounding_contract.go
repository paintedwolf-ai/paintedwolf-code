package contract

// synthesisToWorkerGroundingCodePairing maps synthesis guard codes to worker counterparts.
// An empty value marks a synthesis-only code.
var synthesisToWorkerGroundingCodePairing = map[string]string{
	"SYNTH_HANDLE_NOT_IN_LEGS":    "WORKER_EVIDENCE_HANDLE_UNKNOWN",
	"SYNTH_URL_NOT_OBSERVED":      "WORKER_URL_NOT_OBSERVED",
	"SYNTH_CITATIONS_REQUIRED":    "WORKER_SCOUT_NO_SURVEY_EVIDENCE",
	"SYNTH_CITATION_UNVERIFIABLE": "WORKER_EVIDENCE_HANDLE_UNKNOWN",
}

// Closeout codes eligible for in-session repair: citation grounding, and the
// run report's document fields. The hint registry's evidence.in_session_retry
// must list the same set.
var evidenceGroundingInSessionRetryCodes = []string{
	"CITATION_REPAIR_INVALID",
	"CITATION_SYNTAX_INVALID",
	"WORKER_EVIDENCE_HANDLE_UNKNOWN",
	"WORKER_URL_NOT_OBSERVED",
	"WORKER_SCOUT_NO_SURVEY_EVIDENCE",
	"SURFACE_CLAIM_UNGROUNDED",
	"PAGE_MEASURE_UNGROUNDED",
	"SYNTH_HANDLE_NOT_IN_LEGS",
	"SYNTH_URL_NOT_OBSERVED",
	"SYNTH_CITATION_UNVERIFIABLE",
	"PRESENT_MARKDOWN_EMBED",
	"PRESENT_IMAGE_UNOBSERVED",
	"INVEST_HANDLE_NOT_OBSERVED",
	"INVEST_URL_NOT_OBSERVED",
	"INVEST_CITATION_UNVERIFIABLE",
	"REPORT_FENCE_UNREADABLE",
	"REPORT_DOCUMENT_INVALID",
	"REPORT_CLAIM_UNREPORTED",
	"REPORT_INVENTORY_UNACCOUNTED",
}
