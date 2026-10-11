package workercompletion

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/workercompletionxml"
	"github.com/lycaon/lycaon/pkg/api"
)

type WorkerCompletionEnvelope struct {
	JobID           string
	ChildSessionID  string
	AgentType       string
	State           string
	MergeStatus     string
	HintCode        string
	Summary         string
	Body            string
	Digest          string
	Proof           WorkerCompletionProof
	Report          WorkerCompletionReport
	DecisionRequest *api.WorkerDecisionRequest
}

// FormatWorkerCompletionEnvelope renders the parent <task> block from env.
func FormatWorkerCompletionEnvelope(env WorkerCompletionEnvelope) string {
	state := NormalizeWorkerCompletionState(env.State)
	if state == "" {
		return ""
	}
	summary := strings.TrimSpace(env.Summary)
	if summary == "" {
		summary = defaultWorkerCompletionSummary(state, env.AgentType)
	}
	reportJSON := ""
	if !env.Report.Empty() {
		reportJSON = marshalEnvelopeJSON(env.Report.ParentReport())
	}
	decisionJSON := marshalEnvelopeJSON(env.DecisionRequest)
	doc := workercompletionxml.Doc{
		JobID:               strings.TrimSpace(env.JobID),
		ChildSessionID:      strings.TrimSpace(env.ChildSessionID),
		AgentType:           strings.TrimSpace(env.AgentType),
		State:               state,
		MergeStatus:         strings.TrimSpace(env.MergeStatus),
		HintCode:            strings.TrimSpace(env.HintCode),
		Summary:             summary,
		TaskResult:          parentTaskResult(summary, env.Body, !env.Report.Empty()),
		Digest:              parentDigest(env.Digest, reportJSON != "" || decisionJSON != ""),
		ProofJSON:           marshalEnvelopeJSON(ParentProofFrom(env.Proof)),
		ReportJSON:          reportJSON,
		DecisionRequestJSON: decisionJSON,
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return ""
	}
	// Command tails and worker prose can embed the absolute branch root.
	return enginepaths.RewriteWorkerBranchPaths(string(out))
}

func parentTaskResult(summary, body string, hasReport bool) string {
	if hasReport {
		return ""
	}
	body = strings.TrimSpace(body)
	if body == "" || body == summary {
		return ""
	}
	return body
}

func parentDigest(digest string, hasStructured bool) string {
	if hasStructured {
		return ""
	}
	return strings.TrimSpace(digest)
}

func marshalEnvelopeJSON(v any) string {
	if v == nil {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s := string(raw)
	if s == "" || s == "{}" || s == "null" {
		return ""
	}
	return s
}

// ParseWorkerCompletionEnvelope reads a parent <task> block.
func ParseWorkerCompletionEnvelope(content string) (WorkerCompletionEnvelope, bool) {
	doc, ok := workercompletionxml.Unmarshal(content)
	if !ok {
		return WorkerCompletionEnvelope{}, false
	}
	env := WorkerCompletionEnvelope{
		JobID:          strings.TrimSpace(doc.JobID),
		ChildSessionID: strings.TrimSpace(doc.ChildSessionID),
		AgentType:      strings.TrimSpace(doc.AgentType),
		State:          NormalizeWorkerCompletionState(doc.State),
		MergeStatus:    strings.TrimSpace(doc.MergeStatus),
		HintCode:       strings.TrimSpace(doc.HintCode),
		Summary:        strings.TrimSpace(doc.Summary),
		Body:           strings.TrimSpace(doc.TaskResult),
		Digest:         strings.TrimSpace(doc.Digest),
	}
	if proofJSON := strings.TrimSpace(doc.ProofJSON); proofJSON != "" {
		var parent ParentProof
		if err := json.Unmarshal([]byte(proofJSON), &parent); err == nil {
			env.Proof = parent.Proof()
		}
	}
	if reportJSON := strings.TrimSpace(doc.ReportJSON); reportJSON != "" {
		if report, ok := ParseWorkerCompletionReport(reportJSON); ok {
			env.Report = report
		}
	}
	if decJSON := strings.TrimSpace(doc.DecisionRequestJSON); decJSON != "" {
		var dec api.WorkerDecisionRequest
		if err := json.Unmarshal([]byte(decJSON), &dec); err == nil && strings.TrimSpace(dec.Question) != "" {
			env.DecisionRequest = &dec
		}
	}
	if env.JobID == "" && env.State == "" && env.AgentType == "" {
		return WorkerCompletionEnvelope{}, false
	}
	return env, true
}

func defaultWorkerCompletionSummary(state, agentType string) string {
	agent := strings.TrimSpace(agentType)
	if agent == "" {
		agent = "worker"
	}
	switch NormalizeWorkerCompletionState(state) {
	case string(api.WorkerSummaryStatusComplete):
		return fmt.Sprintf("%s finished", agent)
	case string(api.WorkerSummaryStatusPartial):
		return fmt.Sprintf("%s finished with remaining work", agent)
	case string(api.WorkerSummaryStatusOpen):
		return fmt.Sprintf("%s finished — changes open on branch", agent)
	case string(api.WorkerSummaryStatusNeedsDecision):
		return fmt.Sprintf("%s needs a decision to continue", agent)
	case string(api.WorkerSummaryStatusHeld):
		return fmt.Sprintf("%s is held", agent)
	case string(api.WorkerSummaryStatusCanceled):
		return fmt.Sprintf("%s canceled", agent)
	case string(api.WorkerSummaryStatusFailed):
		return fmt.Sprintf("%s failed", agent)
	default:
		return ""
	}
}
