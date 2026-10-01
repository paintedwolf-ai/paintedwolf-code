package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/pkg/api"
)

// DecisionStore stores pending worker decisions.
type DecisionStore interface {
	Put(context.Context, api.WorkerDecisionRequest) error
	Get(context.Context, string) (api.WorkerDecisionRequest, bool, error)
	GetByJob(context.Context, string) (api.WorkerDecisionRequest, bool, error)
	Clear(context.Context, string) error
}

// SetDecisionStore wires pending worker decisions.
func (m *Manager) SetDecisionStore(store DecisionStore) {
	if m != nil {
		m.decisions = store
	}
}

// Decisions returns the pending worker decision store.
func (m *Manager) Decisions() DecisionStore {
	if m == nil {
		return nil
	}
	return m.decisions
}

// workerDecisionSummaryRunes bounds decision-row questions.
const workerDecisionSummaryRunes = 160

// FormatWorkerDecision renders a pending coordinator decision.
func FormatWorkerDecision(agentType string, dec api.WorkerDecisionRequest) (summary, body string) {
	agent := strings.TrimSpace(agentType)
	if agent == "" {
		agent = "worker"
	}
	question := strings.TrimSpace(dec.Question)
	summary = agent + " needs a decision: " +
		runeclamp.Clamp(question, workerDecisionSummaryRunes)
	var bodyBuilder strings.Builder
	bodyBuilder.WriteString("NEEDS DECISION — ")
	bodyBuilder.WriteString(question)
	bodyBuilder.WriteString("\nOptions:\n")
	for i, option := range dec.Options {
		fmt.Fprintf(&bodyBuilder, "%d. %s\n", i+1, option)
	}
	fmt.Fprintf(&bodyBuilder, "Resume the worker with answer_decision(job_id=%q, option=<number or exact option text>).", dec.WorkerID)
	return summary, bodyBuilder.String()
}

func normalizeDecision(decision api.WorkerDecisionRequest) (api.WorkerDecisionRequest, error) {
	decision.ChildSessionID = strings.TrimSpace(decision.ChildSessionID)
	decision.WorkerID = strings.TrimSpace(decision.WorkerID)
	decision.Question = strings.TrimSpace(decision.Question)
	if decision.ChildSessionID == "" || decision.WorkerID == "" || decision.Question == "" {
		return api.WorkerDecisionRequest{}, fmt.Errorf("decision identity is incomplete")
	}
	decision.Options = cleanDecisionStrings(decision.Options)
	decision.ArtifactID = strings.TrimSpace(decision.ArtifactID)
	decision.ArtifactIDs = cleanDecisionStrings(decision.ArtifactIDs)
	if decision.BlockerClass == "" {
		decision.BlockerClass = api.WorkerBlockerDecision
	}
	return decision, nil
}

func cleanDecisionStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
