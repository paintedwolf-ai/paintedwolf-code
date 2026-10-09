package delegation

import (
	"context"
	"database/sql"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/worker"
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

// AddLeg inserts an additional leg for a delegation.
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

// dispatchLeg commits the pending-to-dispatched CAS; ErrLegNotPending means
// another dispatch already took the leg and no second worker may be enqueued.
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
