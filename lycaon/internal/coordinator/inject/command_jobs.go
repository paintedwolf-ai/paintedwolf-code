package inject

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/runeclamp"
)

const maxCommandLedgerLineRunes = 64

// RenderCommandJobsBlock renders the host ledger of command jobs and held calls
// for a turn. Each sentence renders only in the states it is true for.
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
	block, err := anchor.RenderInform(
		ctx, anchor.InjectCommandJobs, anchor.MatchContext{Surface: surface, SessionID: sessionID},
		renderer, commandJobsVars(jobs, held, now),
	)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(block)
}

// commandJobsVars are the ledger's facts: one row per job or held call, and
// which kinds are present, so each sentence is conditioned on its own state.
func commandJobsVars(jobs []bgprocess.JobSnapshot, held []heldcall.Running, now time.Time) map[string]any {
	rows := make([]map[string]any, 0, len(jobs)+len(held))
	running, exited := false, false
	for _, job := range jobs {
		row := map[string]any{"handle": job.Handle, "origin_tool": job.OriginTool, "exited": job.Exited}
		if len(job.Stages) > 0 {
			row["command"] = commandLedgerLine(job.Stages[0].Command)
		}
		if job.Exited {
			exited = true
			row["exit_code"], row["refusals"] = job.ExitCode, job.Refusals
			rows = append(rows, row)
			continue
		}
		running = true
		row["mode"], row["elapsed"] = job.Mode, now.Sub(job.StartedAt).Round(time.Second).String()
		if job.Timeout > 0 {
			row["deadline_in"] = max(job.StartedAt.Add(job.Timeout).Sub(now).Round(time.Second), 0).String()
		}
		rows = append(rows, row)
	}
	for _, call := range held {
		rows = append(rows, map[string]any{
			"handle": call.Handle, "mode": "held", "origin_tool": call.Tool,
			"elapsed": call.Elapsed.Round(time.Second).String(),
		})
	}
	return map[string]any{
		"jobs": rows, "has_running": running, "has_exited": exited, "has_held": len(held) > 0,
		"can_wait": running || len(held) > 0,
	}
}

// commandLedgerLine keeps each ledger row on one line: a multiline statement's
// lines are joined by single spaces, then fit within the row bound.
func commandLedgerLine(command string) string {
	lines := strings.FieldsFunc(strings.ToValidUTF8(command, "�"), func(r rune) bool { return r == '\n' || r == '\r' })
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return runeclamp.Fit(strings.Join(slices.DeleteFunc(lines, func(line string) bool { return line == "" }), " "), maxCommandLedgerLineRunes)
}
