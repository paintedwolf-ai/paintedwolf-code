package wiring

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/app"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/testutil"
)

// WaitForSessionProgress waits for a condition only host work on the session
// can bring about. It fails, with the session's recent wake decisions, once the
// host has held no work that could still make the condition true for
// testutil.ProgressIdleWindow.
func WaitForSessionProgress(t testing.TB, host *app.ServeApp, sessionID, step string, cond func() bool) {
	t.Helper()
	testutil.WaitForProgress(t, step, cond, func() testutil.Progress {
		return sessionProgress(t.Context(), host, sessionID)
	})
}

// GoPrompt submits a prompt from a new goroutine. The session counts as busy
// from this call, so a progress wait cannot see it idle before the goroutine
// reaches the host.
func GoPrompt(ctx context.Context, host *app.ServeApp, sessionID, text string) <-chan error {
	release := host.Sessions.Manager.Submissions.HoldActivity(sessionID)
	done := make(chan error, 1)
	go func() {
		defer release()
		_, err := host.Sessions.Manager.Submissions.Prompt(ctx, sessionID, text)
		done <- err
	}()
	return done
}

func sessionProgress(ctx context.Context, host *app.ServeApp, sessionID string) testutil.Progress {
	activity := host.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Activity(ctx, sessionID)
	return testutil.Progress{Epoch: activity.Epoch, Idle: len(activity.Busy) == 0, Report: formatActivity(sessionID, activity)}
}

func formatActivity(sessionID string, activity loopwake.SessionActivity) string {
	var b strings.Builder
	fmt.Fprintf(&b, "session %s host work: ", sessionID)
	if len(activity.Busy) == 0 {
		b.WriteString("none")
	}
	for i, kind := range activity.Busy {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(string(kind))
	}
	b.WriteString("\nwake decisions, oldest first:")
	if len(activity.Decisions) == 0 {
		b.WriteString(" none")
	}
	for _, d := range activity.Decisions {
		fmt.Fprintf(&b, "\n  %s", d.Wake)
		if d.Inform != "" && d.Inform != d.Wake {
			fmt.Fprintf(&b, " (inform %s)", d.Inform)
		}
		if d.LegID != "" {
			fmt.Fprintf(&b, " leg %s", d.LegID)
		}
		if d.JobID != "" {
			fmt.Fprintf(&b, " job %s", d.JobID)
		}
		fmt.Fprintf(&b, ": %s", d.Outcome)
	}
	return b.String()
}
