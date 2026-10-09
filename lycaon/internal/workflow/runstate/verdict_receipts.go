package runstate

import (
	"github.com/lycaon/lycaon/pkg/api"
)

// VerdictSubmission is the journaled submit_verdict input.
type VerdictSubmission struct {
	SessionID string                               `json:"session_id"`
	Verdict   map[string]string                    `json:"verdict"`
	Cited     []api.CitationGroundingCitedEvidence `json:"cited_evidence"`
	CitedURLs []string                             `json:"cited_urls,omitempty"`
}

// VerdictReceipt binds a structured submission and its audit to one phase.
type VerdictReceipt struct {
	SourceRevision int64             `json:"source_revision"`
	ToolCallID     string            `json:"tool_call_id"`
	RunID          string            `json:"run_id"`
	Phase          string            `json:"phase"`
	Status         string            `json:"status"`
	Submission     VerdictSubmission `json:"submission"`
	Outcome        ReviewOutcome     `json:"outcome"`
}
