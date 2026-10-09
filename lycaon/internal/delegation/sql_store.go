package delegation

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// SQLStore persists delegations and legs in SQLite.
type SQLStore struct {
	db      db.Handle
	queries *db.Queries
	outbox  delegationEventOutbox
}

// NewSQLStore creates a delegation store backed by the database.
func NewSQLStore(database db.Handle) *SQLStore {
	return &SQLStore{db: database, queries: db.New(database)}
}

// Create commits one complete delegation graph.
func (s *SQLStore) Create(ctx context.Context, delegation api.Delegation, sessionID string, legs []api.Leg) (*api.Delegation, error) {
	return s.create(ctx, delegation, sessionID, legs, CreateReceipt{})
}

// CreateOnce commits the delegation and its receipt in one transaction, or
// answers the delegation the receipt's operation already created.
func (s *SQLStore) CreateOnce(ctx context.Context, delegation api.Delegation, sessionID string, legs []api.Leg, receipt CreateReceipt) (*api.Delegation, error) {
	return s.create(ctx, delegation, sessionID, legs, receipt)
}

// DelegationByOperation answers the delegation an operation created.
func (s *SQLStore) DelegationByOperation(ctx context.Context, receipt CreateReceipt) (*api.Delegation, bool, error) {
	id, found, err := operationDelegation(ctx, s.queries, receipt)
	if err != nil || !found {
		return nil, false, err
	}
	out, err := s.Get(ctx, id)
	return out, err == nil, err
}

func operationDelegation(ctx context.Context, queries *db.Queries, receipt CreateReceipt) (string, bool, error) {
	row, err := queries.GetDelegationOperation(ctx, db.NullString(receipt.OperationID))
	if db.IsNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if row.InputDigest.String != receipt.InputDigest {
		return "", false, ErrOperationConflict
	}
	return row.ID, true, nil
}

func (s *SQLStore) create(ctx context.Context, delegation api.Delegation, sessionID string, legs []api.Leg, receipt CreateReceipt) (*api.Delegation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(legs) == 0 {
		return nil, ErrLegNotFound
	}
	if delegation.ID == "" {
		delegation.ID = uuid.NewString()
	}
	if delegation.CreatedAt.IsZero() {
		delegation.CreatedAt = time.Now().UTC()
	}
	if delegation.Status == "" {
		delegation.Status = api.DelegationStatusActive
	}
	if delegation.Phase == "" {
		delegation.Phase = api.DelegationPhaseSetup
	}
	for i := range legs {
		if legs[i].ID == "" {
			legs[i].ID = uuid.NewString()
		}
		legs[i].DelegationID = delegation.ID
		if legs[i].CreatedAt.IsZero() {
			legs[i].CreatedAt = time.Now().UTC()
		}
		if legs[i].Status == "" {
			legs[i].Status = api.LegStatusPending
		}
	}
	if err := ValidateLegDependencies(legs); err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)

	if receipt.OperationID != "" {
		priorID, found, err := operationDelegation(ctx, qtx, receipt)
		if err != nil {
			return nil, err
		}
		if found {
			_ = tx.Rollback()
			return s.Get(ctx, priorID)
		}
	}
	if err := qtx.InsertDelegation(ctx, db.InsertDelegationParams{
		ID:                   delegation.ID,
		ProjectID:            delegation.ProjectID,
		WorkspaceRootID:      db.NullString(delegation.WorkspaceRootID),
		WorkspacePath:        delegation.WorkspacePath,
		CoordinatorSessionID: db.NullString(sessionID),
		Task:                 delegation.Task,
		Strategy:             string(delegation.Strategy),
		InspectMode:          db.NullString(string(delegation.InspectMode)),
		Status:               string(delegation.Status),
		Reason:               db.NullString(delegation.Reason),
		Phase:                db.NullString(string(delegation.Phase)),
		WorkflowID:           db.NullString(delegation.WorkflowID),
		WorkflowVersion:      db.NullString(delegation.WorkflowVersion),
		WorkflowRunID:        db.NullString(delegation.WorkflowRunID),
		BlueprintPath:        db.NullString(delegation.BlueprintPath),
		BaseHeadSha:          db.NullString(delegation.BaseHeadSHA),
		OperationID:          db.NullString(receipt.OperationID),
		InputDigest:          db.NullString(receipt.InputDigest),
		CreatedAt:            db.FormatTime(delegation.CreatedAt),
	}); err != nil {
		return nil, err
	}
	for i := range legs {
		if err := insertLeg(ctx, qtx, legs[i]); err != nil {
			return nil, err
		}
	}
	if err := s.emitDelegationTx(ctx, tx, delegation.ID, legs[0].ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.notify()
	delegation.CoordinatorSessionID = sessionID
	delegation.Legs = append([]api.Leg(nil), legs...)
	return &delegation, nil
}

// Get returns a delegation with legs loaded.
func (s *SQLStore) Get(ctx context.Context, delegationID string) (*api.Delegation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	row, err := s.queries.GetDelegation(ctx, delegationID)
	if db.IsNoRows(err) {
		return nil, ErrDelegationNotFound
	}
	if err != nil {
		return nil, err
	}
	delegation, err := delegationFromRow(row)
	if err != nil {
		return nil, err
	}
	legs, err := s.ListLegs(ctx, delegationID)
	if err != nil {
		return nil, err
	}
	delegation.Legs = legs
	return delegation, nil
}

// Abort atomically settles every active leg and the delegation.
func (s *SQLStore) Abort(ctx context.Context, delegationID string, completedAt time.Time, reason string) error {
	return s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		row, err := qtx.GetDelegation(ctx, delegationID)
		if db.IsNoRows(err) {
			return ErrDelegationNotFound
		}
		if err != nil {
			return err
		}
		if api.DelegationStatus(row.Status) != api.DelegationStatusActive {
			return nil
		}

		if err := qtx.AbortActiveDelegationLegs(ctx, db.AbortActiveDelegationLegsParams{
			CompletedAt:  db.NullTimePtr(&completedAt),
			DelegationID: delegationID,
		}); err != nil {
			return err
		}
		n, err := qtx.AbortDelegation(ctx, db.AbortDelegationParams{
			ID:     delegationID,
			Reason: db.NullString(reason),
		})
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrDelegationNotFound
		}
		return s.emitDelegationTx(ctx, tx, delegationID, "")
	})
}

// ListByProject returns delegations for a project id.
func (s *SQLStore) ListByProject(ctx context.Context, projectID, sessionID string) ([]api.Delegation, error) {
	var rows []db.Delegations
	var err error
	if sessionID == "" {
		rows, err = s.queries.ListDelegationsByProject(ctx, projectID)
	} else {
		rows, err = s.queries.ListDelegationsBySession(ctx, db.ListDelegationsBySessionParams{ProjectID: projectID, SessionID: db.NullString(sessionID)})
	}
	if err != nil {
		return nil, err
	}
	out := make([]api.Delegation, 0, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		delegation, err := delegationFromRow(row)
		if err != nil {
			return nil, err
		}
		ids = append(ids, delegation.ID)
		out = append(out, *delegation)
	}
	if len(ids) == 0 {
		return out, nil
	}
	raw, err := db.MarshalJSON(ids)
	if err != nil {
		return nil, err
	}
	legs, err := s.queries.ListDelegationLegsForDelegations(ctx, raw.String)
	if err != nil {
		return nil, err
	}
	byDelegation := make(map[string][]api.Leg, len(rows))
	for _, row := range legs {
		leg, err := legFromRow(row)
		if err != nil {
			return nil, err
		}
		byDelegation[row.DelegationID] = append(byDelegation[row.DelegationID], *leg)
	}
	for i := range out {
		out[i].Legs = byDelegation[out[i].ID]
	}
	return out, nil
}

// CancelActiveByWorkflowRunID marks active delegations canceled for a workflow run.
func (s *SQLStore) CancelActiveByWorkflowRunID(ctx context.Context, runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	return s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		// Read the ids before the update: afterwards nothing distinguishes a
		// delegation this call canceled from one already canceled.
		ids, err := qtx.ListActiveDelegationIDsByWorkflowRun(ctx, db.NullString(runID))
		if err != nil {
			return err
		}
		if err := qtx.CancelActiveDelegationsByWorkflowRun(ctx, db.NullString(runID)); err != nil {
			return err
		}
		completedAt := time.Now().UTC()
		for _, id := range ids {
			if err := qtx.AbortActiveDelegationLegs(ctx, db.AbortActiveDelegationLegsParams{
				DelegationID: id, CompletedAt: db.NullTimePtr(&completedAt),
			}); err != nil {
				return err
			}
			if err := s.emitDelegationTx(ctx, tx, id, ""); err != nil {
				return err
			}
		}
		return nil
	})
}

// DelegationBySessionID maps coordinator session to delegation id.
func (s *SQLStore) DelegationBySessionID(sessionID string) (string, bool) {
	id, err := s.queries.GetDelegationIDByCoordinatorSession(context.Background(), db.NullString(sessionID))
	if err != nil {
		return "", false
	}
	return id, true
}

// DelegationByWorkflowRunID returns the delegation bound to one workflow run.
func (s *SQLStore) DelegationByWorkflowRunID(ctx context.Context, workflowRunID string) (string, bool, error) {
	id, err := s.queries.GetDelegationIDByWorkflowRun(ctx, db.NullString(strings.TrimSpace(workflowRunID)))
	if db.IsNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// SessionID returns coordinator session for a delegation.
func (s *SQLStore) SessionID(delegationID string) (string, bool) {
	sid, err := s.queries.GetDelegationCoordinatorSession(context.Background(), delegationID)
	if err != nil || !sid.Valid {
		return "", false
	}
	return sid.String, true
}

// delegationFromRow maps a generated delegations row onto the wire type.
func delegationFromRow(r db.Delegations) (*api.Delegation, error) {
	delegation := api.Delegation{
		ID:                   r.ID,
		ProjectID:            r.ProjectID,
		WorkspaceRootID:      db.StringFromNull(r.WorkspaceRootID),
		WorkspacePath:        r.WorkspacePath,
		CoordinatorSessionID: db.StringFromNull(r.CoordinatorSessionID),
		Task:                 r.Task,
		Strategy:             api.HuntStrategy(r.Strategy),
		Status:               api.DelegationStatus(r.Status),
		Reason:               db.StringFromNull(r.Reason),
		WorkflowID:           db.StringFromNull(r.WorkflowID),
		WorkflowVersion:      db.StringFromNull(r.WorkflowVersion),
		WorkflowRunID:        db.StringFromNull(r.WorkflowRunID),
		BlueprintPath:        db.StringFromNull(r.BlueprintPath),
		BaseHeadSHA:          db.StringFromNull(r.BaseHeadSha),
	}
	if r.InspectMode.Valid {
		delegation.InspectMode = api.InspectMode(r.InspectMode.String)
	}
	if r.Phase.Valid {
		delegation.Phase = api.DelegationPhase(r.Phase.String)
	}
	var err error
	delegation.CreatedAt, err = db.ParseTime(r.CreatedAt)
	return &delegation, err
}
