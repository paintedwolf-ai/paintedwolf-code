package processcontrol

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

// HandleHeldCallSettled wakes a wait subscribed to a held call that settled.
func (m *Service) HandleHeldCallSettled(sessionID, handle string) {
	if m == nil || m.Held == nil {
		return
	}
	status, err := m.Held.Status(sessionID, handle)
	if err != nil || status.Settled == nil {
		return
	}
	digest := fmt.Sprintf("handle=%s tool=%s mode=held outcome=%s elapsed=%s",
		status.Handle, status.Tool, status.Settled.Outcome, status.Elapsed.Round(time.Millisecond))
	env := anchor.Envelope{CommandCompletionDigest: digest, Vars: map[string]any{
		"held_call_handle": status.Handle, "held_call_tool": status.Tool,
		"held_call_outcome": string(status.Settled.Outcome),
	}}
	m.processReports.publish(sessionID, handle, digest)
	m.nudgeProcessWait(context.Background(), sessionID, handle, anchor.ProcessFinished, env)
}

// HandleState answers for a background handle of either kind.
func (m *Service) HandleState(sessionID, handle string) (known, running bool) {
	if m == nil {
		return false, false
	}
	if known, running = m.Background.State(sessionID, handle); known {
		return known, running
	}
	return m.Held.State(sessionID, handle)
}

// HandlesRunning reports whether a listed handle of either kind still runs.
func (m *Service) HandlesRunning(sessionID string, handles []string) bool {
	if m == nil {
		return false
	}
	return m.Background.HasRunningHandles(sessionID, handles) || m.Held.HasRunningHandles(sessionID, handles)
}

// heldCallOutput describes a held call for a client that asks for its output.
func (m *Service) heldCallOutput(sessionID, handle string) (*api.BackgroundProcessOutput, bool) {
	status, err := m.Held.Status(sessionID, handle)
	if err != nil {
		return nil, false
	}
	output := &api.BackgroundProcessOutput{ProcessID: status.Handle, Running: status.Running, Chunks: []api.BackgroundProcessChunk{}}
	if status.Settled != nil {
		code := 0
		if status.Settled.Outcome != api.ToolResultOutcomeCompleted {
			code = 1
		}
		output.ExitCode = &code
	}
	return output, true
}

func isHeldHandle(handle string) bool { return strings.HasPrefix(strings.TrimSpace(handle), "held-") }
