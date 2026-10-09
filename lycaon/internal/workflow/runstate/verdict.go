package runstate

import (
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
	"time"
)

type VerdictOperation struct {
	ToolCallID       string
	RunID            string
	SourceRevision   int64
	Phase            string
	InputDigest      string
	EvidenceRecordID string
	EvidenceJSON     string
	Status           string
	ResponseJSON     string
	Error            string
	// CreatedAt is when the verdict was submitted; recovery replays keep it.
	CreatedAt time.Time
}

// ReviewOutcome reports what RecordReviewLoopVerdict did, for tool results.
type ReviewOutcome struct {
	// Applied reports whether the active phase accepts review verdicts.
	Applied bool
	// Valid reports schema validity against the phase verdict_schema.
	Valid bool
	// Terminal reports the gate was satisfied and the host is advancing.
	Terminal bool
	// Attempt is the review round counter after this submission.
	Attempt     int
	Phase       string
	EvidenceKey string
	// MissingAgents lists required reviewers without succeeded envelopes.
	MissingAgents  []string
	InventoryIssue *InventoryIssue
	// CoverageIssue is the structured refusal of a coverage assessment.
	CoverageIssue *toolrejection.ToolReject
	QuestionIssue *toolrejection.ToolReject
	// IterationCapExceeded reports a non-terminal verdict rejected because the
	// phase already reached iteration_cap on a prior attempt.
	IterationCapExceeded bool
	// GroundingCode is the citation-audit reject on a terminal verdict.
	GroundingCode    string
	UngroundedCount  int
	UngroundedSample []string
	UncitedReviewers []string
	ObservedHandles  []string
	// Grounding is the validated citation provenance of an accepted terminal verdict.
	Grounding *api.CitationGrounding
}
