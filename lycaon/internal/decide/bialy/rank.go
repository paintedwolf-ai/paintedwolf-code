package bialy

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/decide"
)

// Rank preserves candidate order and decision priority across bounded batches.
func (c *Client) Rank(ctx context.Context, head decide.Head, task string, candidates []string) ([]float64, decide.Engine, error) {
	if len(candidates) == 0 {
		return nil, c.Engine(), nil
	}
	proc, err := c.ensure(ctx)
	if err != nil {
		return nil, decide.Engine{}, err
	}
	release, err := proc.prioritize(ctx)
	if err != nil {
		return nil, decide.Engine{}, err
	}
	defer release()
	engine := c.Engine()
	rows := 32
	if c.cfg.Resources != nil {
		rows = c.cfg.Resources.MaxRows
	}
	scores := make([]float64, 0, len(candidates))
	for start := 0; start < len(candidates); start += rows {
		batch := candidates[start:min(start+rows, len(candidates))]
		resp, err := c.exchange(ctx, proc, request{Method: methodRank, Head: head, Task: task, Candidates: batch})
		if err != nil {
			return nil, decide.Engine{}, err
		}
		if len(resp.Scores) != len(batch) {
			return nil, decide.Engine{}, fmt.Errorf("%w: %d scores for %d candidates", decide.ErrEngine, len(resp.Scores), len(batch))
		}
		scores = append(scores, resp.Scores...)
	}
	return scores, engine, nil
}
