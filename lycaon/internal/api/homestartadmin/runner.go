package homestartadmin

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/internal/api/sessionadmin"
	"github.com/lycaon/lycaon/internal/homestart"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// runs admits at most one creation run per launch.
type runs struct {
	mu     sync.Mutex
	active map[string]struct{}
}

func newRuns() *runs { return &runs{active: make(map[string]struct{})} }

func (r *runs) begin(launchID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.active[launchID]; ok {
		return false
	}
	r.active[launchID] = struct{}{}
	return true
}

func (r *runs) end(launchID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, launchID)
}

// ResumeUnfinished continues creation an engine stop interrupted.
func (h *Handler) ResumeUnfinished(ctx context.Context) error {
	ids, err := h.deps.Store.ListUnfinished(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		rec, err := h.deps.Store.Load(ctx, id)
		if err != nil {
			return err
		}
		h.schedule(ctx, rec)
	}
	return nil
}

func (h *Handler) schedule(ctx context.Context, rec homestart.Record) {
	if !rec.Unfinished() || !h.runs.begin(rec.LaunchID) {
		return
	}
	h.background.Go(ctx, func(ctx context.Context) {
		defer h.runs.end(rec.LaunchID)
		h.advance(ctx, rec.LaunchID)
	})
}

// advance commits the remaining creation steps. Each step's rows and its
// progress commit together, so a step is either absent or complete.
func (h *Handler) advance(ctx context.Context, launchID string) {
	perf := observability.StartPerformanceOperation("home_start.create", map[string]string{"launch_id": launchID})
	outcome := "error"
	defer func() { perf.End(outcome) }()
	for {
		rec, err := h.deps.Store.Load(ctx, launchID)
		if err != nil {
			slog.ErrorContext(ctx, "load home start", "launch_id", launchID, "err", err)
			return
		}
		if !rec.Unfinished() || rec.FailureCode != "" {
			outcome = "settled"
			return
		}
		actor, ok := h.actor(ctx, rec)
		if !ok {
			h.fail(ctx, rec, failureFor(rec.Progress), false)
			return
		}
		stepCtx := people.WithCaller(ctx, actor)
		switch rec.Progress {
		case homestart.ProgressReserved:
			if !h.createProject(stepCtx, rec) {
				return
			}
			perf.Mark("project")
		case homestart.ProgressProjectCreated:
			if !h.createSession(stepCtx, rec) {
				return
			}
			perf.Mark("session")
		}
	}
}

func (h *Handler) createProject(ctx context.Context, rec homestart.Record) bool {
	p, err := h.deps.Projects.CreateReserved(ctx, rec.ProjectID, project.CreateParams{Draft: true},
		h.deps.Store.CommitProgress(rec.LaunchID, homestart.ProgressReserved, homestart.ProgressProjectCreated))
	if err != nil {
		if errors.Is(err, homestart.ErrClosed) {
			return false
		}
		slog.WarnContext(ctx, "create home start project", "launch_id", rec.LaunchID, "project_id", rec.ProjectID, "err", err)
		if errors.Is(err, project.ErrDraftWorkspaceOccupied) {
			h.fail(ctx, rec, homestart.FailureDraftWorkspaceOccupied, false)
		} else {
			h.fail(ctx, rec, homestart.FailureProjectCreate, true)
		}
		return false
	}
	h.deps.ProjectEffects.PublishProjectCreated(ctx, p)
	// Indexing, observation and warming never hold the chat's binding.
	h.background.Go(ctx, func(ctx context.Context) {
		h.deps.ProjectEffects.PrepareCreatedProject(ctx, p)
	})
	return true
}

func (h *Handler) createSession(ctx context.Context, rec homestart.Record) bool {
	req := wire.CreateSessionRequest{ProjectID: rec.ProjectID, Posture: wire.SessionPostureBuild}
	if rec.Choices != nil {
		req.Mode, req.ProviderID, req.Model = rec.Choices.Mode, rec.Choices.ProviderID, rec.Choices.Model
	}
	_, err := h.deps.Sessions.CreateReservedSession(ctx, req, sessionadmin.SessionReservation{
		ID:     rec.SessionID,
		Commit: h.deps.Store.CommitProgress(rec.LaunchID, homestart.ProgressProjectCreated, homestart.ProgressSessionBound),
	})
	if err != nil {
		if errors.Is(err, homestart.ErrClosed) || errors.Is(err, project.ErrNotFound) {
			return false
		}
		slog.WarnContext(ctx, "create home start chat", "launch_id", rec.LaunchID, "session_id", rec.SessionID, "err", err)
		h.fail(ctx, rec, homestart.FailureSessionCreate, true)
		return false
	}
	return true
}

// actor is the start's person when that person may still act on this host.
func (h *Handler) actor(ctx context.Context, rec homestart.Record) (people.Person, bool) {
	owner, err := h.deps.People.HostOwner(ctx)
	if err != nil || owner.ID != rec.PersonID {
		return people.Person{}, false
	}
	return owner, true
}

func (h *Handler) fail(ctx context.Context, rec homestart.Record, code string, resumable bool) {
	if err := h.deps.Store.Fail(ctx, rec.LaunchID, rec.Progress, code, resumable); err != nil {
		slog.ErrorContext(ctx, "record home start failure", "launch_id", rec.LaunchID, "err", err)
	}
}

func failureFor(progress homestart.Progress) string {
	if progress == homestart.ProgressReserved {
		return homestart.FailureProjectCreate
	}
	return homestart.FailureSessionCreate
}
