package kick

import (
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

type KickOption func(*kickMeta)

type kickMeta struct {
	subject                   string
	completedAt               *time.Time
	workerDigest              string
	evidenceDigest            string
	commandCompletion         string
	commandRefusal            string
	topologyOutput            string
	pendingOverlayPromoteJobs []string
	partialWorkerJobs         []string
	promotedPaths             []string
	scanID                    string
	scanStatus                string
	scanCategories            string
	scanFindingsCount         int
	workerBudget              *WorkerBudgetFacts
	batchSeq                  int
	batchSeqSet               bool
	vars                      map[string]any
	designForkCriterion       string
	fanoutPlanText            string
	maxFanoutLegs             int
	workerDecisionRequest     map[string]any
}

// WithSubject names the job, leg, scan, or process a kick reports, so kicks
// for different subjects queue and render separately.
func WithSubject(subject string) KickOption {
	return func(m *kickMeta) {
		m.subject = strings.TrimSpace(subject)
	}
}

// WithCommandCompletion passes the terminal command envelope into process-finished guidance.
func WithCommandCompletion(completion string) KickOption {
	return func(m *kickMeta) {
		m.commandCompletion = strings.TrimSpace(completion)
	}
}

// WithCommandRefusal passes a running job's refusal report into process-refused guidance.
func WithCommandRefusal(report string) KickOption {
	return func(m *kickMeta) {
		m.commandRefusal = strings.TrimSpace(report)
	}
}

// WithLegCompletedAt passes completed_ago into the leg-finished kick template.
func WithLegCompletedAt(t time.Time) KickOption {
	return func(m *kickMeta) {
		if !t.IsZero() {
			utc := t.UTC()
			m.completedAt = &utc
		}
	}
}

// WithEvidenceDigest passes curated synthesis evidence navigation into coordinator kicks.
func WithEvidenceDigest(digest string) KickOption {
	return func(m *kickMeta) {
		m.evidenceDigest = strings.TrimSpace(digest)
	}
}

// WithTopologyOutput passes merged topology output into synthesis kicks.
func WithTopologyOutput(output string) KickOption {
	return func(m *kickMeta) {
		m.topologyOutput = strings.TrimSpace(output)
	}
}

// WithDesignForkCriterion passes the design-fork criterion into debate-select kicks.
func WithDesignForkCriterion(criterion string) KickOption {
	return func(m *kickMeta) {
		m.designForkCriterion = strings.TrimSpace(criterion)
	}
}

// WithFanoutPlanText passes a formatted fanout plan into fanout execute kicks.
func WithFanoutPlanText(text string) KickOption {
	return func(m *kickMeta) {
		m.fanoutPlanText = strings.TrimSpace(text)
	}
}

// WithMaxFanoutLegs passes the leg cap into fanout plan kicks.
func WithMaxFanoutLegs(n int) KickOption {
	return func(m *kickMeta) {
		m.maxFanoutLegs = n
	}
}

// WithWorkerDigest passes host-compiled worker proof into coordinator kicks.
func WithWorkerDigest(digest string) KickOption {
	return func(m *kickMeta) {
		m.workerDigest = strings.TrimSpace(digest)
	}
}

// WithWorkerDecisionRequest passes a structured worker request_decision into coordinator kicks.
func WithWorkerDecisionRequest(req api.WorkerDecisionRequest) KickOption {
	return func(m *kickMeta) {
		question := strings.TrimSpace(req.Question)
		if question == "" {
			return
		}
		opts := make([]string, 0, len(req.Options))
		for _, o := range req.Options {
			if o = strings.TrimSpace(o); o != "" {
				opts = append(opts, o)
			}
		}
		class := string(req.BlockerClass)
		if strings.TrimSpace(class) == "" {
			class = string(api.WorkerBlockerDecision)
		}
		m.workerDecisionRequest = map[string]any{
			"job_id":           strings.TrimSpace(req.WorkerID),
			"child_session_id": strings.TrimSpace(req.ChildSessionID),
			"question":         question,
			"options":          opts,
			"blocker_class":    class,
		}
		if id := strings.TrimSpace(req.ArtifactID); id != "" {
			m.workerDecisionRequest["artifact_id"] = id
		}
		if ids := req.ArtifactIDs; len(ids) > 0 {
			clean := make([]string, 0, len(ids))
			for _, id := range ids {
				if id = strings.TrimSpace(id); id != "" {
					clean = append(clean, id)
				}
			}
			if len(clean) > 0 {
				m.workerDecisionRequest["artifact_ids"] = clean
			}
		}
	}
}

// WorkerDecisionRequestFromOptions extracts structured decision facts stamped on kick options.
func WorkerDecisionRequestFromOptions(opts []KickOption) (api.WorkerDecisionRequest, bool) {
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		meta := kickMeta{}
		opt(&meta)
		if len(meta.workerDecisionRequest) == 0 {
			continue
		}
		question, _ := meta.workerDecisionRequest["question"].(string)
		if strings.TrimSpace(question) == "" {
			continue
		}
		jobID, _ := meta.workerDecisionRequest["job_id"].(string)
		child, _ := meta.workerDecisionRequest["child_session_id"].(string)
		class, _ := meta.workerDecisionRequest["blocker_class"].(string)
		artifactID, _ := meta.workerDecisionRequest["artifact_id"].(string)
		var artifactIDs []string
		switch raw := meta.workerDecisionRequest["artifact_ids"].(type) {
		case []string:
			artifactIDs = append([]string(nil), raw...)
		case []any:
			for _, it := range raw {
				if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
					artifactIDs = append(artifactIDs, strings.TrimSpace(s))
				}
			}
		}
		var options []string
		switch raw := meta.workerDecisionRequest["options"].(type) {
		case []string:
			options = append([]string(nil), raw...)
		case []any:
			for _, it := range raw {
				if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
					options = append(options, strings.TrimSpace(s))
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
		}, true
	}
	return api.WorkerDecisionRequest{}, false
}

// WithPendingOverlayPromote records overlays awaiting review.
func WithPendingOverlayPromote(jobIDs []string) KickOption {
	return func(m *kickMeta) {
		m.pendingOverlayPromoteJobs = append([]string(nil), jobIDs...)
	}
}

// WithPartialWorkerJobs lists implementer job ids whose envelopes are still partial.
func WithPartialWorkerJobs(jobIDs []string) KickOption {
	return func(m *kickMeta) {
		m.partialWorkerJobs = append([]string(nil), jobIDs...)
	}
}

// WithScanFinished passes terminal scan facts into the scan-finished kick template.
func WithScanFinished(scanID, status, categories string, findingsCount int) KickOption {
	return func(m *kickMeta) {
		m.scanID = strings.TrimSpace(scanID)
		m.scanStatus = strings.TrimSpace(status)
		m.scanCategories = strings.TrimSpace(categories)
		m.scanFindingsCount = findingsCount
	}
}

// WorkerBudgetFacts describes one worker's tool-round ceiling for coordinator guidance.
type WorkerBudgetFacts struct {
	JobID          string
	ChildSessionID string
	Used           int
	Max            int
	HostMax        int
	// Request is the worker's unanswered ask for more rounds.
	Request *api.WorkerBudgetRequest
	// Exhausted marks a partial finish at the ceiling.
	Exhausted bool
	// ResumeMax is the ceiling a resume of an exhausted worker should carry.
	ResumeMax int
}

// WithWorkerBudget passes one worker's ceiling facts into budget and finish kicks.
func WithWorkerBudget(facts WorkerBudgetFacts) KickOption {
	return func(m *kickMeta) {
		facts.JobID = strings.TrimSpace(facts.JobID)
		facts.ChildSessionID = strings.TrimSpace(facts.ChildSessionID)
		m.workerBudget = &facts
	}
}

// renderData projects budget facts under the names budget templates read.
func (f WorkerBudgetFacts) renderData(data map[string]any) {
	data["job_id"] = f.JobID
	data["tool_loops_used"] = f.Used
	data["max_tool_loops"] = f.Max
	data["host_max_tool_loops"] = f.HostMax
	if f.ChildSessionID != "" {
		data["child_session_id"] = f.ChildSessionID
	}
	if f.Request != nil {
		data["budget_request_open"] = true
		data["requested_max"] = f.Request.RequestedMax
		data["requested_rounds"] = f.Request.Rounds
		data["remaining_work"] = append([]string(nil), f.Request.RemainingWork...)
	}
	if f.Exhausted {
		data["worker_budget_exhausted"] = true
		data["suggested_resume_max"] = f.ResumeMax
	}
}

// WithPromotedPaths lists repo-relative paths integrated during overlay promote.
func WithPromotedPaths(paths []string) KickOption {
	return func(m *kickMeta) {
		for _, p := range paths {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			m.promotedPaths = append(m.promotedPaths, p)
		}
	}
}

// WithBatchSeq records the coordinator batch epoch.
func WithBatchSeq(seq int) KickOption {
	return func(m *kickMeta) {
		m.batchSeq = seq
		m.batchSeqSet = true
	}
}

// WithVars merges declared anchor data.
func WithVars(data map[string]any) KickOption {
	return func(m *kickMeta) {
		if len(data) == 0 {
			return
		}
		if m.vars == nil {
			m.vars = make(map[string]any, len(data))
		}
		for k, v := range data {
			m.vars[k] = v
		}
	}
}

func cloneKickMeta(m kickMeta) kickMeta {
	out := kickMeta{
		subject:           m.subject,
		workerDigest:      m.workerDigest,
		evidenceDigest:    m.evidenceDigest,
		commandCompletion: m.commandCompletion,
		commandRefusal:    m.commandRefusal,
		topologyOutput:    m.topologyOutput,
		scanID:            m.scanID,
		scanStatus:        m.scanStatus,
		scanCategories:    m.scanCategories,
		scanFindingsCount: m.scanFindingsCount,
		batchSeq:          m.batchSeq,
		batchSeqSet:       m.batchSeqSet,
		fanoutPlanText:    m.fanoutPlanText,
		maxFanoutLegs:     m.maxFanoutLegs,
	}
	if m.completedAt != nil {
		t := m.completedAt.UTC()
		out.completedAt = &t
	}
	if m.workerBudget != nil {
		facts := *m.workerBudget
		if facts.Request != nil {
			req := *facts.Request
			req.RemainingWork = append([]string(nil), req.RemainingWork...)
			facts.Request = &req
		}
		out.workerBudget = &facts
	}
	if len(m.workerDecisionRequest) > 0 {
		out.workerDecisionRequest = make(map[string]any, len(m.workerDecisionRequest))
		for k, v := range m.workerDecisionRequest {
			out.workerDecisionRequest[k] = v
		}
	}
	if len(m.pendingOverlayPromoteJobs) > 0 {
		out.pendingOverlayPromoteJobs = append([]string(nil), m.pendingOverlayPromoteJobs...)
	}
	if len(m.partialWorkerJobs) > 0 {
		out.partialWorkerJobs = append([]string(nil), m.partialWorkerJobs...)
	}
	if len(m.promotedPaths) > 0 {
		out.promotedPaths = append([]string(nil), m.promotedPaths...)
	}
	if len(m.vars) > 0 {
		out.vars = make(map[string]any, len(m.vars))
		for k, v := range m.vars {
			out.vars[k] = v
		}
	}
	return out
}
