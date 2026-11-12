package briefing

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
)

var (
	// ErrStopped reports admission after shutdown began.
	ErrStopped = errors.New("briefing scheduler stopped")
	// ErrBusy reports a full project queue; the caller may retry later.
	ErrBusy = errors.New("briefing queue is full")
	// ErrQueueExpired reports work that waited past its bound without starting.
	ErrQueueExpired = errors.New("briefing waited too long to start")
)

// Admission chooses how a kind's work starts.
type Admission int

const (
	// Queued work waits in a bounded per-project queue for an active slot.
	Queued Admission = iota
	// Immediate work starts at once; its own provider deadline bounds it.
	Immediate
	// Superseding work starts at once and cancels earlier automatic work in
	// the same group.
	Superseding
)

// Policy is a kind's scheduling contract.
type Policy struct {
	Admission Admission
	// QueueLimit and Active bound queued work per project.
	QueueLimit int
	Active     int
	// QueueWait bounds how long queued work may wait to start.
	QueueWait time.Duration
}

// QueuedPolicy is the step, activity and worker contract.
var QueuedPolicy = Policy{Admission: Queued, QueueLimit: 64, Active: 2, QueueWait: 30 * time.Second}

// Job is one keyed unit of work. Equal keys share one run.
type Job struct {
	Key     string
	Project string
	// Group scopes supersession; empty disables it.
	Group  string
	Manual bool
	Policy Policy
	Run    func(context.Context)
	// Expired runs instead of Run when queued work exceeds its wait.
	Expired func(context.Context)
}

// Scheduler runs jobs to completion independent of the requests that admit
// them. Navigation never cancels admitted work; shutdown and supersession do.
type Scheduler struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	stopped  bool
	running  map[string]*run
	projects map[string]*projectLane
	groups   map[string]*run
	workers  sync.WaitGroup
	now      func() time.Time
}

type run struct {
	job      Job
	ctx      context.Context
	cancel   context.CancelFunc
	queuedAt time.Time
}

type projectLane struct {
	active int
	queue  []*run
}

func NewScheduler(ctx context.Context) *Scheduler {
	ctx, cancel := context.WithCancel(ctx)
	return &Scheduler{ctx: ctx, cancel: cancel, running: map[string]*run{}, projects: map[string]*projectLane{}, groups: map[string]*run{}, now: time.Now}
}

// Submit admits a job or joins the run already holding its key. A manual job
// joining queued work promotes it ahead of automatic work. The run keeps
// ctx's values but not its cancellation.
func (s *Scheduler) Submit(ctx context.Context, job Job) (joined bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false, ErrStopped
	}
	if existing, ok := s.running[job.Key]; ok && existing.ctx.Err() == nil {
		if job.Manual && !existing.job.Manual {
			existing.job.Manual = true
			s.promote(existing)
		}
		return true, nil
	}
	runCtx, cancel := s.runContext(ctx)
	r := &run{job: job, ctx: runCtx, cancel: cancel, queuedAt: s.now()}
	switch job.Policy.Admission {
	case Queued:
		lane := s.lane(job.Project)
		if len(lane.queue) >= job.Policy.QueueLimit {
			cancel()
			return false, ErrBusy
		}
		s.running[job.Key] = r
		lane.queue = append(lane.queue, r)
		s.promote(r)
		s.drain(job.Project)
	case Superseding:
		if job.Group != "" && !job.Manual {
			if prior, ok := s.groups[job.Group]; ok {
				prior.cancel()
			}
			s.groups[job.Group] = r
		}
		s.running[job.Key] = r
		s.start(r)
	default:
		s.running[job.Key] = r
		s.start(r)
	}
	return false, nil
}

// runContext ends with the scheduler but never with the admitting request.
func (s *Scheduler) runContext(values context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(values))
	stop := context.AfterFunc(s.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}

// Promote marks queued work as manual so it starts ahead of automatic work.
func (s *Scheduler) Promote(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.running[key]; ok && !existing.job.Manual {
		existing.job.Manual = true
		s.promote(existing)
	}
}

func (s *Scheduler) lane(project string) *projectLane {
	lane, ok := s.projects[project]
	if !ok {
		lane = &projectLane{}
		s.projects[project] = lane
	}
	return lane
}

// promote moves manual queued work ahead of automatic work, keeping order
// within each class.
func (s *Scheduler) promote(r *run) {
	lane := s.lane(r.job.Project)
	at := -1
	for i, queued := range lane.queue {
		if queued == r {
			at = i
		}
	}
	if at < 0 || !r.job.Manual {
		return
	}
	insert := 0
	for insert < at && lane.queue[insert].job.Manual {
		insert++
	}
	copy(lane.queue[insert+1:at+1], lane.queue[insert:at])
	lane.queue[insert] = r
}

func (s *Scheduler) drain(project string) {
	lane := s.lane(project)
	for len(lane.queue) > 0 {
		next := lane.queue[0]
		if next.ctx.Err() != nil {
			lane.queue = lane.queue[1:]
			s.forget(next)
			continue
		}
		if s.now().Sub(next.queuedAt) > next.job.Policy.QueueWait {
			lane.queue = lane.queue[1:]
			s.expire(next)
			continue
		}
		if lane.active >= next.job.Policy.Active {
			return
		}
		lane.queue = lane.queue[1:]
		lane.active++
		s.start(next)
	}
}

// ExpireQueued settles queued work that waited past its bound. Callers invoke
// it from their own reconciliation; it never starts a timer itself.
func (s *Scheduler) ExpireQueued(project string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lane := s.lane(project)
	kept := lane.queue[:0]
	for _, queued := range lane.queue {
		if s.now().Sub(queued.queuedAt) > queued.job.Policy.QueueWait {
			s.expire(queued)
			continue
		}
		kept = append(kept, queued)
	}
	lane.queue = kept
}

func (s *Scheduler) expire(r *run) {
	s.forget(r)
	if r.job.Expired == nil {
		r.cancel()
		return
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer r.cancel()
		defer observability.GuardPanic("briefing.expire")
		r.job.Expired(context.WithoutCancel(r.ctx))
	}()
}

func (s *Scheduler) start(r *run) {
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer observability.GuardPanic("briefing.run")
		defer s.finish(r)
		r.job.Run(r.ctx)
	}()
}

func (s *Scheduler) finish(r *run) {
	r.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forget(r)
	if r.job.Policy.Admission == Queued {
		s.lane(r.job.Project).active--
		s.drain(r.job.Project)
	}
}

func (s *Scheduler) forget(r *run) {
	if s.running[r.job.Key] == r {
		delete(s.running, r.job.Key)
	}
	if r.job.Group != "" && s.groups[r.job.Group] == r {
		delete(s.groups, r.job.Group)
	}
}

// Cancel stops one kind's work, as disabling its setting requires.
func (s *Scheduler) Cancel(match func(Job) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.running {
		if match(r.job) {
			r.cancel()
		}
	}
}

// Stop closes admission before cancelling, so Wait cannot race new work.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	s.stopped = true
	s.cancel()
	s.mu.Unlock()
}

func (s *Scheduler) Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
