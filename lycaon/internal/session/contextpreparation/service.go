// Package contextpreparation schedules disposable prefix work for the viewed chat.
package contextpreparation

import (
	"context"
	"sync"
)

type Candidate struct {
	Key string
	Run func(context.Context) bool
}

type Build func(context.Context, string) (Candidate, error)

// Service retains one focus and serializes preparation behind cancellation.
type Service struct {
	mu                          sync.Mutex
	build                       Build
	wake                        chan struct{}
	done                        chan struct{}
	started, stopped, suspended bool
	focus, project, completed   string
	ctx                         context.Context
	revision                    uint64
	cancel                      context.CancelFunc
}

func New(build Build) *Service {
	return &Service{build: build, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (s *Service) Focus(ctx context.Context, session, project string) {
	if s == nil || session == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if s.focus != session || s.project != project {
		s.invalidateLocked()
		s.focus, s.project = session, project
		s.suspended = false
		s.ctx = context.WithoutCancel(ctx)
	}
	s.wakeLocked()
}

// Changed invalidates only the focused scope; empty scope denotes device settings.
func (s *Service) Changed(session, project string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.focus == "" || session != "" && session != s.focus || project != "" && project != s.project {
		return
	}
	s.invalidateLocked()
	s.wakeLocked()
}

// Retry admits another attempt without interrupting an unchanged candidate.
func (s *Service) Retry(session string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped && s.focus == session {
		s.suspended = false
		s.wakeLocked()
	}
}

func (s *Service) invalidateLocked() {
	s.revision++
	s.completed = ""
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *Service) wakeLocked() {
	if s.suspended || s.focus == "" {
		return
	}
	if !s.started {
		s.started = true
		go s.work()
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) work() {
	defer close(s.done)
	for range s.wake {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		if s.suspended || s.focus == "" {
			s.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(s.ctx)
		focus, revision := s.focus, s.revision
		s.cancel = cancel
		s.mu.Unlock()
		candidate, err := s.build(ctx, focus)
		s.mu.Lock()
		valid := err == nil && ctx.Err() == nil && revision == s.revision && candidate.Key != "" && candidate.Key != s.completed && candidate.Run != nil
		s.mu.Unlock()
		prepared := valid && candidate.Run(ctx)
		s.mu.Lock()
		if prepared && ctx.Err() == nil && revision == s.revision {
			s.completed = candidate.Key
		}
		cancel()
		s.cancel = nil
		s.mu.Unlock()
	}
}

func (s *Service) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	s.invalidateLocked()
	if s.started {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	} else {
		close(s.done)
	}
}

func (s *Service) Wait(ctx context.Context) error {
	if s == nil {
		return nil
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Suspend cancels preparation until the focused chat becomes idle.
func (s *Service) Suspend(session string, forget bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.focus != session || s.stopped {
		return
	}
	s.invalidateLocked()
	s.suspended = true
	if forget {
		s.focus, s.project, s.ctx = "", "", nil
	}
}
