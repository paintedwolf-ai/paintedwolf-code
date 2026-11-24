package inject

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
)

// Every ledger state carries exactly the facts its sentences need, and every
// state renders.
func TestCommandJobsFactsFollowEachLedgerState(t *testing.T) {
	now := time.Now().UTC()
	running := bgprocess.JobSnapshot{Handle: "server", Mode: bgprocess.JobModeBackground, OriginTool: "command", StartedAt: now.Add(-time.Minute)}
	watched := bgprocess.JobSnapshot{Handle: "watched-build", WatchHandle: "watch-3", Mode: bgprocess.JobModeBackground, OriginTool: "command", StartedAt: now.Add(-time.Minute)}
	exited := bgprocess.JobSnapshot{Handle: "crashed", OriginTool: "command", StartedAt: now.Add(-time.Minute), Exited: true, ExitCode: 1}
	refused := exited
	refused.Handle, refused.Refusals = "refused", 2
	held := heldcall.Running{Handle: "held", Tool: "find", Elapsed: time.Minute}
	for _, tc := range []struct {
		name                                             string
		jobs                                             []bgprocess.JobSnapshot
		held                                             []heldcall.Running
		running, exits, waitable, hasWatched, unwatchedW bool
	}{
		{"running only", []bgprocess.JobSnapshot{running}, nil, true, false, true, false, true},
		{"watched only", []bgprocess.JobSnapshot{watched}, nil, true, false, true, true, false},
		{"watched and unwatched", []bgprocess.JobSnapshot{watched, running}, nil, true, false, true, true, true},
		{"exited only", []bgprocess.JobSnapshot{exited}, nil, false, true, false, false, false},
		{"exited with refusals", []bgprocess.JobSnapshot{refused}, nil, false, true, false, false, false},
		{"running and exited", []bgprocess.JobSnapshot{running, exited}, nil, true, true, true, false, true},
		{"held only", nil, []heldcall.Running{held}, false, false, true, false, true},
		{"held and exited", []bgprocess.JobSnapshot{exited}, []heldcall.Running{held}, false, true, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := commandJobsVars(tc.jobs, tc.held, now)
			if vars["has_running"] != tc.running || vars["has_exited"] != tc.exits || vars["can_wait"] != tc.waitable ||
				vars["has_held"] != (len(tc.held) > 0) || vars["has_watched"] != tc.hasWatched || vars["has_unwatched_wait"] != tc.unwatchedW {
				t.Fatalf("facts = %+v", vars)
			}
			rows := vars["jobs"].([]map[string]any)
			for i, job := range tc.jobs {
				row := rows[i]
				if row["handle"] != job.Handle || row["exited"] != job.Exited {
					t.Fatalf("row %d = %+v for %+v", i, row, job)
				}
				_, hasElapsed := row["elapsed"]
				if job.Exited && (row["exit_code"] != job.ExitCode || row["refusals"] != job.Refusals || hasElapsed) {
					t.Fatalf("exited row %d = %+v", i, row)
				}
				if !job.Exited && (row["mode"] != job.Mode || !hasElapsed) {
					t.Fatalf("running row %d = %+v", i, row)
				}
			}
			for _, surface := range []string{"coordinator", "worker"} {
				if RenderCommandJobsBlock(context.Background(), promptstest.InjectRenderer(t), "session", tc.jobs, tc.held, now, surface) == "" {
					t.Fatalf("%s ledger did not render", surface)
				}
			}
		})
	}
}
