// Package resources releases run state and disposes chat authority at their distinct lifetimes.
package resources

import (
	"context"

	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
)

type Service struct {
	Registry  *resourcelifecycle.Registry
	Work      *Work
	State     *TurnState
	Tools     *ToolState
	Authority *Authority
	Queue     *queue.Store
}

func New(work *Work, state *TurnState, tools *ToolState, authority *Authority, queue *queue.Store) *Service {
	s := &Service{Registry: resourcelifecycle.New(), Work: work, State: state, Tools: tools, Authority: authority, Queue: queue}
	_ = s.Registry.Register(resourcelifecycle.ScopeSession, "session-memory", 100, s.release)
	_ = s.Registry.RegisterDisposal(resourcelifecycle.ScopeSession, "chat-sandbox-authority", 101, authority.dispose)
	return s
}

func (s *Service) RegisterCleanup(name string, order int, cleanup func(context.Context, string) error) error {
	return s.Registry.Register(resourcelifecycle.ScopeSession, name, order, func(ctx context.Context, scope resourcelifecycle.Scope) error { return cleanup(ctx, scope.ID) })
}

func (s *Service) RegisterDisposal(name string, order int, cleanup func(context.Context, string) error) error {
	return s.Registry.RegisterDisposal(resourcelifecycle.ScopeSession, name, order, func(ctx context.Context, scope resourcelifecycle.Scope) error { return cleanup(ctx, scope.ID) })
}

func (s *Service) Dispose(ctx context.Context, sessionID string) {
	_ = s.Registry.Dispose(ctx, resourcelifecycle.SessionScope(sessionID))
	if s.Queue != nil {
		s.Queue.Clear(sessionID)
	}
}

func (s *Service) release(ctx context.Context, scope resourcelifecycle.Scope) error {
	if scope.ID == "" {
		return nil
	}
	s.Work.Cancel(ctx, scope.ID)
	s.State.Forget(ctx, scope.ID)
	s.Tools.Forget(scope.ID)
	s.Authority.ReleaseRun(scope.ID)
	return nil
}
