package workeroutcomes

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/pkg/api"
)

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
