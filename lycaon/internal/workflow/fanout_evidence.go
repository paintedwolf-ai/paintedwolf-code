package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

const fanoutExecuteOutputStage = "fan_out"

type fanoutMessageReader interface {
	GetMessages(context.Context, string) ([]api.Message, error)
}

// StampFanoutExecuteOutput records merged worker evidence for synthesis.
func StampFanoutExecuteOutput(ctx context.Context, store fanoutMessageReader, sessionID string, vars map[string]any) map[string]any {
	if store == nil || strings.TrimSpace(sessionID) == "" {
		return vars
	}
	msgs, err := store.GetMessages(ctx, sessionID)
	if err != nil || len(msgs) == 0 {
		return vars
	}
	output := mergeFanoutWorkerEnvelopes(msgs)
	if output == "" {
		return vars
	}
	return markTopologyStage(vars, fanoutExecuteOutputStage, output)
}

func (m *RunManager) stampReviewLoopEvidence(ctx context.Context, run *api.WorkflowRun) error {
	if m == nil || run == nil || m.Sessions == nil {
		return nil
	}
	_, err := m.StampRunVars(ctx, run.ID, func(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		vars = StampFanoutExecuteOutput(ctx, m.Sessions, run.SessionID, vars)
		if digest := compileReviewLoopDigest(ctx, m, run); digest != "" {
			vars = SetHostVar(vars, "evidence_digest", digest)
		}

		return vars, true, nil
	})
	return err
}

func compileReviewLoopDigest(ctx context.Context, m *RunManager, run *api.WorkflowRun) string {
	if m == nil || run == nil {
		return ""
	}
	var blocks []string
	for _, source := range m.EvidenceDigests {
		if source == nil {
			continue
		}
		if block := strings.TrimSpace(source(ctx, run.ID)); block != "" {
			blocks = append(blocks, block)
		}
	}
	if m.Sessions != nil {
		msgs, err := m.Sessions.GetMessages(ctx, run.SessionID)
		if err == nil {
			if cites := formatWorkerCitationDigest(msgs); cites != "" {
				blocks = append(blocks, cites)
			}
		}
	}
	return strings.TrimSpace(strings.Join(blocks, "\n\n"))
}

func formatWorkerCitationDigest(msgs []api.Message) string {
	since := api.UserIntentBoundary(msgs)
	var blocks []string
	for _, msg := range msgs[since:] {
		if msg.WorkerSummary == nil {
			continue
		}
		ws := msg.WorkerSummary
		if ws.Grounding == nil || len(ws.Grounding.CitedEvidence) == 0 {
			continue
		}
		var lines []string
		for _, c := range ws.Grounding.CitedEvidence {
			line := strings.TrimSpace(c.Path)
			if c.Line > 0 {
				line += fmt.Sprintf(":%d", c.Line)
			}
			if ex := strings.TrimSpace(c.Excerpt); ex != "" {
				line += " — " + ex
			}
			if line != "" {
				lines = append(lines, "- "+line)
			}
		}
		if len(lines) == 0 {
			continue
		}
		label := strings.TrimSpace(ws.AgentType)
		if label == "" {
			label = "worker"
		}
		blocks = append(blocks, "## "+label+" citations\n"+strings.Join(lines, "\n"))
	}
	return strings.TrimSpace(strings.Join(blocks, "\n\n"))
}

func mergeFanoutWorkerEnvelopes(msgs []api.Message) string {
	since := api.UserIntentBoundary(msgs)
	var blocks []string
	for _, env := range workeroutcomes.TerminalWorkerEnvelopesSince(msgs, since) {
		if strings.TrimSpace(env.AgentType) == "" && strings.TrimSpace(env.JobID) == "" {
			continue
		}
		block := strings.TrimSpace(workeroutcomes.WorkerEnvelopeBlockText(env))
		if block == "" {
			continue
		}
		label := strings.TrimSpace(env.AgentType)
		if label == "" {
			label = "worker"
		}
		blocks = append(blocks, "## "+label+" ("+strings.TrimSpace(env.JobID)+")\n\n"+block)
	}
	return strings.TrimSpace(strings.Join(blocks, "\n\n---\n\n"))
}
