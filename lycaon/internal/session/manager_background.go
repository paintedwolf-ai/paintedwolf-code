package session

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetBackgroundRegistry wires session-scoped background command process lifecycle.
func (m *Manager) SetBackgroundRegistry(reg *bgprocess.Registry) {
	if m != nil {
		m.bgRegistry = reg
		_ = m.RegisterSessionCleanup("background-processes", 20, reg.Lifecycle.DisposeSession)
	}
}

// StopBackgroundProcess requests termination through the shared command registry.
func (m *Manager) StopBackgroundProcess(sessionID, handle string) (*api.BackgroundProcessStopResult, error) {
	if m == nil || m.bgRegistry == nil {
		return nil, fmt.Errorf("background registry unavailable")
	}
	if isHeldHandle(handle) {
		if result, err := m.heldCalls.Stop(sessionID, handle); err == nil {
			return &api.BackgroundProcessStopResult{ProcessID: result.Handle, StopRequested: result.Running, Running: result.Running}, nil
		}
	}
	return m.bgRegistry.Lifecycle.Stop(sessionID, handle)
}

// ListBackgroundProcesses returns visible processes for session recovery.
func (m *Manager) ListBackgroundProcesses(ctx context.Context, sessionID string) []api.BackgroundProcess {
	if m == nil || m.bgRegistry == nil {
		return nil
	}
	processes := m.bgRegistry.Output.List(ctx, sessionID)
	budget := 32768 / max(1, len(processes))
	for i := range processes {
		output, err := m.GetBackgroundProcessOutput(ctx, sessionID, processes[i].ProcessID)
		if err != nil {
			continue
		}
		capBackgroundOutput(output, budget)
		processes[i].Output = output
	}
	for _, held := range m.heldCalls.List(sessionID) {
		if output, ok := m.heldCallOutput(sessionID, held.ProcessID); ok {
			held.Output = output
		}
		processes = append(processes, held)
	}
	return processes
}

// GetBackgroundProcessOutput returns the bounded retained output tail of one visible process.
func (m *Manager) GetBackgroundProcessOutput(ctx context.Context, sessionID, handle string) (*api.BackgroundProcessOutput, error) {
	if m == nil || m.bgRegistry == nil {
		return nil, fmt.Errorf("background registry unavailable")
	}
	if isHeldHandle(handle) {
		if output, ok := m.heldCallOutput(sessionID, handle); ok {
			return output, nil
		}
	}
	output, err := m.bgRegistry.Output.ReadOutput(ctx, sessionID, handle)
	if err != nil {
		return nil, err
	}
	capBackgroundOutput(&output, 8192)
	return &output, nil
}

// Cut only after screening: a preview cannot expose a fragment of a secret.
func capBackgroundOutput(output *api.BackgroundProcessOutput, budget int) {
	remaining := budget
	chunks := append([]api.BackgroundProcessChunk(nil), output.Chunks...)
	first := len(chunks)
	for first > 0 && remaining > 0 {
		first--
		text := chunks[first].Text
		if len(text) > remaining {
			start := len(text) - remaining
			for start < len(text) && !utf8.RuneStart(text[start]) {
				start++
			}
			chunks[first].Text = text[start:]
			output.Truncated = true
		}
		remaining -= len(chunks[first].Text)
	}
	if first > 0 {
		output.Truncated = true
	}
	output.Chunks = chunks[first:]
}
