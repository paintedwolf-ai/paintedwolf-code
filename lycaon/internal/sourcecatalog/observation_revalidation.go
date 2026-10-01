package sourcecatalog

import (
	"context"
	"github.com/lycaon/lycaon/internal/pagedview"
)

// An unchanged membership retains its pages and sequence while refreshing its observation.
func (b *structuralBuilder) revalidate(ctx context.Context, dir string) error {
	if b.base == nil {
		return nil
	}
	previous, found, err := b.base.directories.Get(ctx, dir)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	current, observation, err := b.children(ctx, dir)
	if err != nil {
		return err
	}
	if !observation.Complete || stateOf(previous.observation) != stateOf(observation) {
		return nil
	}
	old := &pagedview.RangeIndex[TreeItem]{Store: b.base, Root: previous.page}
	after := ""
	for {
		before, err := old.ReadAfter(ctx, after, indexBatchSize)
		if err != nil {
			return err
		}
		now, err := current.ReadAfter(ctx, after, indexBatchSize)
		if err != nil {
			return err
		}
		if len(before) != len(now) {
			return nil
		}
		for i, item := range before {
			if item.Key != now[i].Key || item.Value.Path != now[i].Value.Path || item.Value.Symlink != now[i].Value.Symlink {
				return nil
			}
		}
		if len(before) == 0 {
			break
		}
		after = before[len(before)-1].Key
	}
	observation.Sequence = previous.observation.Sequence
	observation.FirstListed = previous.observation.FirstListed
	current.Root = previous.page
	return b.save(ctx, current, observation)
}
