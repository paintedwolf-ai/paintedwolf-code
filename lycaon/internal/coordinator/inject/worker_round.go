package inject

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
)

// WorkerRoundFrame is a worker's running round as each of its requests shows
// it: the standing brief and the round's instructions stay put after their
// delivery and through compaction, until the round delivers.
type WorkerRoundFrame struct {
	Ordinal      int64
	Brief        string
	Instructions []WorkerMessageLine
	// Workspace and PriorChange describe a later round's starting point.
	Workspace   string
	PriorChange string
	// EarlierResults are the worker's settled results, oldest first.
	EarlierResults []string
}

// RenderWorkerRoundBlock renders host framing for the round and, as a
// separate part, the brief and instructions the coordinator wrote.
func RenderWorkerRoundBlock(ctx context.Context, renderer *prompts.InjectRenderer, sessionID string, frame WorkerRoundFrame) WorkerMessagesBlock {
	brief := strings.TrimSpace(frame.Brief)
	if renderer == nil || (brief == "" && len(frame.Instructions) == 0) {
		return WorkerMessagesBlock{}
	}
	header, err := anchor.RenderInform(ctx, anchor.InjectWorkerRound, anchor.MatchContext{Surface: "worker", SessionID: sessionID}, renderer, map[string]any{
		"round": frame.Ordinal, "has_brief": brief != "", "instruction_count": len(frame.Instructions),
		"workspace": frame.Workspace, "prior_change": frame.PriorChange, "earlier_results": frame.EarlierResults,
	})
	if err != nil || strings.TrimSpace(header) == "" {
		return WorkerMessagesBlock{}
	}
	var data []string
	if brief != "" {
		data = append(data, "Brief:\n"+brief)
	}
	if len(frame.Instructions) > 0 {
		data = append(data, "Instructions this round:\n"+workerMessageData(frame.Instructions, false))
	}
	return WorkerMessagesBlock{Header: strings.TrimSpace(header), Messages: strings.Join(data, "\n\n")}
}
