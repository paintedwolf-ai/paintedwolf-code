package workerrecords

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
)

// maxPrerequisiteWalk bounds the workers a cycle check visits.
const maxPrerequisiteWalk = 256

// Prerequisites names what a worker waits for: producer workers by name, or
// one exact result to start on.
type Prerequisites struct {
	AfterWorkers []string `json:"after_workers,omitempty"`
	BaseResultID string   `json:"base_result_id,omitempty"`
}

// PrerequisiteReceipt lists the recorded producers and base result.
type PrerequisiteReceipt struct {
	WorkerID     string   `json:"worker_id"`
	ProducerIDs  []string `json:"producer_ids,omitempty"`
	BaseResultID string   `json:"base_result_id,omitempty"`
}

// AddPrerequisites records a worker's prerequisites. Names resolve in the
// worker's session at write time; a missing name, the worker itself and a
// cycle are refused.
func (s *Store) AddPrerequisites(ctx context.Context, actor Actor, op Op, workerID string, pre Prerequisites) (PrerequisiteReceipt, error) {
	pre = normalizePrerequisites(pre)
	return run(ctx, s, actor, op, "add_prerequisites", []any{workerID, pre}, func(q *db.Queries, _ *sql.Tx) (PrerequisiteReceipt, error) {
		w, err := coordinatorWorker(ctx, q, actor, workerID)
		if err != nil {
			return PrerequisiteReceipt{}, err
		}
		receipt := PrerequisiteReceipt{WorkerID: w.ID, BaseResultID: pre.BaseResultID}
		now := s.stamp()
		for _, name := range pre.AfterWorkers {
			producer, err := q.GetWorkerByName(ctx, db.GetWorkerByNameParams{SessionID: w.SessionID, Name: name})
			if db.IsNoRows(err) {
				return PrerequisiteReceipt{}, ErrPrerequisiteUnknown
			}
			if err != nil {
				return PrerequisiteReceipt{}, err
			}
			if producer.ID == w.ID {
				return PrerequisiteReceipt{}, ErrPrerequisiteSelf
			}
			if err := checkNoCycle(ctx, q, w.ID, producer.ID); err != nil {
				return PrerequisiteReceipt{}, err
			}
			if err := q.InsertWorkerDependency(ctx, db.InsertWorkerDependencyParams{
				WorkerID: w.ID, ProducerWorkerID: nullString(producer.ID), OperationID: op.ID, CreatedAt: now,
			}); err != nil {
				if db.IsUniqueConstraint(err) {
					receipt.ProducerIDs = append(receipt.ProducerIDs, producer.ID)
					continue
				}
				return PrerequisiteReceipt{}, err
			}
			receipt.ProducerIDs = append(receipt.ProducerIDs, producer.ID)
		}
		if pre.BaseResultID != "" {
			owner, err := q.GetWorkerResultOwner(ctx, pre.BaseResultID)
			if db.IsNoRows(err) {
				return PrerequisiteReceipt{}, ErrResultNotFound
			}
			if err != nil {
				return PrerequisiteReceipt{}, err
			}
			base, err := q.GetWorker(ctx, owner.WorkerID)
			if err != nil {
				return PrerequisiteReceipt{}, err
			}
			if base.SessionID != w.SessionID {
				return PrerequisiteReceipt{}, ErrResultNotFound
			}
			if base.ID == w.ID {
				return PrerequisiteReceipt{}, ErrPrerequisiteSelf
			}
			if err := q.InsertWorkerDependency(ctx, db.InsertWorkerDependencyParams{
				WorkerID: w.ID, BaseResultID: nullString(pre.BaseResultID), OperationID: op.ID, CreatedAt: now,
			}); err != nil {
				return PrerequisiteReceipt{}, err
			}
		}
		return receipt, nil
	})
}

func normalizePrerequisites(pre Prerequisites) Prerequisites {
	seen := map[string]bool{}
	var names []string
	for _, name := range pre.AfterWorkers {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return Prerequisites{AfterWorkers: names, BaseResultID: strings.TrimSpace(pre.BaseResultID)}
}

// checkNoCycle refuses producer when it already waits, directly or through
// other workers, on consumer. The walk is bounded.
func checkNoCycle(ctx context.Context, q *db.Queries, consumer, producer string) error {
	seen := map[string]bool{producer: true}
	queue := []string{producer}
	for len(queue) > 0 {
		if len(seen) > maxPrerequisiteWalk {
			return ErrPrerequisiteDepth
		}
		next := queue[0]
		queue = queue[1:]
		upstream, err := q.ListWorkerProducerIDs(ctx, next)
		if err != nil {
			return err
		}
		for _, id := range upstream {
			if !id.Valid {
				continue
			}
			if id.String == consumer {
				return ErrPrerequisiteCycle
			}
			if !seen[id.String] {
				seen[id.String] = true
				queue = append(queue, id.String)
			}
		}
	}
	return nil
}

// Dependency is one recorded prerequisite.
type Dependency struct {
	ProducerWorkerID string `json:"producer_worker_id,omitempty"`
	BaseResultID     string `json:"base_result_id,omitempty"`
}

// Dependencies lists a worker's prerequisites.
func (s *Store) Dependencies(ctx context.Context, actor Actor, workerID string) ([]Dependency, error) {
	if err := actor.check(); err != nil {
		return nil, err
	}
	w, _, err := loadWorker(ctx, s.q, actor, workerID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListWorkerDependencies(ctx, w.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Dependency, 0, len(rows))
	for _, d := range rows {
		out = append(out, Dependency{ProducerWorkerID: d.ProducerWorkerID.String, BaseResultID: d.BaseResultID.String})
	}
	return out, nil
}
