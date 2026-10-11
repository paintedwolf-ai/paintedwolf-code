package delegation

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// DispatchLegWithJob commits the leg, job, and delegation phase together.
func (s *SQLStore) DispatchLegWithJob(ctx context.Context, leg api.Leg, delegation api.Delegation, task api.WorkerTask) error {
	return s.dispatchLegWithJob(ctx, leg, delegation, task, false)
}

// RedispatchLegWithJob commits a bounded continuation and its replacement job.
func (s *SQLStore) RedispatchLegWithJob(ctx context.Context, leg api.Leg, delegation api.Delegation, task api.WorkerTask) error {
	return s.dispatchLegWithJob(ctx, leg, delegation, task, true)
}

func (s *SQLStore) dispatchLegWithJob(ctx context.Context, leg api.Leg, delegation api.Delegation, task api.WorkerTask, retry bool) error {
	if leg.WorkerID == "" || task.ID == "" || leg.WorkerID != task.ID {
		return ErrLegNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := db.New(tx)
	var dispatchErr error
	if retry {
		dispatchErr = redispatchLeg(ctx, qtx, leg)
	} else {
		dispatchErr = dispatchLeg(ctx, qtx, leg)
	}
	if dispatchErr != nil {
		return dispatchErr
	}
	if err := worker.InsertTaskTx(ctx, tx, task); err != nil {
		return err
	}
	// Commit the leg and worker atomically.
	if err := jobstate.EnqueueJobEventTx(ctx, tx, s.outbox, task.ID); err != nil {
		return err
	}
	n, err := qtx.UpdateDelegation(ctx, db.UpdateDelegationParams{
		Task: delegation.Task, Status: string(delegation.Status), Phase: db.NullString(string(delegation.Phase)), ID: delegation.ID,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		if _, err := qtx.GetDelegation(ctx, delegation.ID); err != nil {
			if db.IsNoRows(err) {
				return ErrDelegationNotFound
			}
			return err
		}
		return ErrLegNotPending
	}
	if err := s.emitDelegationTx(ctx, tx, delegation.ID, leg.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notify()
	return nil
}

func redispatchLeg(ctx context.Context, queries *db.Queries, leg api.Leg) error {
	resultJSON, err := db.MarshalJSON(leg.Result)
	if err != nil {
		return err
	}
	filesJSON, err := db.MarshalJSON(leg.Files)
	if err != nil {
		return err
	}
	criteriaJSON, err := db.MarshalJSON(leg.CompletionCriteria)
	if err != nil {
		return err
	}
	dependsOnJSON, err := db.MarshalJSON(leg.DependsOn)
	if err != nil {
		return err
	}
	n, err := queries.RedispatchDelegationLeg(ctx, db.RedispatchDelegationLegParams{
		Title: leg.Title, DependsOnJson: dependsOnJSON, FilesJson: filesJSON,
		CompletionCriteriaJson: criteriaJSON, WorkspaceRoot: db.NullString(leg.WorkspaceRoot),
		WorkspaceID: db.NullString(leg.WorkspaceID), Prompt: db.NullString(leg.Prompt),
		AgentType: db.NullString(leg.AgentType), WorkerID: db.NullString(leg.WorkerID),
		ResultJson: resultJSON, StartedAt: db.NullTimePtr(leg.StartedAt),
		CompletedAt: db.NullTimePtr(leg.CompletedAt), ID: leg.ID, DelegationID: leg.DelegationID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLegNotPending
	}
	return nil
}

// AddLeg adds a leg to an active delegation.
func (s *SQLStore) AddLeg(ctx context.Context, delegationID string, leg api.Leg) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if leg.ID == "" {
		leg.ID = uuid.NewString()
	}
	leg.DelegationID = delegationID
	if leg.CreatedAt.IsZero() {
		leg.CreatedAt = time.Now().UTC()
	}
	if leg.Status == "" {
		leg.Status = api.LegStatusPending
	}
	existing, err := s.ListLegs(ctx, delegationID)
	if err != nil {
		return err
	}
	if err := ValidateLegDependencies(append(existing, leg)); err != nil {
		return err
	}
	return s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		row, err := qtx.GetDelegation(ctx, delegationID)
		if db.IsNoRows(err) {
			return ErrDelegationNotFound
		}
		if err != nil {
			return err
		}
		if api.DelegationStatus(row.Status) != api.DelegationStatusActive {
			return ErrDelegationSettled
		}

		if err := insertLeg(ctx, qtx, leg); err != nil {
			return err
		}
		return s.emitDelegationTx(ctx, tx, delegationID, leg.ID)
	})
}

// GetLeg returns a leg.
func (s *SQLStore) GetLeg(ctx context.Context, delegationID, legID string) (*api.Leg, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	row, err := s.queries.GetDelegationLeg(ctx, db.GetDelegationLegParams{
		DelegationID: delegationID,
		ID:           legID,
	})
	if db.IsNoRows(err) {
		return nil, ErrLegNotFound
	}
	if err != nil {
		return nil, err
	}
	return legFromRow(row)
}

// UpdateLeg updates a leg row.
func (s *SQLStore) UpdateLeg(ctx context.Context, leg api.Leg) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	existing, err := s.ListLegs(ctx, leg.DelegationID)
	if err != nil {
		return err
	}
	for i := range existing {
		if existing[i].ID == leg.ID {
			existing[i] = leg
			break
		}
	}
	if err := ValidateLegDependencies(existing); err != nil {
		return err
	}
	return s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		current, err := qtx.GetDelegationLeg(ctx, db.GetDelegationLegParams{DelegationID: leg.DelegationID, ID: leg.ID})
		if db.IsNoRows(err) {
			return ErrLegNotFound
		}
		if err != nil {
			return err
		}
		delegation, err := qtx.GetDelegation(ctx, leg.DelegationID)
		if err != nil {
			return err
		}
		if api.DelegationStatus(delegation.Status) != api.DelegationStatusActive || api.LegStatus(current.Status).IsTerminal() || db.StringFromNull(current.WorkerID) != leg.WorkerID {
			return nil
		}

		if err := updateLeg(ctx, qtx, leg); err != nil {
			return err
		}
		return s.emitDelegationTx(ctx, tx, leg.DelegationID, leg.ID)
	})
}

func dispatchLeg(ctx context.Context, queries *db.Queries, leg api.Leg) error {
	resultJSON, err := db.MarshalJSON(leg.Result)
	if err != nil {
		return err
	}
	filesJSON, err := db.MarshalJSON(leg.Files)
	if err != nil {
		return err
	}
	criteriaJSON, err := db.MarshalJSON(leg.CompletionCriteria)
	if err != nil {
		return err
	}
	dependsOnJSON, err := db.MarshalJSON(leg.DependsOn)
	if err != nil {
		return err
	}
	n, err := queries.DispatchDelegationLeg(ctx, db.DispatchDelegationLegParams{
		Title:                  leg.Title,
		DependsOnJson:          dependsOnJSON,
		FilesJson:              filesJSON,
		CompletionCriteriaJson: criteriaJSON,
		WorkspaceRoot:          db.NullString(leg.WorkspaceRoot),
		WorkspaceID:            db.NullString(leg.WorkspaceID),
		Prompt:                 db.NullString(leg.Prompt),
		AgentType:              db.NullString(leg.AgentType),
		WorkerID:               db.NullString(leg.WorkerID),
		ResultJson:             resultJSON,
		StartedAt:              db.NullTimePtr(leg.StartedAt),
		CompletedAt:            db.NullTimePtr(leg.CompletedAt),
		ID:                     leg.ID,
		DelegationID:           leg.DelegationID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLegNotPending
	}
	return nil
}

func updateLeg(ctx context.Context, queries *db.Queries, leg api.Leg) error {
	resultJSON, err := db.MarshalJSON(leg.Result)
	if err != nil {
		return err
	}
	filesJSON, err := db.MarshalJSON(leg.Files)
	if err != nil {
		return err
	}
	criteriaJSON, err := db.MarshalJSON(leg.CompletionCriteria)
	if err != nil {
		return err
	}
	dependsOnJSON, err := db.MarshalJSON(leg.DependsOn)
	if err != nil {
		return err
	}
	n, err := queries.UpdateDelegationLeg(ctx, db.UpdateDelegationLegParams{
		Title:                  leg.Title,
		Status:                 string(leg.Status),
		DependsOnJson:          dependsOnJSON,
		FilesJson:              filesJSON,
		CompletionCriteriaJson: criteriaJSON,
		WorkspaceRoot:          db.NullString(leg.WorkspaceRoot),
		WorkspaceID:            db.NullString(leg.WorkspaceID),
		Prompt:                 db.NullString(leg.Prompt),
		AgentType:              db.NullString(leg.AgentType),
		WorkerID:               db.NullString(leg.WorkerID),
		ResultJson:             resultJSON,
		StartedAt:              db.NullTimePtr(leg.StartedAt),
		CompletedAt:            db.NullTimePtr(leg.CompletedAt),
		ID:                     leg.ID,
		DelegationID:           leg.DelegationID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLegNotFound
	}
	return nil
}

// ListLegs returns legs for a delegation.
func (s *SQLStore) ListLegs(ctx context.Context, delegationID string) ([]api.Leg, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.queries.ListDelegationLegs(ctx, delegationID)
	if err != nil {
		return nil, err
	}
	out := make([]api.Leg, 0, len(rows))
	for _, r := range rows {
		leg, err := legFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, *leg)
	}
	return out, nil
}

// legFromRow maps a generated legs row onto the wire type.
func legFromRow(r db.DelegationLegs) (*api.Leg, error) {
	leg := api.Leg{
		ID:           r.ID,
		DelegationID: r.DelegationID,
		Title:        r.Title,
		Status:       api.LegStatus(r.Status),
		ParentID:     db.StringFromNull(r.ParentID),
		Prompt:       db.StringFromNull(r.Prompt),
		AgentType:    db.StringFromNull(r.AgentType),
		WorkerID:     db.StringFromNull(r.WorkerID),
	}
	_ = db.UnmarshalJSON(r.FilesJson, &leg.Files)
	_ = db.UnmarshalJSON(r.DependsOnJson, &leg.DependsOn)
	_ = db.UnmarshalJSON(r.CompletionCriteriaJson, &leg.CompletionCriteria)
	leg.WorkspaceRoot = db.StringFromNull(r.WorkspaceRoot)
	leg.WorkspaceID = db.StringFromNull(r.WorkspaceID)
	_ = db.UnmarshalJSON(r.ResultJson, &leg.Result)
	var err error
	leg.CreatedAt, err = db.ParseTime(r.CreatedAt)
	if err != nil {
		return nil, err
	}
	leg.StartedAt, err = db.TimePtrFromNull(r.StartedAt)
	if err != nil {
		return nil, err
	}
	leg.CompletedAt, err = db.TimePtrFromNull(r.CompletedAt)
	return &leg, err
}

func insertLeg(ctx context.Context, q *db.Queries, leg api.Leg) error {
	resultJSON, err := db.MarshalJSON(leg.Result)
	if err != nil {
		return err
	}
	filesJSON, err := db.MarshalJSON(leg.Files)
	if err != nil {
		return err
	}
	criteriaJSON, err := db.MarshalJSON(leg.CompletionCriteria)
	if err != nil {
		return err
	}
	dependsOnJSON, err := db.MarshalJSON(leg.DependsOn)
	if err != nil {
		return err
	}
	return q.InsertDelegationLeg(ctx, db.InsertDelegationLegParams{
		ID:                     leg.ID,
		DelegationID:           leg.DelegationID,
		Title:                  leg.Title,
		Status:                 string(leg.Status),
		ParentID:               db.NullString(leg.ParentID),
		DependsOnJson:          dependsOnJSON,
		FilesJson:              filesJSON,
		CompletionCriteriaJson: criteriaJSON,
		WorkspaceRoot:          db.NullString(leg.WorkspaceRoot),
		WorkspaceID:            db.NullString(leg.WorkspaceID),
		Prompt:                 db.NullString(leg.Prompt),
		AgentType:              db.NullString(leg.AgentType),
		WorkerID:               db.NullString(leg.WorkerID),
		ResultJson:             resultJSON,
		CreatedAt:              db.FormatTime(leg.CreatedAt),
		StartedAt:              db.NullTimePtr(leg.StartedAt),
		CompletedAt:            db.NullTimePtr(leg.CompletedAt),
	})
}
