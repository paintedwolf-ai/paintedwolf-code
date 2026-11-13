package kick

import (
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

type KickOption func(*kickMeta)

type kickMeta struct {
	subject               string
	completedAt           *time.Time
	workerDigest          string
	evidenceDigest        string
	commandCompletion     string
	commandRefusal        string
	topologyOutput        string
	promotedPaths         []string
	scanID                string
	scanStatus            string
	scanCategories        string
	scanFindingsCount     int
	workerRoundNotice     *WorkerRoundNoticeFacts
	batchSeq              int
	batchSeqSet           bool
	vars                  map[string]any
	designForkCriterion   string
	fanoutPlanText        string
	maxFanoutLegs         int
	workerDecisionRequest map[string]any
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
func WithWorkerDecisionRequest(req api.WorkerDecisionRequest, worker string) KickOption {
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
			"worker":           strings.TrimSpace(worker),
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

// WithScanFinished passes terminal scan facts into the scan-finished kick template.
func WithScanFinished(scanID, status, categories string, findingsCount int) KickOption {
	return func(m *kickMeta) {
		m.scanID = strings.TrimSpace(scanID)
		m.scanStatus = strings.TrimSpace(status)
		m.scanCategories = strings.TrimSpace(categories)
		m.scanFindingsCount = findingsCount
	}
}

// Round notice kinds.
const (
	// RoundNoticeConsumption reports a round's measured consumption.
	RoundNoticeConsumption = "consumption"
	// RoundNoticeQuiet reports a round with no durable progress for a while.
	RoundNoticeQuiet = "quiet"
)

// Round in-flight states a quiet notice names.
const (
	RoundInFlightQueued    = "queued"
	RoundInFlightModelCall = "model_call"
	RoundInFlightTools     = "tools"
)

// WorkerRoundNoticeFacts are the measurements a round notice carries to the
// coordinator. They inform; nothing in them stops or judges the round.
type WorkerRoundNoticeFacts struct {
	JobID string
	// Worker is the name the coordinator addresses the worker by.
	Worker    string
	AgentType string
	Kind      string
	// ModelCalls, Tokens and SpendNanoUSD measure the round so far.
	ModelCalls   int
	Tokens       int
	SpendNanoUSD int64
	SpendPriced  bool
	// QuietFor and InFlight describe a quiet round.
	QuietFor  time.Duration
	InFlight  string
	ToolCalls int
	ToolsDone int
}

// WithWorkerRoundNotice passes one round notice's measurements into its kick.
func WithWorkerRoundNotice(facts WorkerRoundNoticeFacts) KickOption {
	return func(m *kickMeta) {
		facts.JobID = strings.TrimSpace(facts.JobID)
		m.workerRoundNotice = &facts
	}
}

const nanoUSDPerCent = 10_000_000

// renderData projects round notice facts under the names the template reads.
func (f WorkerRoundNoticeFacts) renderData(data map[string]any) {
	data["worker"] = f.Worker
	data["agent_type"] = f.AgentType
	data["notice_kind"] = f.Kind
	data["model_calls"] = f.ModelCalls
	data["tokens"] = f.Tokens
	// A priced round that cost nothing has no spend to report; one under a
	// cent would round to a misleading $0.00.
	switch {
	case !f.SpendPriced || f.SpendNanoUSD <= 0:
	case f.SpendNanoUSD < nanoUSDPerCent:
		data["spend_under_cent"] = true
	default:
		data["estimated_spend_usd"] = fmt.Sprintf("%.2f", float64(f.SpendNanoUSD)/1e9)
	}
	if f.Kind == RoundNoticeQuiet {
		data["quiet_minutes"] = int(f.QuietFor / time.Minute)
		data["in_flight"] = f.InFlight
		data["tool_calls"] = f.ToolCalls
		data["tools_done"] = f.ToolsDone
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
	if m.workerRoundNotice != nil {
		facts := *m.workerRoundNotice
		out.workerRoundNotice = &facts
	}
	if len(m.workerDecisionRequest) > 0 {
		out.workerDecisionRequest = make(map[string]any, len(m.workerDecisionRequest))
		for k, v := range m.workerDecisionRequest {
			out.workerDecisionRequest[k] = v
		}
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
