package sourceledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

const (
	CheckpointTracking   = "tracking"
	CheckpointSession    = "session"
	CheckpointTurn       = "turn"
	CheckpointNamed      = "named"
	DefaultPinPageLimit  = 100
	MaxPinPageLimit      = 500
	checkpointTimeLayout = "2006-01-02T15:04:05.000000000Z"
)

// ErrPinNotFound reports a missing named boundary.
var ErrPinNotFound = errors.New("pin not found")

// Checkpoint is an immutable manifest boundary.
type Checkpoint struct {
	ID             string
	ProjectID      string
	Kind           string
	Label          string
	ParentID       string
	SessionID      string
	Turn           int
	CreatedOrdinal int64
	CreatedTS      time.Time
}

// PinGitHead is the git position one root held when a pin was taken —
// recorded facts as of the last observation before the boundary.
type PinGitHead struct {
	RootID     string
	RepoState  string
	HeadCommit string
	HeadRef    string
}

// Pin is the named boundary exposed outside the ledger.
type Pin struct {
	ID             string
	ProjectID      string
	Label          string
	CreatedOrdinal int64
	CreatedTS      time.Time
	GitHeads       []PinGitHead
}

// PinPageQuery selects one newest-first page.
type PinPageQuery struct {
	Limit           int
	BeforeCreatedTS string
	BeforeID        string
}

// PinPage contains pins and the next keyset boundary.
type PinPage struct {
	Pins          []Pin
	NextCreatedTS string
	NextID        string
}

// StructuralCheckpointInput describes a tracking, session, or turn boundary.
type StructuralCheckpointInput struct {
	ProjectID string
	Kind      string
	Label     string
	SessionID string
	Turn      int
}

// CreateStructuralCheckpoint stores a structural manifest delta.
func (s *Checkpoints) CreateStructuralCheckpoint(ctx context.Context, in StructuralCheckpointInput) (Checkpoint, error) {
	if in.Kind == CheckpointNamed {
		return Checkpoint{}, fmt.Errorf("named boundaries are pins")
	}
	return s.createCheckpoint(ctx, in)
}

// CreatePin stores a named manifest delta.
func (s *Checkpoints) CreatePin(ctx context.Context, projectID, label string) (Pin, error) {
	checkpoint, err := s.createCheckpoint(ctx, StructuralCheckpointInput{
		ProjectID: projectID, Kind: CheckpointNamed, Label: label,
	})
	if err != nil {
		return Pin{}, err
	}
	pin := pinFromCheckpoint(checkpoint)
	heads, err := s.checkpointGitHeads(ctx, []string{pin.ID})
	if err != nil {
		return Pin{}, err
	}
	pin.GitHeads = heads[pin.ID]
	return pin, nil
}

// checkpointGitHeads batch-loads the recorded git positions behind boundaries.
func (s *Checkpoints) checkpointGitHeads(ctx context.Context, ids []string) (map[string][]PinGitHead, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListSourceCheckpointGitStatesForCheckpoints(ctx, string(raw))
	if err != nil {
		return nil, err
	}
	out := make(map[string][]PinGitHead, len(ids))
	for _, row := range rows {
		out[row.CheckpointID] = append(out[row.CheckpointID], PinGitHead{
			RootID: row.RootID, RepoState: row.RepoState,
			HeadCommit: row.HeadCommit, HeadRef: row.HeadRef,
		})
	}
	return out, nil
}

func (s *Checkpoints) createCheckpoint(ctx context.Context, in StructuralCheckpointInput) (Checkpoint, error) {
	if s == nil {
		return Checkpoint{}, fmt.Errorf("ledger not configured")
	}
	if err := validateCheckpointInput(in); err != nil {
		return Checkpoint{}, err
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()

	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return Checkpoint{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if in.Kind == CheckpointTurn {
		row, lookupErr := q.GetTurnCheckpointForSession(ctx, db.GetTurnCheckpointForSessionParams{
			ProjectID: in.ProjectID, SessionID: in.SessionID, Turn: int64(in.Turn),
		})
		if lookupErr == nil {
			return checkpointFromColumns(row.ID, row.ProjectID, row.Kind, row.Label, row.ParentID,
				row.SessionID, row.Turn, row.CreatedOrdinal, row.CreatedTs), nil
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			return Checkpoint{}, lookupErr
		}
	}
	ordinal, err := q.LatestSourceOrdinal(ctx, in.ProjectID)
	if err != nil {
		return Checkpoint{}, err
	}
	now := time.Now().UTC()
	parentID := ""
	parentOrdinal := int64(-1)
	parent, parentErr := q.LatestStructuralSourceCheckpoint(ctx, in.ProjectID)
	if parentErr == nil {
		parentID = parent.ID
		parentOrdinal = parent.CreatedOrdinal
	} else if !errors.Is(parentErr, sql.ErrNoRows) {
		return Checkpoint{}, parentErr
	}
	out := Checkpoint{
		ID: newID(), ProjectID: in.ProjectID, Kind: in.Kind, Label: in.Label,
		ParentID: parentID, SessionID: in.SessionID, Turn: in.Turn,
		CreatedOrdinal: ordinal, CreatedTS: now,
	}
	if err := q.InsertSourceCheckpoint(ctx, db.InsertSourceCheckpointParams{
		ID: out.ID, ProjectID: out.ProjectID, Kind: out.Kind, Label: out.Label,
		ParentID: out.ParentID, SessionID: out.SessionID, Turn: int64(out.Turn),
		CreatedOrdinal: out.CreatedOrdinal, CreatedTs: now.Format(checkpointTimeLayout),
	}); err != nil {
		return Checkpoint{}, err
	}
	heads, err := q.ListTrunkSourceHeadsAfterOrdinal(ctx, db.ListTrunkSourceHeadsAfterOrdinalParams{
		ProjectID: in.ProjectID, Ordinal: parentOrdinal,
	})
	if err != nil {
		return Checkpoint{}, err
	}
	for _, head := range heads {
		if err := q.InsertSourceCheckpointEntry(ctx, db.InsertSourceCheckpointEntryParams{
			CheckpointID: out.ID, FileID: head.FileID,
			VersionID: head.VersionID, Ordinal: head.Ordinal,
		}); err != nil {
			return Checkpoint{}, err
		}
	}
	// The boundary carries the git position as last observed; no repository
	// read runs inside the transaction.
	gitHeads, err := q.ListSourceGitHeads(ctx, in.ProjectID)
	if err != nil {
		return Checkpoint{}, err
	}
	for _, head := range gitHeads {
		if err := q.InsertSourceCheckpointGitState(ctx, db.InsertSourceCheckpointGitStateParams{
			CheckpointID: out.ID, RootID: head.RootID, RepoState: head.RepoState,
			HeadCommit: head.HeadCommit, HeadRef: head.HeadRef,
		}); err != nil {
			return Checkpoint{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Checkpoint{}, err
	}
	return out, nil
}

// ListPinsPage returns one page of named project boundaries.
func (s *Checkpoints) ListPinsPage(ctx context.Context, projectID string, query PinPageQuery) (PinPage, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = DefaultPinPageLimit
	}
	if limit > MaxPinPageLimit {
		limit = MaxPinPageLimit
	}
	rows, err := s.queries.ListNamedSourceCheckpointPage(ctx, db.ListNamedSourceCheckpointPageParams{
		ProjectID: projectID, BeforeCreatedTs: query.BeforeCreatedTS,
		BeforeID: query.BeforeID, PageLimit: int64(limit + 1),
	})
	if err != nil {
		return PinPage{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := PinPage{Pins: make([]Pin, 0, len(rows))}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		out.Pins = append(out.Pins, pinFromCheckpoint(checkpointFromDB(row)))
		ids = append(ids, row.ID)
	}
	heads, err := s.checkpointGitHeads(ctx, ids)
	if err != nil {
		return PinPage{}, err
	}
	for i := range out.Pins {
		out.Pins[i].GitHeads = heads[out.Pins[i].ID]
	}
	if hasMore && len(out.Pins) != 0 {
		last := out.Pins[len(out.Pins)-1]
		out.NextCreatedTS = last.CreatedTS.UTC().Format(checkpointTimeLayout)
		out.NextID = last.ID
	}
	return out, nil
}

func pinFromCheckpoint(checkpoint Checkpoint) Pin {
	return Pin{
		ID: checkpoint.ID, ProjectID: checkpoint.ProjectID, Label: checkpoint.Label,
		CreatedOrdinal: checkpoint.CreatedOrdinal, CreatedTS: checkpoint.CreatedTS,
	}
}

// UpdatePinLabel changes a named boundary's label.
func (s *Checkpoints) UpdatePinLabel(ctx context.Context, projectID, id, label string) error {
	updated, err := s.queries.UpdateNamedSourceCheckpointLabel(ctx, db.UpdateNamedSourceCheckpointLabelParams{
		Label: label, ID: id, ProjectID: projectID,
	})
	if err != nil {
		return err
	}
	if updated == 0 {
		return ErrPinNotFound
	}
	return nil
}

// GetPin returns one named project boundary.
func (s *Checkpoints) GetPin(ctx context.Context, projectID, id string) (Pin, error) {
	row, err := s.queries.GetSourceCheckpoint(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Pin{}, ErrPinNotFound
		}
		return Pin{}, err
	}
	if row.ProjectID != projectID || row.Kind != CheckpointNamed {
		return Pin{}, ErrPinNotFound
	}
	pin := pinFromCheckpoint(checkpointFromDB(row))
	heads, err := s.checkpointGitHeads(ctx, []string{id})
	if err != nil {
		return Pin{}, err
	}
	pin.GitHeads = heads[id]
	return pin, nil
}

// DeletePin removes a named boundary.
func (s *Checkpoints) DeletePin(ctx context.Context, projectID, id string) error {
	deleted, err := s.queries.DeleteNamedSourceCheckpoint(ctx, db.DeleteNamedSourceCheckpointParams{ID: id, ProjectID: projectID})
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrPinNotFound
	}
	return nil
}

func checkpointFromDB(row db.SourceCheckpoints) Checkpoint {
	ts, _ := time.Parse(time.RFC3339Nano, row.CreatedTs)
	return Checkpoint{
		ID: row.ID, ProjectID: row.ProjectID, Kind: row.Kind, Label: row.Label,
		ParentID: row.ParentID, SessionID: row.SessionID, Turn: int(row.Turn),
		CreatedOrdinal: row.CreatedOrdinal, CreatedTS: ts,
	}
}

func validateCheckpointInput(in StructuralCheckpointInput) error {
	if in.ProjectID == "" {
		return fmt.Errorf("checkpoint project is required")
	}
	switch in.Kind {
	case CheckpointTracking:
		if in.SessionID == "" && in.Turn == 0 {
			return nil
		}
	case CheckpointSession:
		if in.SessionID != "" && in.Turn == 0 {
			return nil
		}
	case CheckpointTurn:
		if in.SessionID != "" && in.Turn > 0 {
			return nil
		}
	case CheckpointNamed:
		if in.SessionID == "" && in.Turn == 0 {
			return nil
		}
	}
	return fmt.Errorf("invalid %q checkpoint boundary", in.Kind)
}

// Checkpoints owns durable history boundaries and review presentation.
type Checkpoints struct {
	queries  *db.Queries
	recordMu *sync.Mutex
	sqlDB    db.Handle
}

// TurnCheckpoint resolves a user turn's boundary; found=false means the baseline is unknown.
func (s *Checkpoints) TurnCheckpoint(ctx context.Context, projectID, sessionID string, turn int) (Checkpoint, bool, error) {
	if s == nil {
		return Checkpoint{}, false, fmt.Errorf("ledger not configured")
	}
	row, err := s.queries.GetTurnCheckpointForSession(ctx, db.GetTurnCheckpointForSessionParams{
		ProjectID: projectID, SessionID: sessionID, Turn: int64(turn),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, err
	}
	return checkpointFromColumns(row.ID, row.ProjectID, row.Kind, row.Label, row.ParentID,
		row.SessionID, row.Turn, row.CreatedOrdinal, row.CreatedTs), true, nil
}

// Effects at or below the first-turn ordinal predate the session.
// found=false means the session has no recorded turn boundary.
