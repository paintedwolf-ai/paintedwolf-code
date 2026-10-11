package sourceledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/gitstate"
	"github.com/lycaon/lycaon/internal/keylock"
	"github.com/lycaon/lycaon/internal/sourcefeed"
)

type GitStateReader interface {
	// Unreadable repositories have an explicit state.
	HeadState(ctx context.Context, rootAbs string) gitstate.State
	// Newest first; errors leave the movement unexplained.
	RefLogHead(ctx context.Context, rootAbs string, limit int) ([]gitstate.RefLogEntry, error)
}

// A nil reader disables ref observations without disabling file history.
func (s *Git) SetGitReader(reader GitStateReader) {
	if s == nil {
		return
	}
	s.gitReader = reader
}

// ObserveGitState returns the terminal transition id per changed root.
// First observations seed a baseline without creating a transition.
func (s *Git) ObserveGitState(ctx context.Context, projectID string, roots []RootSpec) (map[string]string, error) {
	if s == nil || s.sqlDB == nil || s.gitReader == nil {
		return nil, nil
	}
	release, err := s.lockObservations(ctx, projectID, roots)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.observeGitState(ctx, projectID, roots)
}

// The caller holds the roots' observation locks through file reconciliation.
func (s *Git) observeGitState(ctx context.Context, projectID string, roots []RootSpec) (map[string]string, error) {
	if s.gitReader == nil {
		return nil, nil
	}
	terminal := make(map[string]string)
	var failures []error
	for _, root := range roots {
		attribution := gitAttribution{}
		if window := s.commands.attributionWindow(projectID, []RootSpec{root}); window != nil {
			attribution = gitAttribution{actor: Contributor{SessionID: window.sessionID, Turn: window.turn,
				ToolCallID: window.toolCallID, ToolName: window.toolName}, commandWindowID: window.id}
		}
		transitionID, err := s.observeRootGitState(ctx, projectID, root, attribution)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if transitionID != "" {
			terminal[root.ID] = transitionID
		}
	}
	if len(terminal) == 0 {
		return nil, errors.Join(failures...)
	}
	failures = append(failures, sourcefeed.EmitGitSignal(ctx, projectID, rootRefsOf(roots)))
	return terminal, errors.Join(failures...)
}

func (s *Git) observeRootGitState(ctx context.Context, projectID string, root RootSpec, attribution gitAttribution) (string, error) {
	next := s.gitReader.HeadState(ctx, root.Path)
	prevRow, err := s.queries.GetSourceGitHead(ctx, db.GetSourceGitHeadParams{
		ProjectID: projectID, BranchID: root.BranchID.String(), RootID: root.ID,
	})
	seeding := errors.Is(err, sql.ErrNoRows)
	if err != nil && !seeding {
		return "", err
	}
	now := time.Now().UTC()
	if seeding {
		return "", s.storeGitHead(ctx, projectID, root, next, now)
	}
	prev := gitstate.State{
		Repo:       gitstate.RepoState(prevRow.RepoState),
		HeadCommit: prevRow.HeadCommit,
		HeadRef:    prevRow.HeadRef,
	}
	if prev.Equal(next) {
		return "", nil
	}
	// The reflog read is external work and stays outside any transaction.
	var reflog []gitstate.RefLogEntry
	if prev.Repo == gitstate.RepoPresent && next.Repo == gitstate.RepoPresent {
		if entries, logErr := s.gitReader.RefLogHead(ctx, root.Path, gitstate.MaxTransitionsPerObservation*4); logErr == nil {
			reflog = entries
		}
	}
	transitions := gitstate.Classify(prev, next, reflog)
	return s.commitGitTransitions(ctx, projectID, root.ID, prevRow, next, transitions, now, attribution)
}

// A concurrent head update makes this pass a no-op.
func (s *Git) commitGitTransitions(
	ctx context.Context,
	projectID, rootID string,
	prevRow db.GetSourceGitHeadRow,
	next gitstate.State,
	transitions []gitstate.Transition,
	now time.Time,
	attribution gitAttribution,
) (string, error) {
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	current, err := q.GetSourceGitHead(ctx, db.GetSourceGitHeadParams{
		ProjectID: projectID, BranchID: prevRow.BranchID, RootID: rootID,
	})
	if err != nil {
		return "", err
	}
	if current != prevRow {
		return "", nil
	}
	terminalID := ""
	for _, transition := range transitions {
		ordinal, err := q.AdvanceSourceOrdinal(ctx, projectID)
		if err != nil {
			return "", err
		}
		terminalID = newID()
		if err := q.InsertSourceGitTransition(ctx, db.InsertSourceGitTransitionParams{
			ID: terminalID, ProjectID: projectID, BranchID: prevRow.BranchID, RootID: rootID,
			Kind:       string(transition.Kind),
			FromCommit: transition.FromCommit, ToCommit: transition.ToCommit,
			FromRef: transition.FromRef, ToRef: transition.ToRef,
			Detail: transition.Detail, Ordinal: ordinal,
			ObservedTs: now.Format(time.RFC3339Nano),
			SessionID:  attribution.actor.SessionID, Turn: int64(attribution.actor.Turn),
			ToolCallID: attribution.actor.ToolCallID, ToolName: attribution.actor.ToolName,
			CommandWindowID: db.NullString(attribution.commandWindowID),
		}); err != nil {
			return "", err
		}
	}
	if err := q.UpsertSourceGitHead(ctx, db.UpsertSourceGitHeadParams{
		ProjectID: projectID, BranchID: prevRow.BranchID, RootID: rootID, RepoState: string(next.Repo),
		HeadCommit: next.HeadCommit, HeadRef: next.HeadRef,
		ObservedTs: now.Format(time.RFC3339Nano),
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return terminalID, nil
}

func (s *Git) storeGitHead(ctx context.Context, projectID string, root RootSpec, state gitstate.State, now time.Time) error {
	return s.queries.UpsertSourceGitHead(ctx, db.UpsertSourceGitHeadParams{
		ProjectID: projectID, BranchID: root.BranchID.String(), RootID: root.ID, RepoState: string(state.Repo),
		HeadCommit: state.HeadCommit, HeadRef: state.HeadRef,
		ObservedTs: now.Format(time.RFC3339Nano),
	})
}

// GitTransition is one recorded ref movement, read back for briefs and walk
// enrichment.
type GitTransition struct {
	SessionID       string
	Turn            int
	ToolCallID      string
	ToolName        string
	CommandWindowID string
	ID              string
	ProjectID       string
	RootID          string
	Kind            string
	FromCommit      string
	ToCommit        string
	FromRef         string
	ToRef           string
	Detail          string
	Ordinal         int64
	ObservedTS      time.Time
}

// GitTransitionsBetween pages transitions in (afterOrdinal, throughOrdinal],
// newest first; throughOrdinal zero leaves the window open at the top.
func (s *Git) GitTransitionsBetween(
	ctx context.Context,
	projectID string,
	afterOrdinal, throughOrdinal int64,
	limit int,
) ([]GitTransition, error) {
	if s == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.queries.ListSourceGitTransitionsBetween(ctx, db.ListSourceGitTransitionsBetweenParams{
		ProjectID: projectID, AfterOrdinal: afterOrdinal,
		ThroughOrdinal: throughOrdinal, PageLimit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]GitTransition, 0, len(rows))
	for _, row := range rows {
		out = append(out, gitTransitionFromRow(row))
	}
	return out, nil
}

// GitTransitionsByIDs resolves recorded transitions for effect surfaces.
func (s *Git) GitTransitionsByIDs(ctx context.Context, ids []string) (map[string]GitTransition, error) {
	if s == nil || len(ids) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListSourceGitTransitionsByIDs(ctx, string(raw))
	if err != nil {
		return nil, err
	}
	out := make(map[string]GitTransition, len(rows))
	for _, row := range rows {
		out[row.ID] = gitTransitionFromRow(row)
	}
	return out, nil
}

func gitTransitionFromRow(row db.SourceGitTransitions) GitTransition {
	ts, _ := time.Parse(time.RFC3339Nano, row.ObservedTs)
	return GitTransition{
		ID: row.ID, ProjectID: row.ProjectID, RootID: row.RootID, Kind: row.Kind,
		FromCommit: row.FromCommit, ToCommit: row.ToCommit, FromRef: row.FromRef, ToRef: row.ToRef,
		Detail: row.Detail, Ordinal: row.Ordinal, ObservedTS: ts,
		SessionID: row.SessionID, Turn: int(row.Turn), ToolCallID: row.ToolCallID,
		ToolName: row.ToolName, CommandWindowID: row.CommandWindowID.String,
	}
}

// Git serializes observed ref transitions and managed Git effects.
type Git struct {
	gitObservations keylock.Group
	gitReader       GitStateReader
	queries         *db.Queries
	recordMu        *sync.Mutex
	sqlDB           db.Handle
	commands        gitCommandsPort
	inventory       gitInventoryPort
}

type gitCommandsPort interface {
	attributionWindow(projectID string, roots []RootSpec) *openCommandWindow
}

type gitInventoryPort interface {
	observePaths(ctx context.Context, projectID string, roots []RootSpec, refs []PathRef, transitionByRoot map[string]string, actor *Contributor) (int, error)
}
