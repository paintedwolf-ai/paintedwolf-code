// Package attention derives cross-project attention state for sessions awaiting human action.
package attention

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// Candidate represents a session candidate for the attention view.
type Candidate struct {
	SessionID string
	ProjectID string
	Title     string
	Status    api.SessionStatus
	// StatusSince is when the session entered Status.
	StatusSince time.Time
	// SeenAt is when the person last had this chat readable on screen. Nil
	// means never, so a finish with no seen mark counts as unseen.
	SeenAt *time.Time
}

// CandidateSource enumerates attention candidates across every project.
type CandidateSource interface {
	ListAttentionCandidates(ctx context.Context) ([]Candidate, error)
}

// PendingCheckpointSource reports when each session's oldest unresolved human
// checkpoint was issued, keyed by session id. Worker checkpoints also contribute
// to their parent. Sessions with nothing pending in their scope are absent.
type PendingCheckpointSource interface {
	OldestPendingCheckpoints(ctx context.Context) (map[string]time.Time, error)
}

// AskLatchSource reports whether a session is holding an open ask latch —
// ask_user / request_user_feedback awaiting an answer, or a plan awaiting human
// approval — and when that wait opened. Both park the turn on a person. A
// failed read returns err; false means the session is not waiting.
type AskLatchSource interface {
	PendingAsk(ctx context.Context, sessionID string) (time.Time, bool, error)
}

// FinishSource reports when each session's newest turn completed, keyed by
// session id. Only completed turns count; sessions without one are absent.
type FinishSource interface {
	LatestFinishes(ctx context.Context) (map[string]time.Time, error)
}

// ProjectNamer resolves a project display name. An unnamed project yields "".
type ProjectNamer interface {
	ProjectName(ctx context.Context, projectID string) string
}

// Source builds attention views. Unconfigured sources contribute no rows;
// failed reads abort the snapshot so clients retain their last known view.
type Source struct {
	Sessions    CandidateSource
	Checkpoints PendingCheckpointSource
	Asks        AskLatchSource
	Finishes    FinishSource
	Projects    ProjectNamer
}

// BuildView returns every session that needs a person or is running, across
// every project, worst class first. Idle sessions with nothing pending are
// never read, so the view stays small enough to publish whole on every change.
func (s *Source) BuildView(ctx context.Context) (api.AttentionView, error) {
	view := api.AttentionView{Rows: []api.AttentionRow{}}
	if s == nil || s.Sessions == nil {
		return view, nil
	}
	candidates, err := s.Sessions.ListAttentionCandidates(ctx)
	if err != nil {
		return view, err
	}
	pending, err := s.oldestPendingCheckpoints(ctx)
	if err != nil {
		return view, err
	}
	finished, err := s.latestFinishes(ctx)
	if err != nil {
		return view, err
	}
	names := map[string]string{}
	for _, candidate := range candidates {
		row, ok, err := s.classify(ctx, candidate, pending, finished)
		if err != nil {
			return api.AttentionView{}, err
		}
		if !ok {
			continue
		}
		projectID := strings.TrimSpace(candidate.ProjectID)
		name, cached := names[projectID]
		if !cached && s.Projects != nil {
			name = s.Projects.ProjectName(ctx, projectID)
			names[projectID] = name
		}
		row.ProjectName = name
		view.Rows = append(view.Rows, row)
	}
	SortRows(view.Rows)
	return view, nil
}

// classify resolves one candidate's attention class from machine state.
// Blocking beats running: a busy session holding a pending checkpoint is
// waiting on a person, not making progress.
func (s *Source) classify(
	ctx context.Context,
	candidate Candidate,
	pending map[string]time.Time,
	finished map[string]time.Time,
) (api.AttentionRow, bool, error) {
	row := api.AttentionRow{
		SessionID: candidate.SessionID,
		ProjectID: strings.TrimSpace(candidate.ProjectID),
		Title:     strings.TrimSpace(candidate.Title),
	}
	if at, ok := pending[candidate.SessionID]; ok {
		row.Class = api.AttentionClassNeedsYou
		row.Reason = api.AttentionReasonCheckpoint
		row.SinceAt = at
		return row, true, nil
	}
	askedAt, ask, err := s.pendingAsk(ctx, candidate.SessionID)
	if err != nil {
		return api.AttentionRow{}, false, err
	}
	if ask {
		row.Class = api.AttentionClassNeedsYou
		row.Reason = api.AttentionReasonAsk
		row.SinceAt = askedAt
		return row, true, nil
	}
	switch candidate.Status {
	case api.SessionStatusError:
		row.Class = api.AttentionClassError
		row.Reason = api.AttentionReasonTurnError
		row.SinceAt = candidate.StatusSince
		return row, true, nil
	case api.SessionStatusBusy:
		row.Class = api.AttentionClassRunning
		row.Reason = api.AttentionReasonTurnRunning
		row.SinceAt = candidate.StatusSince
		return row, true, nil
	case api.SessionStatusIdle:
		// Only reading the chat clears this row.
		if at, ok := unseenFinish(finished[candidate.SessionID], candidate.SeenAt); ok {
			row.Class = api.AttentionClassFinished
			row.Reason = api.AttentionReasonTurnFinished
			row.SinceAt = at
			return row, true, nil
		}
	case api.SessionStatusPreparing:
	}
	return api.AttentionRow{}, false, nil
}

// unseenFinish reports the completion the person has not read yet. A zero
// finish means no turn ever completed; a nil seen mark means never read.
func unseenFinish(finishedAt time.Time, seenAt *time.Time) (time.Time, bool) {
	if finishedAt.IsZero() {
		return time.Time{}, false
	}
	if seenAt != nil && !finishedAt.After(*seenAt) {
		return time.Time{}, false
	}
	return finishedAt, true
}

func (s *Source) pendingAsk(ctx context.Context, sessionID string) (time.Time, bool, error) {
	if s.Asks == nil {
		return time.Time{}, false, nil
	}
	since, pending, err := s.Asks.PendingAsk(ctx, sessionID)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("attention pending ask for %s: %w", sessionID, err)
	}
	return since, pending, nil
}

func (s *Source) latestFinishes(ctx context.Context) (map[string]time.Time, error) {
	if s.Finishes == nil {
		return nil, nil
	}
	finished, err := s.Finishes.LatestFinishes(ctx)
	if err != nil {
		return nil, fmt.Errorf("attention latest finishes: %w", err)
	}
	return finished, nil
}

func (s *Source) oldestPendingCheckpoints(ctx context.Context) (map[string]time.Time, error) {
	if s.Checkpoints == nil {
		return nil, nil
	}
	pending, err := s.Checkpoints.OldestPendingCheckpoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("attention pending checkpoints: %w", err)
	}
	return pending, nil
}

// SortRows orders a view for display: worst class first, then longest-waiting
// first inside a class, then session id so the order never flickers between two
// rows that entered the same state in the same millisecond.
func SortRows(rows []api.AttentionRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ra, rb := api.AttentionRank(a.Class), api.AttentionRank(b.Class); ra != rb {
			return ra < rb
		}
		if !a.SinceAt.Equal(b.SinceAt) {
			return a.SinceAt.Before(b.SinceAt)
		}
		return a.SessionID < b.SessionID
	})
}
