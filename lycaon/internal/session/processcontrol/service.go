package processcontrol

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
}
type Verification interface {
	RecordSourceRunTerminal(context.Context, *api.Session, string, string, tools.SourceRunCapture, time.Time)
}
type ToolPolicy interface {
	AfterTool(context.Context, *api.Session, string, map[string]any, string, int, guidance.ToolResultFacts) (string, guidance.ToolResultFacts)
}
type Guidance interface {
	Emit(context.Context, string, anchor.ID, anchor.Envelope)
}
type SubjectDropper interface {
	DropSubject(sessionID string, id anchor.ID, subject string) bool
}
type Waits interface {
	SessionSleepingOnProcess(sessionID, handle string) bool
	// SessionSleepingOnRefusal also covers a process the session watches.
	SessionSleepingOnRefusal(sessionID, handle string) bool
}
type Nudges interface {
	NudgeProcessRefused(context.Context, string, string, anchor.Envelope)
	NudgeProcessFinished(context.Context, string, string, anchor.Envelope)
}
type Service struct {
	Background     *bgprocess.Registry
	Held           *heldcall.Registry
	Verification   Verification
	ToolPolicy     ToolPolicy
	Guidance       Guidance
	store          Sessions
	waits          Waits
	nudges         Nudges
	watches        *watch.Store
	processReports processReports
}

func New(sessions Sessions, verification Verification, policy ToolPolicy, guidance Guidance) *Service {
	return &Service{store: sessions, Verification: verification, ToolPolicy: policy, Guidance: guidance}
}
func (s *Service) Observe(sessionID, handle string) {
	if report, ok := s.processReports.lookup(sessionID, handle); ok {
		report.exit.Observe()
	} else {
		s.Background.Observe(sessionID, handle)
	}
}

func (s *Service) SetLoop(waits Waits, nudges Nudges) { s.waits, s.nudges = waits, nudges }
func (s *Service) SetWatches(watches *watch.Store) {
	s.watches = watches
	if watches != nil {
		watches.SetProcessKickTransfer(s.dropQueuedProcessKick)
		watches.SetProcessObserver(s.Observe)
	}
}

// LedgerJobs returns the session's command-job ledger annotated with any active watch handle.
func (s *Service) LedgerJobs(sessionID string) []bgprocess.JobSnapshot {
	if s == nil || s.Background == nil {
		return nil
	}
	jobs := s.Background.LedgerJobs(sessionID)
	if len(jobs) == 0 || !s.watches.Configured() {
		return jobs
	}
	watched := s.watches.WatchedCommandHandles(context.Background(), sessionID)
	if len(watched) == 0 {
		return jobs
	}
	for i := range jobs {
		jobs[i].WatchHandle = watched[jobs[i].Handle]
	}
	return jobs
}

func (s *Service) HasActionableWatches(ctx context.Context, sessionID string) bool {
	if s == nil || !s.watches.Configured() {
		return false
	}
	sess, err := s.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		ok, _ := watch.HasActionableWatches(ctx, s.watches, watch.SessionOwner(sessionID))
		return ok
	}
	if sess.IsWorkerChild() {
		var count int
		_ = s.watches.Database().QueryRowContext(ctx, `SELECT COUNT(*) FROM watch_registrations wr
			WHERE wr.session_id = ? AND wr.owner_kind = 'job'
			  AND (wr.state = 'live' OR EXISTS (
			      SELECT 1 FROM watch_matches wm WHERE wm.watch_id = wr.watch_id AND wm.reservation_state = 'pending'
			  ))`, sessionID).Scan(&count)
		return count > 0
	}
	ok, _ := watch.HasActionableWatches(ctx, s.watches, watch.SessionOwner(sessionID))
	return ok
}
func (s *Service) Run(ctx context.Context, spec heldcall.Spec, fn heldcall.Func) (heldcall.Outcome, error) {
	if s.Held == nil {
		settled := fn(ctx)
		return heldcall.Outcome{Settled: &settled}, nil
	}
	return s.Held.Run(ctx, spec, fn)
}
