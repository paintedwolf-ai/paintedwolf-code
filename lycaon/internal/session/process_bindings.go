package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/heldcall"
)

func (m *Host) SetBackgroundRegistry(reg *bgprocess.Registry) {
	m.Processes.Background = reg
	m.Runner.Execution.SetProcesses(reg)
	m.Chats.Protection.SetJobs(reg)
	m.ProjectControl.SetProcesses(reg)
	_ = m.Resources.RegisterCleanup("awaited-processes", 20, func(ctx context.Context, id string) error {
		return reg.Lifecycle.DisposeAwaitedSession(ctx, id, bgprocess.TerminationReasonFromContext(ctx, bgprocess.TerminationStoppedByUser))
	})
	_ = m.Resources.RegisterDisposal("background-processes", 20, func(ctx context.Context, id string) error {
		return reg.Lifecycle.DisposeSession(ctx, id, bgprocess.TerminationReasonFromContext(ctx, bgprocess.TerminationChatDeleted))
	})
}
func (m *Host) SetHeldCalls(reg *heldcall.Registry) {
	m.Processes.Held = reg
	_ = m.Resources.RegisterCleanup("held-calls", 20, func(_ context.Context, id string) error { reg.DisposeSession(id); return nil })
}
