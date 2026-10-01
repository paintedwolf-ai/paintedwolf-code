package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetHeldCalls wires the registry that holds tool calls past their foreground wait.
func (m *Manager) SetHeldCalls(reg *heldcall.Registry) {
	if m == nil {
		return
	}
	m.heldCalls = reg
	_ = m.RegisterSessionCleanup("held-calls", 20, func(_ context.Context, sessionID string) error {
		reg.DisposeSession(sessionID)
		return nil
	})
}

// HandleHeldCallSettled wakes a wait subscribed to a held call that settled.
func (m *Manager) HandleHeldCallSettled(sessionID, handle string) {
	if m == nil || m.heldCalls == nil {
		return
	}
	status, err := m.heldCalls.Status(sessionID, handle)
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

// heldCallPort supervises detachable tool calls for the prompt loop. Without a
// registry a call runs to completion in the foreground.
type heldCallPort struct{ m *Manager }

func (p heldCallPort) Run(ctx context.Context, spec heldcall.Spec, fn heldcall.Func) (heldcall.Outcome, error) {
	if p.m == nil || p.m.heldCalls == nil {
		settled := fn(ctx)
		return heldcall.Outcome{Settled: &settled}, nil
	}
	return p.m.heldCalls.Run(ctx, spec, fn)
}

// processHandleState answers for a background handle of either kind.
func (m *Manager) processHandleState(sessionID, handle string) (known, running bool) {
	if m == nil {
		return false, false
	}
	if known, running = m.bgRegistry.State(sessionID, handle); known {
		return known, running
	}
	return m.heldCalls.State(sessionID, handle)
}

// processHandlesRunning reports whether a listed handle of either kind still runs.
func (m *Manager) processHandlesRunning(sessionID string, handles []string) bool {
	if m == nil {
		return false
	}
	return m.bgRegistry.HasRunningHandles(sessionID, handles) || m.heldCalls.HasRunningHandles(sessionID, handles)
}

// heldCallOutput describes a held call for a client that asks for its output.
func (m *Manager) heldCallOutput(sessionID, handle string) (*api.BackgroundProcessOutput, bool) {
	status, err := m.heldCalls.Status(sessionID, handle)
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
