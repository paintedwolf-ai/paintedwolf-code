package workeroutcomes

import (
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

// SummaryInput is metadata for a parent-visible worker summary.
type SummaryInput struct {
	Summary         string
	Report          workercompletion.WorkerCompletionReport
	DelegationID    string
	LegID           string
	JobID           string
	AgentType       string
	ChildSessionID  string
	ParentSessionID string
	ProjectDir      string
	ProjectRoots    []projectroot.RootRef
	ActiveRootID    string
	ProjectID       string
	Status          string
	HintCode        string
	PolicyFeedback  *api.WorkerPolicyFeedback
	ReportEvaluated bool
	CompletedAt     *time.Time
	GroundingOut    *api.CitationGrounding
}
