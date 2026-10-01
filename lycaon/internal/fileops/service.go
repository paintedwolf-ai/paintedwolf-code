package fileops

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/keylock"
)

var ErrCapacity = errors.New("file operation queue is full")

type Service struct {
	store     *Store
	mu        sync.Mutex
	active    map[string]*Run
	workers   chan struct{}
	projects  keylock.Group
	observers []func(Request)
}

// Observe receives every persisted request state in order, outside the service's locks.
func (s *Service) Observe(observer func(Request)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observers = append(s.observers, observer)
}

func (s *Service) notify(request Request) {
	s.mu.Lock()
	observers := slices.Clone(s.observers)
	s.mu.Unlock()
	for _, observer := range observers {
		observer(request)
	}
}

type Run struct {
	service    *Service
	mu         sync.Mutex
	request    Request
	done       chan struct{}
	canceled   chan struct{}
	cancelOnce sync.Once
	lastSaved  time.Time
}

type Outcome struct {
	Status int
	Body   string
}

func NewService(store *Store) *Service {
	return &Service{store: store, active: make(map[string]*Run), workers: make(chan struct{}, 4)}
}

func (s *Service) Recover(ctx context.Context, reconcile func(context.Context, Request) (Outcome, bool, error)) error {
	return s.store.Recover(ctx, reconcile)
}

// Admit persists acceptance before the transport may acknowledge it.
func (s *Service) Admit(ctx context.Context, request Request, retry bool) (*Run, bool, error) {
	run, created, err := s.admit(ctx, request, retry)
	if err == nil && created {
		s.notify(run.Snapshot())
	}
	return run, created, err
}

func (s *Service) admit(ctx context.Context, request Request, retry bool) (*Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, err := s.store.Get(ctx, request.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	if err == nil {
		if held.InputDigest != request.InputDigest || held.PersonID != request.PersonID || held.ProjectID != request.ProjectID || held.RootScope != request.RootScope {
			return nil, false, ErrConflict
		}
		if running := s.active[request.ID]; running != nil {
			select {
			case <-running.Done():
				if !retry {
					return running, false, nil
				}
				held = running.Snapshot()
			default:
				return running, false, nil
			}
		}
		if !retry || held.State == "completed" {
			run := newRun(s, held)
			close(run.done)
			return run, false, nil
		}
		request = held
	}
	if len(s.active) >= 64 && s.active[request.ID] == nil {
		return nil, false, ErrCapacity
	}
	now := time.Now().UTC()
	request.State, request.Phase, request.Cancelable = "queued", "preparing", true
	request.ResponseStatus, request.ResponseBody = 0, ""
	request.EntriesProcessed, request.BytesProcessed = 0, 0
	request.UpdatedAt = now
	request.CompletedAt = nil
	if errors.Is(err, ErrNotFound) {
		request.CreatedAt = now
		err = s.store.Insert(ctx, request)
	} else {
		err = s.store.Update(ctx, request)
	}
	if err != nil {
		return nil, false, err
	}
	run := newRun(s, request)
	s.active[request.ID] = run
	return run, true, nil
}

func newRun(s *Service, r Request) *Run {
	return &Run{service: s, request: r, done: make(chan struct{}), canceled: make(chan struct{})}
}
func (r *Run) Done() <-chan struct{} { return r.done }
func (r *Run) Snapshot() Request     { r.mu.Lock(); defer r.mu.Unlock(); return r.request }

func (s *Service) Get(ctx context.Context, id string) (Request, error) {
	s.mu.Lock()
	run := s.active[id]
	s.mu.Unlock()
	if run != nil {
		return run.Snapshot(), nil
	}
	return s.store.Get(ctx, id)
}

func (s *Service) List(ctx context.Context, projectID, personID string) ([]Request, error) {
	requests, err := s.store.List(ctx, projectID, personID)
	if err != nil {
		return nil, err
	}
	for i := range requests {
		if current, err := s.Get(ctx, requests[i].ID); err == nil {
			requests[i] = current
		}
	}
	return requests, nil
}

func (s *Service) Cancel(id string) error {
	s.mu.Lock()
	run := s.active[id]
	s.mu.Unlock()
	if run == nil {
		return ErrNotCancelable
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if !run.request.Cancelable {
		return ErrNotCancelable
	}
	run.cancelOnce.Do(func() { close(run.canceled) })
	return nil
}

func (r *Run) Report(ctx context.Context, phase string, entries, bytes int64) {
	r.mu.Lock()
	changed := r.request.Phase != phase
	r.request.Phase, r.request.EntriesProcessed, r.request.BytesProcessed = phase, entries, bytes
	persisted := false
	if changed || time.Since(r.lastSaved) >= time.Second {
		r.request.UpdatedAt = time.Now().UTC()
		if r.service.store.Update(ctx, r.request) == nil {
			r.lastSaved = r.request.UpdatedAt
			persisted = true
		}
	}
	snapshot := r.request
	r.mu.Unlock()
	if persisted {
		r.service.notify(snapshot)
	}
}

// BeginEffect enters the irreversible effect unless cancellation won the run lock first.
func (r *Run) BeginEffect(ctx context.Context) error {
	r.mu.Lock()
	select {
	case <-r.canceled:
		r.mu.Unlock()
		return context.Canceled
	default:
	}
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return err
	}
	r.request.Phase, r.request.Cancelable = "applying", false
	r.request.UpdatedAt = time.Now().UTC()
	err := r.service.store.Update(ctx, r.request)
	snapshot := r.request
	r.mu.Unlock()
	if err == nil {
		r.service.notify(snapshot)
	}
	return err
}

func (r *Run) Execute(parent context.Context, work func(context.Context) Outcome) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() {
		select {
		case <-r.canceled:
			cancel()
		case <-ctx.Done():
		}
	}()
	outcome := Outcome{Status: 500, Body: `{"code":"internal_error","error":"The file operation was interrupted."}`}
	defer func() { r.finish(context.WithoutCancel(parent), ctx, outcome) }()
	// Project queues wait without occupying global workers.
	release, err := r.service.projects.Acquire(ctx, r.Snapshot().ProjectID)
	if err != nil {
		return
	}
	defer release()
	select {
	case r.service.workers <- struct{}{}:
		defer func() { <-r.service.workers }()
	case <-ctx.Done():
		return
	}
	r.mu.Lock()
	r.request.State = "running"
	r.mu.Unlock()
	r.Report(ctx, "preparing", 0, 0)
	outcome = work(ctx)
}

func (r *Run) finish(ctx, runContext context.Context, outcome Outcome) {
	r.mu.Lock()
	if outcome.Status == 0 {
		outcome.Status = 204
	}
	r.request.ResponseStatus, r.request.ResponseBody = outcome.Status, outcome.Body
	r.request.State = "completed"
	if outcome.Status >= 400 {
		r.request.State = "failed"
	}
	if runContext.Err() != nil && outcome.Status >= 400 {
		r.request.State = "interrupted"
		select {
		case <-r.canceled:
			if r.request.Cancelable {
				r.request.State = "canceled"
			}
		default:
		}
	}
	r.request.Cancelable = false
	now := time.Now().UTC()
	r.request.UpdatedAt = now
	r.request.CompletedAt = &now
	persistErr := r.service.store.Update(ctx, r.request)
	if persistErr != nil {
		r.request.State = "interrupted"
		r.request.ResponseStatus = 500
		r.request.ResponseBody = `{"code":"internal_error","error":"The file operation result could not be recorded. Review file history before retrying."}`
	}
	id := r.request.ID
	snapshot := r.request
	r.mu.Unlock()
	if persistErr == nil {
		r.service.mu.Lock()
		delete(r.service.active, id)
		r.service.mu.Unlock()
	}
	// Observers see the terminal state before waiters resume.
	r.service.notify(snapshot)
	close(r.done)
}
