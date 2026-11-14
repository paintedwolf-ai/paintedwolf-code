package workerrecords

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
)

// Round is one stored round.
type Round struct {
	WorkerID            string `json:"worker_id"`
	Ordinal             int64  `json:"ordinal"`
	JobID               string `json:"job_id"`
	InstructionFirstSeq int64  `json:"instruction_first_seq,omitempty"`
	InstructionLastSeq  int64  `json:"instruction_last_seq,omitempty"`
	ExecutionTarget     string `json:"execution_target"`
	ReviewSubject       string `json:"review_subject,omitempty"`
	CreatedAt           string `json:"created_at"`
	SettledAt           string `json:"settled_at,omitempty"`
	// CheckpointID is what the round published; a result handle resolves to it.
	CheckpointID string `json:"checkpoint_id,omitempty"`
	// ResultID is the round's immutable result once its job settled.
	ResultID string `json:"result_id,omitempty"`
}

func roundFromRow(r db.WorkerRounds) Round {
	return Round{
		WorkerID: r.WorkerID, Ordinal: r.Ordinal, JobID: r.JobID,
		InstructionFirstSeq: r.InstructionFirstSeq.Int64, InstructionLastSeq: r.InstructionLastSeq.Int64,
		ExecutionTarget: r.ExecutionTarget, ReviewSubject: r.ReviewSubjectJson,
		CreatedAt: r.CreatedAt, SettledAt: r.SettledAt.String, CheckpointID: r.CheckpointID,
	}
}

// RoundSpec admits a round on a job. The instruction range names the
// coordinator messages the round starts with.
type RoundSpec struct {
	JobID               string          `json:"job_id"`
	InstructionFirstSeq int64           `json:"instruction_first_seq,omitempty"`
	InstructionLastSeq  int64           `json:"instruction_last_seq,omitempty"`
	ExecutionTarget     string          `json:"execution_target"`
	ReviewSubject       json.RawMessage `json:"review_subject,omitempty"`
}

// RoundReceipt names an admitted round.
type RoundReceipt struct {
	WorkerID string `json:"worker_id"`
	Ordinal  int64  `json:"ordinal"`
	JobID    string `json:"job_id"`
}

// AdmitRound records a new round. A worker has at most one unsettled round,
// and its job must belong to the worker's sessions.
func (s *Store) AdmitRound(ctx context.Context, actor Actor, op Op, workerID string, spec RoundSpec) (RoundReceipt, error) {
	spec.JobID = strings.TrimSpace(spec.JobID)
	if spec.JobID == "" || (spec.ExecutionTarget != "local" && spec.ExecutionTarget != "runner") ||
		(spec.InstructionFirstSeq == 0) != (spec.InstructionLastSeq == 0) || spec.InstructionLastSeq < spec.InstructionFirstSeq ||
		(len(spec.ReviewSubject) > 0 && !json.Valid(spec.ReviewSubject)) {
		return RoundReceipt{}, ErrInvalidRound
	}
	return run(ctx, s, actor, op, "admit_round", []any{workerID, spec}, func(q *db.Queries, _ *sql.Tx) (RoundReceipt, error) {
		w, err := coordinatorWorker(ctx, q, actor, workerID)
		if err != nil {
			return RoundReceipt{}, err
		}
		if _, err := q.GetUnsettledWorkerRound(ctx, w.ID); err == nil {
			return RoundReceipt{}, ErrRoundUnsettled
		} else if !db.IsNoRows(err) {
			return RoundReceipt{}, err
		}
		job, err := q.GetWorkerJobBinding(ctx, spec.JobID)
		if db.IsNoRows(err) {
			return RoundReceipt{}, ErrJobBinding
		}
		if err != nil {
			return RoundReceipt{}, err
		}
		if job.ParentSessionID != w.SessionID || (job.ChildSessionID != "" && w.ChildSessionID.Valid && job.ChildSessionID != w.ChildSessionID.String) {
			return RoundReceipt{}, ErrJobBinding
		}
		if _, err := q.GetWorkerRoundByJob(ctx, spec.JobID); err == nil {
			return RoundReceipt{}, ErrJobBinding
		} else if !db.IsNoRows(err) {
			return RoundReceipt{}, err
		}
		if spec.InstructionLastSeq >= w.NextMessageSeq {
			return RoundReceipt{}, ErrInvalidRound
		}
		now := s.stamp()
		ordinal, err := q.AllocateWorkerRoundOrdinal(ctx, db.AllocateWorkerRoundOrdinalParams{UpdatedAt: now, ID: w.ID})
		if err != nil {
			return RoundReceipt{}, err
		}
		if err := q.InsertWorkerRound(ctx, db.InsertWorkerRoundParams{
			WorkerID: w.ID, Ordinal: ordinal, JobID: spec.JobID,
			InstructionFirstSeq: nullInt(spec.InstructionFirstSeq), InstructionLastSeq: nullInt(spec.InstructionLastSeq),
			ExecutionTarget: spec.ExecutionTarget, ReviewSubjectJson: string(spec.ReviewSubject),
			OperationID: op.ID, CreatedAt: now,
		}); err != nil {
			return RoundReceipt{}, err
		}
		return RoundReceipt{WorkerID: w.ID, Ordinal: ordinal, JobID: spec.JobID}, nil
	})
}

// SettleReceipt confirms a settled round.
type SettleReceipt struct {
	WorkerID     string `json:"worker_id"`
	Ordinal      int64  `json:"ordinal"`
	SettledAt    string `json:"settled_at"`
	CheckpointID string `json:"checkpoint_id,omitempty"`
}

// SettleRound ends the worker's unsettled round on job jobID, linking the
// checkpoint it published. Settling an already settled round returns its
// settlement.
func (s *Store) SettleRound(ctx context.Context, actor Actor, op Op, workerID, jobID, checkpointID string) (SettleReceipt, error) {
	jobID = strings.TrimSpace(jobID)
	checkpointID = strings.TrimSpace(checkpointID)
	return run(ctx, s, actor, op, "settle_round", []string{workerID, jobID, checkpointID}, func(q *db.Queries, _ *sql.Tx) (SettleReceipt, error) {
		w, sd, err := loadWorker(ctx, q, actor, workerID)
		if err != nil {
			return SettleReceipt{}, err
		}
		if sd != sideCoordinator {
			return SettleReceipt{}, ErrCoordinatorOnly
		}
		r, err := q.GetWorkerRoundByJob(ctx, jobID)
		if db.IsNoRows(err) || (err == nil && r.WorkerID != w.ID) {
			return SettleReceipt{}, ErrRoundNotFound
		}
		if err != nil {
			return SettleReceipt{}, err
		}
		if r.SettledAt.Valid {
			return SettleReceipt{WorkerID: w.ID, Ordinal: r.Ordinal, SettledAt: r.SettledAt.String, CheckpointID: r.CheckpointID}, nil
		}
		now := s.stamp()
		if _, err := q.SettleWorkerRound(ctx, db.SettleWorkerRoundParams{SettledAt: nullString(now), CheckpointID: checkpointID, WorkerID: w.ID, Ordinal: r.Ordinal}); err != nil {
			return SettleReceipt{}, err
		}
		return SettleReceipt{WorkerID: w.ID, Ordinal: r.Ordinal, SettledAt: now, CheckpointID: checkpointID}, nil
	})
}

// Rounds lists a worker's rounds in order.
func (s *Store) Rounds(ctx context.Context, actor Actor, workerID string) ([]Round, error) {
	if err := actor.check(); err != nil {
		return nil, err
	}
	w, _, err := loadWorker(ctx, s.q, actor, workerID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListWorkerRounds(ctx, w.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Round, 0, len(rows))
	for _, r := range rows {
		round := roundFromRow(db.WorkerRounds{
			WorkerID: r.WorkerID, Ordinal: r.Ordinal, JobID: r.JobID, InstructionFirstSeq: r.InstructionFirstSeq,
			InstructionLastSeq: r.InstructionLastSeq, ExecutionTarget: r.ExecutionTarget, ReviewSubjectJson: r.ReviewSubjectJson,
			OperationID: r.OperationID, CreatedAt: r.CreatedAt, SettledAt: r.SettledAt, CheckpointID: r.CheckpointID,
		})
		round.ResultID = r.ResultID
		out = append(out, round)
	}
	return out, nil
}

// RoundStart starts a stopped worker's next round on an enqueued job with the
// instruction that starts it. A non-empty Brief replaces the standing brief;
// a non-empty AgentType changes the worker's agent and scope mode, and the
// child session's agent with them, so every reader of either sees the new
// agent from the round's first call.
type RoundStart struct {
	JobID           string          `json:"job_id"`
	ExecutionTarget string          `json:"execution_target"`
	ReviewSubject   json.RawMessage `json:"review_subject,omitempty"`
	Instruction     string          `json:"instruction"`
	Brief           string          `json:"brief,omitempty"`
	AgentType       string          `json:"agent_type,omitempty"`
	ScopeMode       string          `json:"scope_mode,omitempty"`
}

// RoundStartReceipt names the round and its starting instruction.
type RoundStartReceipt struct {
	RoundReceipt
	InstructionSeq int64 `json:"instruction_seq"`
}

// StartRound admits the round, records its starting instruction as the
// round's instruction range, and applies any brief or agent change, in one
// transaction.
func (s *Store) StartRound(ctx context.Context, actor Actor, op Op, workerID string, start RoundStart) (RoundStartReceipt, error) {
	start.JobID = strings.TrimSpace(start.JobID)
	start.Instruction = strings.TrimSpace(start.Instruction)
	start.Brief = strings.TrimSpace(start.Brief)
	start.AgentType = strings.TrimSpace(start.AgentType)
	if start.JobID == "" || start.Instruction == "" || (start.ExecutionTarget != "local" && start.ExecutionTarget != "runner") ||
		(len(start.ReviewSubject) > 0 && !json.Valid(start.ReviewSubject)) {
		return RoundStartReceipt{}, ErrInvalidRound
	}
	if (start.AgentType == "") != (start.ScopeMode == "") || (start.ScopeMode != "" && start.ScopeMode != "read" && start.ScopeMode != "write") {
		return RoundStartReceipt{}, ErrInvalidProfile
	}
	return run(ctx, s, actor, op, "start_round", []any{workerID, start}, func(q *db.Queries, _ *sql.Tx) (RoundStartReceipt, error) {
		w, err := coordinatorWorker(ctx, q, actor, workerID)
		if err != nil {
			return RoundStartReceipt{}, err
		}
		if _, err := q.GetUnsettledWorkerRound(ctx, w.ID); err == nil {
			return RoundStartReceipt{}, ErrRoundUnsettled
		} else if !db.IsNoRows(err) {
			return RoundStartReceipt{}, err
		}
		job, err := q.GetWorkerJobBinding(ctx, start.JobID)
		if db.IsNoRows(err) {
			return RoundStartReceipt{}, ErrJobBinding
		}
		if err != nil {
			return RoundStartReceipt{}, err
		}
		if job.ParentSessionID != w.SessionID || !w.ChildSessionID.Valid || (job.ChildSessionID != "" && job.ChildSessionID != w.ChildSessionID.String) {
			return RoundStartReceipt{}, ErrJobBinding
		}
		if _, err := q.GetWorkerRoundByJob(ctx, start.JobID); err == nil {
			return RoundStartReceipt{}, ErrJobBinding
		} else if !db.IsNoRows(err) {
			return RoundStartReceipt{}, err
		}
		now := s.stamp()
		if start.Brief != "" || start.AgentType != "" {
			agent, mode, brief := w.AgentType, w.ScopeMode, w.Brief
			if start.AgentType != "" {
				agent, mode = start.AgentType, start.ScopeMode
			}
			if start.Brief != "" {
				brief = start.Brief
			}
			if _, err := q.UpdateWorkerProfile(ctx, db.UpdateWorkerProfileParams{AgentType: agent, ScopeMode: mode, Brief: brief, UpdatedAt: now, ID: w.ID}); err != nil {
				return RoundStartReceipt{}, err
			}
			if start.AgentType != "" && start.AgentType != w.AgentType {
				n, err := q.UpdateWorkerChildSessionAgent(ctx, db.UpdateWorkerChildSessionAgentParams{
					AgentType: nullString(agent), UpdatedAt: now, ID: w.ChildSessionID.String, ParentSessionID: nullString(w.SessionID),
				})
				if err != nil {
					return RoundStartReceipt{}, err
				}
				if n != 1 {
					return RoundStartReceipt{}, ErrJobBinding
				}
			}
		}
		seq, err := q.AllocateWorkerMessageSeq(ctx, db.AllocateWorkerMessageSeqParams{UpdatedAt: now, ID: w.ID})
		if err != nil {
			return RoundStartReceipt{}, err
		}
		ordinal, err := q.AllocateWorkerRoundOrdinal(ctx, db.AllocateWorkerRoundOrdinalParams{UpdatedAt: now, ID: w.ID})
		if err != nil {
			return RoundStartReceipt{}, err
		}
		if err := q.InsertWorkerRound(ctx, db.InsertWorkerRoundParams{
			WorkerID: w.ID, Ordinal: ordinal, JobID: start.JobID,
			InstructionFirstSeq: nullInt(seq), InstructionLastSeq: nullInt(seq),
			ExecutionTarget: start.ExecutionTarget, ReviewSubjectJson: string(start.ReviewSubject),
			OperationID: op.ID, CreatedAt: now,
		}); err != nil {
			return RoundStartReceipt{}, err
		}
		if err := q.InsertWorkerMessage(ctx, db.InsertWorkerMessageParams{
			ID: uuid.Must(uuid.NewV7()).String(), WorkerID: w.ID, Seq: seq, SenderRole: string(actor.Role), SenderID: actor.PrincipalID,
			RecipientRole: string(RoleWorker), RoundOrdinal: sql.NullInt64{Int64: ordinal, Valid: true}, Body: start.Instruction,
			OperationID: op.ID, CreatedAt: now,
		}); err != nil {
			return RoundStartReceipt{}, err
		}
		return RoundStartReceipt{RoundReceipt: RoundReceipt{WorkerID: w.ID, Ordinal: ordinal, JobID: start.JobID}, InstructionSeq: seq}, nil
	})
}

// RoundByJob finds the round a job runs, for the host paths that run and
// settle jobs. ok is false for a job that runs no worker round.
func (s *Store) RoundByJob(ctx context.Context, jobID string) (Round, bool, error) {
	r, err := s.q.GetWorkerRoundByJob(ctx, strings.TrimSpace(jobID))
	if db.IsNoRows(err) {
		return Round{}, false, nil
	}
	if err != nil {
		return Round{}, false, err
	}
	return roundFromRow(r), true, nil
}
