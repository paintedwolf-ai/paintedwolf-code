package workerrecords

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/db"
)

// Scope modes a worker is created with.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
)

const maxNameLen = 64

// Worker is one stored worker.
type Worker struct {
	ID             string `json:"id"`
	ProjectID      string `json:"project_id"`
	SessionID      string `json:"session_id"`
	Name           string `json:"name"`
	AgentType      string `json:"agent_type"`
	ScopeMode      string `json:"scope_mode"`
	Brief          string `json:"brief"`
	ChildSessionID string `json:"child_session_id,omitempty"`
	DelegationID   string `json:"delegation_id,omitempty"`
	LegID          string `json:"leg_id,omitempty"`
	WorkflowRunID  string `json:"workflow_run_id,omitempty"`
	WorkflowPhase  string `json:"workflow_phase,omitempty"`
	WorkflowWorkID string `json:"workflow_work_id,omitempty"`
	ClosedAt       string `json:"closed_at,omitempty"`
	ClosedReason   string `json:"closed_reason,omitempty"`
	CreatedAt      string `json:"created_at"`
}

func workerFromRow(w db.Workers) Worker {
	return Worker{
		ID: w.ID, ProjectID: w.ProjectID, SessionID: w.SessionID, Name: w.Name, AgentType: w.AgentType,
		ScopeMode: w.ScopeMode, Brief: w.Brief, ChildSessionID: w.ChildSessionID.String,
		DelegationID: w.DelegationID.String, LegID: w.LegID.String, WorkflowRunID: w.WorkflowRunID.String,
		WorkflowPhase: w.WorkflowPhase, WorkflowWorkID: w.WorkflowWorkID, ClosedAt: w.ClosedAt.String,
		ClosedReason: w.ClosedReason, CreatedAt: w.CreatedAt,
	}
}

// WorkerSpec describes a new worker. An empty Name derives one from the agent
// and a per-session counter.
type WorkerSpec struct {
	Name           string `json:"name,omitempty"`
	AgentType      string `json:"agent_type"`
	ScopeMode      string `json:"scope_mode"`
	Brief          string `json:"brief"`
	ChildSessionID string `json:"child_session_id,omitempty"`
	DelegationID   string `json:"delegation_id,omitempty"`
	LegID          string `json:"leg_id,omitempty"`
	WorkflowRunID  string `json:"workflow_run_id,omitempty"`
	WorkflowPhase  string `json:"workflow_phase,omitempty"`
	WorkflowWorkID string `json:"workflow_work_id,omitempty"`
}

// WorkerReceipt names the worker an operation created.
type WorkerReceipt struct {
	WorkerID string `json:"worker_id"`
	Name     string `json:"name"`
}

// CreateWorker records a worker in the actor's session.
func (s *Store) CreateWorker(ctx context.Context, actor Actor, op Op, spec WorkerSpec) (WorkerReceipt, error) {
	spec = normalizeSpec(spec)
	if err := validateSpec(spec); err != nil {
		return WorkerReceipt{}, err
	}
	return run(ctx, s, actor, op, "create_worker", spec, func(q *db.Queries, _ *sql.Tx) (WorkerReceipt, error) {
		session, err := q.GetWorkerSessionBinding(ctx, actor.SessionID)
		if db.IsNoRows(err) {
			return WorkerReceipt{}, ErrInvalidActor
		}
		if err != nil {
			return WorkerReceipt{}, err
		}
		if err := checkCreateBindings(ctx, q, actor.SessionID, spec); err != nil {
			return WorkerReceipt{}, err
		}
		name := spec.Name
		if name == "" {
			if name, err = derivedName(ctx, q, actor.SessionID, spec.AgentType); err != nil {
				return WorkerReceipt{}, err
			}
		} else if _, err := q.GetWorkerByName(ctx, db.GetWorkerByNameParams{SessionID: actor.SessionID, Name: name}); err == nil {
			return WorkerReceipt{}, ErrNameTaken
		} else if !db.IsNoRows(err) {
			return WorkerReceipt{}, err
		}
		id := uuid.Must(uuid.NewV7()).String()
		now := s.stamp()
		var runID sql.NullString
		if spec.WorkflowRunID != "" {
			runID = nullString(spec.WorkflowRunID)
		}
		if err := q.InsertWorker(ctx, db.InsertWorkerParams{
			ID: id, ProjectID: session.ProjectID, SessionID: actor.SessionID, Name: name,
			AgentType: spec.AgentType, ScopeMode: spec.ScopeMode, Brief: spec.Brief,
			ChildSessionID: nullString(spec.ChildSessionID), DelegationID: nullString(spec.DelegationID),
			LegID: nullString(spec.LegID), WorkflowRunID: runID, WorkflowPhase: spec.WorkflowPhase,
			WorkflowWorkID: spec.WorkflowWorkID, OperationID: op.ID, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return WorkerReceipt{}, err
		}
		return WorkerReceipt{WorkerID: id, Name: name}, nil
	})
}

func normalizeSpec(spec WorkerSpec) WorkerSpec {
	spec.Name = strings.TrimSpace(spec.Name)
	spec.AgentType = strings.TrimSpace(spec.AgentType)
	spec.ScopeMode = strings.TrimSpace(spec.ScopeMode)
	spec.ChildSessionID = strings.TrimSpace(spec.ChildSessionID)
	spec.DelegationID = strings.TrimSpace(spec.DelegationID)
	spec.LegID = strings.TrimSpace(spec.LegID)
	spec.WorkflowRunID = strings.TrimSpace(spec.WorkflowRunID)
	spec.WorkflowPhase = strings.TrimSpace(spec.WorkflowPhase)
	spec.WorkflowWorkID = strings.TrimSpace(spec.WorkflowWorkID)
	return spec
}

func validateSpec(spec WorkerSpec) error {
	switch {
	case len(spec.Name) > maxNameLen:
		return ErrInvalidName
	case spec.AgentType == "":
		return ErrInvalidAgent
	case spec.ScopeMode != ScopeRead && spec.ScopeMode != ScopeWrite:
		return ErrInvalidScope
	case spec.LegID != "" && spec.DelegationID == "":
		return ErrBindingInvalid
	case spec.WorkflowRunID == "" && (spec.WorkflowPhase != "" || spec.WorkflowWorkID != ""):
		return ErrBindingInvalid
	}
	return nil
}

// checkCreateBindings confirms the child session and workflow run belong to
// the actor's session and that the workflow work is not bound already.
func checkCreateBindings(ctx context.Context, q *db.Queries, sessionID string, spec WorkerSpec) error {
	if spec.ChildSessionID != "" {
		if err := checkChildSession(ctx, q, sessionID, spec.ChildSessionID); err != nil {
			return err
		}
	}
	if spec.WorkflowRunID == "" {
		return nil
	}
	owner, err := q.GetWorkflowRunSessionID(ctx, spec.WorkflowRunID)
	if db.IsNoRows(err) || (err == nil && owner != sessionID) {
		return ErrBindingInvalid
	}
	if err != nil {
		return err
	}
	if spec.WorkflowWorkID == "" {
		return nil
	}
	workers, err := q.ListSessionWorkers(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, w := range workers {
		if w.WorkflowRunID.String == spec.WorkflowRunID && w.WorkflowPhase == spec.WorkflowPhase && w.WorkflowWorkID == spec.WorkflowWorkID {
			return ErrBindingTaken
		}
	}
	return nil
}

func checkChildSession(ctx context.Context, q *db.Queries, sessionID, childSessionID string) error {
	child, err := q.GetWorkerSessionBinding(ctx, childSessionID)
	if db.IsNoRows(err) || (err == nil && child.ParentSessionID != sessionID) {
		return ErrBindingInvalid
	}
	return err
}

// derivedName is the agent followed by the first free counter in the session.
func derivedName(ctx context.Context, q *db.Queries, sessionID, agent string) (string, error) {
	count, err := q.CountSessionWorkersByAgent(ctx, db.CountSessionWorkersByAgentParams{SessionID: sessionID, AgentType: agent})
	if err != nil {
		return "", err
	}
	for n := count + 1; ; n++ {
		suffix := "-" + strconv.FormatInt(n, 10)
		base := agent
		if len(base)+len(suffix) > maxNameLen {
			base = base[:maxNameLen-len(suffix)]
		}
		name := base + suffix
		_, err := q.GetWorkerByName(ctx, db.GetWorkerByNameParams{SessionID: sessionID, Name: name})
		if db.IsNoRows(err) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
	}
}

// Worker returns a worker the actor may see.
func (s *Store) Worker(ctx context.Context, actor Actor, workerID string) (Worker, error) {
	if err := actor.check(); err != nil {
		return Worker{}, err
	}
	w, _, err := loadWorker(ctx, s.q, actor, workerID)
	if err != nil {
		return Worker{}, err
	}
	return workerFromRow(w), nil
}

// Resolve finds a worker by name in the actor's own session.
func (s *Store) Resolve(ctx context.Context, actor Actor, name string) (Worker, error) {
	if err := actor.check(); err != nil {
		return Worker{}, err
	}
	w, err := s.q.GetWorkerByName(ctx, db.GetWorkerByNameParams{SessionID: actor.SessionID, Name: strings.TrimSpace(name)})
	if db.IsNoRows(err) {
		return Worker{}, ErrWorkerNotFound
	}
	if err != nil {
		return Worker{}, err
	}
	return workerFromRow(w), nil
}

// Workers lists the actor's session's workers in creation order.
func (s *Store) Workers(ctx context.Context, actor Actor) ([]Worker, error) {
	if err := actor.check(); err != nil {
		return nil, err
	}
	rows, err := s.q.ListSessionWorkers(ctx, actor.SessionID)
	if err != nil {
		return nil, err
	}
	out := make([]Worker, 0, len(rows))
	for _, w := range rows {
		out = append(out, workerFromRow(w))
	}
	return out, nil
}

// BindReceipt confirms a child session binding.
type BindReceipt struct {
	WorkerID       string `json:"worker_id"`
	ChildSessionID string `json:"child_session_id"`
}

// BindChildSession records the worker's child session once.
func (s *Store) BindChildSession(ctx context.Context, actor Actor, op Op, workerID, childSessionID string) (BindReceipt, error) {
	childSessionID = strings.TrimSpace(childSessionID)
	return run(ctx, s, actor, op, "bind_child_session", []string{workerID, childSessionID}, func(q *db.Queries, _ *sql.Tx) (BindReceipt, error) {
		w, err := coordinatorWorker(ctx, q, actor, workerID)
		if err != nil {
			return BindReceipt{}, err
		}
		if w.ChildSessionID.Valid {
			if w.ChildSessionID.String == childSessionID {
				return BindReceipt{WorkerID: w.ID, ChildSessionID: childSessionID}, nil
			}
			return BindReceipt{}, ErrBindingInvalid
		}
		if err := checkChildSession(ctx, q, w.SessionID, childSessionID); err != nil {
			return BindReceipt{}, err
		}
		if _, err := q.BindWorkerChildSession(ctx, db.BindWorkerChildSessionParams{
			ChildSessionID: nullString(childSessionID), UpdatedAt: s.stamp(), ID: w.ID,
		}); err != nil {
			return BindReceipt{}, err
		}
		return BindReceipt{WorkerID: w.ID, ChildSessionID: childSessionID}, nil
	})
}

// CloseReceipt confirms a closed worker.
type CloseReceipt struct {
	WorkerID string `json:"worker_id"`
	ClosedAt string `json:"closed_at"`
}

// Close ends a worker's use: a withdrawn plan leg, or a worker whose session
// can no longer be used. Its history stays readable.
func (s *Store) Close(ctx context.Context, actor Actor, op Op, workerID, reason string) (CloseReceipt, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return CloseReceipt{}, ErrReasonRequired
	}
	return run(ctx, s, actor, op, "close_worker", []string{workerID, reason}, func(q *db.Queries, _ *sql.Tx) (CloseReceipt, error) {
		w, err := coordinatorWorker(ctx, q, actor, workerID)
		if err != nil {
			return CloseReceipt{}, err
		}
		if _, err := q.GetUnsettledWorkerRound(ctx, w.ID); err == nil {
			return CloseReceipt{}, ErrRoundUnsettled
		} else if !db.IsNoRows(err) {
			return CloseReceipt{}, err
		}
		now := s.stamp()
		if _, err := q.CloseWorker(ctx, db.CloseWorkerParams{ClosedAt: nullString(now), ClosedReason: reason, ID: w.ID}); err != nil {
			return CloseReceipt{}, err
		}
		return CloseReceipt{WorkerID: w.ID, ClosedAt: now}, nil
	})
}

// Self returns the worker whose child session the actor runs in.
func (s *Store) Self(ctx context.Context, actor Actor) (Worker, error) {
	if err := actor.check(); err != nil {
		return Worker{}, err
	}
	w, err := s.q.GetWorkerByChildSession(ctx, nullString(actor.SessionID))
	if db.IsNoRows(err) {
		return Worker{}, ErrWorkerNotFound
	}
	if err != nil {
		return Worker{}, err
	}
	return workerFromRow(w), nil
}

// SetBrief replaces a worker's standing brief.
func (s *Store) SetBrief(ctx context.Context, actor Actor, op Op, workerID, brief string) (Worker, error) {
	brief = strings.TrimSpace(brief)
	if brief == "" {
		return Worker{}, ErrInvalidProfile
	}
	return run(ctx, s, actor, op, "set_brief", []string{workerID, brief}, func(q *db.Queries, _ *sql.Tx) (Worker, error) {
		w, err := coordinatorWorker(ctx, q, actor, workerID)
		if err != nil {
			return Worker{}, err
		}
		if _, err := q.UpdateWorkerProfile(ctx, db.UpdateWorkerProfileParams{AgentType: w.AgentType, ScopeMode: w.ScopeMode, Brief: brief, UpdatedAt: s.stamp(), ID: w.ID}); err != nil {
			return Worker{}, err
		}
		w.Brief = brief
		return workerFromRow(w), nil
	})
}
