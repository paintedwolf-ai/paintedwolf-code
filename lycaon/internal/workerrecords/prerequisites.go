package workerrecords

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
)

// maxPrerequisiteWalk bounds the workers a cycle check visits.
const maxPrerequisiteWalk = 256

// normalizeWorkerNames trims, deduplicates and sorts producer worker names.
func normalizeWorkerNames(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
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
