package anchor

import (
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/pkg/api"
)

// Envelope carries emit-time kick data.
type Envelope struct {
	// Subject names the job, leg, scan, or process the fact is about.
	Subject                 string
	CompletedAt             *time.Time
	WorkerDigest            string
	EvidenceDigest          string
	CommandCompletionDigest string
	// CommandRefusalDigest reports what the kernel refused a running job.
	CommandRefusalDigest      string
	TopologyOutput            string
	PendingOverlayPromoteJobs []string
	PartialWorkerJobs         []string
	PromotedPaths             []string
	ScanID                    string
	ScanStatus                string
	ScanCategories            string
	ScanFindingsCount         int
	// WorkerBudget carries a worker's ceiling, open request, and exhaustion.
	WorkerBudget *kick.WorkerBudgetFacts
	BatchSeq     int
	BatchSeqSet  bool
	// Vars carries anchor-specific catalog data.
	// (roots-changed data, progress facts, phase-exit facts, …).
	Vars                  map[string]any
	DesignForkCriterion   string
	FanoutPlanText        string
	MaxFanoutLegs         int
	WorkerDecisionRequest map[string]any
}

// KickOptions converts the Envelope into KickOption facades for KickEngine queueing.
func (e Envelope) KickOptions() []kick.KickOption {
	var opts []kick.KickOption
	if s := strings.TrimSpace(e.Subject); s != "" {
		opts = append(opts, kick.WithSubject(s))
	}
	if e.CompletedAt != nil && !e.CompletedAt.IsZero() {
		opts = append(opts, kick.WithLegCompletedAt(*e.CompletedAt))
	}
	if d := strings.TrimSpace(e.WorkerDigest); d != "" {
		opts = append(opts, kick.WithWorkerDigest(d))
	}
	if d := strings.TrimSpace(e.EvidenceDigest); d != "" {
		opts = append(opts, kick.WithEvidenceDigest(d))
	}
	if d := strings.TrimSpace(e.CommandCompletionDigest); d != "" {
		opts = append(opts, kick.WithCommandCompletion(d))
	}
	if d := strings.TrimSpace(e.CommandRefusalDigest); d != "" {
		opts = append(opts, kick.WithCommandRefusal(d))
	}
	if d := strings.TrimSpace(e.TopologyOutput); d != "" {
		opts = append(opts, kick.WithTopologyOutput(d))
	}
	if d := strings.TrimSpace(e.DesignForkCriterion); d != "" {
		opts = append(opts, kick.WithDesignForkCriterion(d))
	}
	if d := strings.TrimSpace(e.FanoutPlanText); d != "" {
		opts = append(opts, kick.WithFanoutPlanText(d))
	}
	if e.MaxFanoutLegs > 0 {
		opts = append(opts, kick.WithMaxFanoutLegs(e.MaxFanoutLegs))
	}
	if len(e.PendingOverlayPromoteJobs) > 0 {
		opts = append(opts, kick.WithPendingOverlayPromote(e.PendingOverlayPromoteJobs))
	}
	if len(e.PartialWorkerJobs) > 0 {
		opts = append(opts, kick.WithPartialWorkerJobs(e.PartialWorkerJobs))
	}
	if len(e.PromotedPaths) > 0 {
		opts = append(opts, kick.WithPromotedPaths(e.PromotedPaths))
	}
	if strings.TrimSpace(e.ScanID) != "" {
		opts = append(opts, kick.WithScanFinished(e.ScanID, e.ScanStatus, e.ScanCategories, e.ScanFindingsCount))
	}
	if e.WorkerBudget != nil && strings.TrimSpace(e.WorkerBudget.JobID) != "" {
		opts = append(opts, kick.WithWorkerBudget(*e.WorkerBudget))
	}
	if e.BatchSeqSet {
		opts = append(opts, kick.WithBatchSeq(e.BatchSeq))
	}
	if len(e.Vars) > 0 {
		opts = append(opts, kick.WithVars(e.Vars))
	}
	if len(e.WorkerDecisionRequest) > 0 {
		req := workerDecisionFromMap(e.WorkerDecisionRequest)
		if strings.TrimSpace(req.Question) != "" {
			opts = append(opts, kick.WithWorkerDecisionRequest(req))
		}
	}
	return opts
}

// HasPendingOverlayPromote reports emit-time overlay-promote facts.
func (e Envelope) HasPendingOverlayPromote() bool {
	return len(e.PendingOverlayPromoteJobs) > 0
}

// HasWorkerDecision reports a structured worker decision.
func (e Envelope) HasWorkerDecision() bool {
	if len(e.WorkerDecisionRequest) == 0 {
		return false
	}
	question, _ := e.WorkerDecisionRequest["question"].(string)
	return strings.TrimSpace(question) != ""
}

// WithWorkerDecision sets structured worker decision facts on the Envelope.
func (e *Envelope) WithWorkerDecision(req api.WorkerDecisionRequest) {
	if e == nil || strings.TrimSpace(req.Question) == "" {
		return
	}
	e.WorkerDecisionRequest = map[string]any{
		"job_id":           strings.TrimSpace(req.WorkerID),
		"child_session_id": strings.TrimSpace(req.ChildSessionID),
		"question":         strings.TrimSpace(req.Question),
		"options":          append([]string(nil), req.Options...),
		"blocker_class":    string(req.BlockerClass),
	}
	if id := strings.TrimSpace(req.ArtifactID); id != "" {
		e.WorkerDecisionRequest["artifact_id"] = id
	}
	if len(req.ArtifactIDs) > 0 {
		e.WorkerDecisionRequest["artifact_ids"] = append([]string(nil), req.ArtifactIDs...)
	}
}

func workerDecisionFromMap(m map[string]any) api.WorkerDecisionRequest {
	question, _ := m["question"].(string)
	jobID, _ := m["job_id"].(string)
	child, _ := m["child_session_id"].(string)
	class, _ := m["blocker_class"].(string)
	artifactID, _ := m["artifact_id"].(string)
	var options []string
	switch raw := m["options"].(type) {
	case []string:
		options = append([]string(nil), raw...)
	case []any:
		for _, it := range raw {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				options = append(options, strings.TrimSpace(s))
			}
		}
	}
	var artifactIDs []string
	switch raw := m["artifact_ids"].(type) {
	case []string:
		artifactIDs = append([]string(nil), raw...)
	case []any:
		for _, it := range raw {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				artifactIDs = append(artifactIDs, strings.TrimSpace(s))
			}
		}
	}
	return api.WorkerDecisionRequest{
		WorkerID:       strings.TrimSpace(jobID),
		ChildSessionID: strings.TrimSpace(child),
		Question:       strings.TrimSpace(question),
		Options:        options,
		BlockerClass:   api.WorkerBlockerClass(strings.TrimSpace(class)),
		ArtifactID:     strings.TrimSpace(artifactID),
		ArtifactIDs:    artifactIDs,
	}
}
