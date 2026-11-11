package bialy

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
)

// Decide preserves whole questions and decision priority across bounded batches.
func (c *Client) Decide(ctx context.Context, head decide.Head, state any, questions map[string]decide.Question) (decide.Result, error) {
	if len(questions) == 0 {
		return decide.Result{Engine: c.Engine()}, nil
	}
	started := time.Now()
	proc, err := c.ensure(ctx)
	if err != nil {
		return decide.Result{}, err
	}
	release, err := proc.prioritize(ctx)
	if err != nil {
		return decide.Result{}, err
	}
	defer release()
	engine := c.Engine()
	rows := 32
	if c.cfg.Resources != nil {
		rows = c.cfg.Resources.MaxRows
	}
	ids := make([]string, 0, len(questions))
	for id := range questions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	answers := make(map[string]decide.Answer, len(questions))
	for start := 0; start < len(ids); start += rows {
		batch := make(map[string]decide.Question, min(rows, len(ids)-start))
		for _, id := range ids[start:min(start+rows, len(ids))] {
			batch[id] = questions[id]
		}
		resp, err := c.exchange(ctx, proc, request{Method: methodDecide, Head: head, State: state, Questions: batch})
		if err != nil {
			return decide.Result{}, err
		}
		for id := range batch {
			answer, ok := resp.Answers[id]
			if !ok {
				return decide.Result{}, fmt.Errorf("%w: no answer for %q", decide.ErrEngine, id)
			}
			answers[id] = answer
		}
	}
	return decide.Result{Answers: answers, Engine: engine, Elapsed: time.Since(started)}, nil
}
