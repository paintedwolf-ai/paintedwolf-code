package inject

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/runeclamp"
)

const maxCommandLedgerLineRunes = 512

// RenderCommandJobsBlock renders the live host ledger of command jobs and held
// calls for a turn.
func RenderCommandJobsBlock(
	ctx context.Context,
	renderer *prompts.InjectRenderer,
	sessionID string,
	jobs []bgprocess.JobSnapshot,
	held []heldcall.Running,
	now time.Time,
	surface string,
) string {
	if renderer == nil || len(jobs)+len(held) == 0 {
		return ""
	}
	rows := make([]map[string]any, 0, len(jobs)+len(held))
	for _, job := range jobs {
		row := map[string]any{
			"handle": job.Handle, "mode": job.Mode, "origin_tool": job.OriginTool,
			"elapsed": now.Sub(job.StartedAt).Round(time.Second).String(),
		}
		if len(job.Stages) > 0 {
			row["command"] = commandLedgerLine(job.Stages[0].Command)
		}
		if job.Timeout > 0 {
			remaining := job.StartedAt.Add(job.Timeout).Sub(now).Round(time.Second)
			if remaining < 0 {
				remaining = 0
			}
			row["deadline_in"] = remaining.String()
		}
		rows = append(rows, row)
	}
	for _, call := range held {
		rows = append(rows, map[string]any{
			"handle": call.Handle, "mode": "held", "origin_tool": call.Tool,
			"elapsed": call.Elapsed.Round(time.Second).String(),
		})
	}
	block, err := anchor.RenderInform(
		ctx, anchor.InjectCommandJobs, anchor.MatchContext{Surface: surface, SessionID: sessionID},
		renderer, map[string]any{
			"jobs": rows, "can_wait": true, "has_command": len(jobs) > 0, "has_held": len(held) > 0,
		},
	)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(block)
}

func commandLedgerLine(command string) string {
	return runeclamp.Clamp(strings.ToValidUTF8(command, "�"), maxCommandLedgerLineRunes)
}
