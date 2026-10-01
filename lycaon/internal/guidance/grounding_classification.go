package guidance

// Classification-free grounding contract.
//
// Grounding never asks "does this token look like a path/URL?" as a gate.
// Verbatim-substring verification (opaque/command shapes) and observed-set
// membership (file_region/url) are the only production verdict paths.
const (
	// GroundingVerdictPrimary documents verbatim-first grounding for command/opaque shapes.
	GroundingVerdictPrimary = "verbatim_substring"

	// LeakDetectionMode documents observed-set membership for prose leak detection.
	LeakDetectionMode = "observed_set_membership"

	// LeakSeverityReject is a structured-observed citation in prose outside the typed channel.
	LeakSeverityReject = "reject"

	// LeakSeverityAdvisory is a lower-trust observed citation in prose — surfaced, not partial.
	LeakSeverityAdvisory = "advisory"
)
