package pagedview

import "context"

// BuildSorted packs a sorted stream with only one unfinished page per tree level.
func (x *RangeIndex[T]) BuildSorted(ctx context.Context, source func(func(RangeItem[T]) error) error) error {
	var levels [][]Branch
	var push func(int, Branch) error
	push = func(level int, branch Branch) error {
		for len(levels) <= level {
			levels = append(levels, nil)
		}
		levels[level] = append(levels[level], branch)
		if len(levels[level]) < PageFanout {
			return nil
		}
		page := RangePage[T]{Children: levels[level]}
		id, err := x.Store.Write(ctx, 0, page)
		if err != nil {
			return err
		}
		parent, err := pageBranch(id, page)
		if err != nil {
			return err
		}
		levels[level] = nil
		return push(level+1, parent)
	}
	items := make([]RangeItem[T], 0, PageFanout)
	flush := func() error {
		if len(items) == 0 {
			return nil
		}
		page := RangePage[T]{Items: items}
		id, err := x.Store.Write(ctx, 0, page)
		if err != nil {
			return err
		}
		branch, err := pageBranch(id, page)
		if err != nil {
			return err
		}
		items = make([]RangeItem[T], 0, PageFanout)
		return push(0, branch)
	}
	last := ""
	err := source(func(item RangeItem[T]) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Key <= last || item.Weight < 0 || item.Unresolved < 0 {
			return ErrWeight
		}
		last = item.Key
		items = append(items, item)
		if len(items) == PageFanout {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	for level := 0; level < len(levels); level++ {
		if len(levels[level]) == 0 {
			continue
		}
		if len(levels[level]) == 1 && level == len(levels)-1 {
			x.Root = levels[level][0].Page
			return nil
		}
		page := RangePage[T]{Children: levels[level]}
		id, err := x.Store.Write(ctx, 0, page)
		if err != nil {
			return err
		}
		branch, err := pageBranch(id, page)
		if err != nil {
			return err
		}
		levels[level] = nil
		if err := push(level+1, branch); err != nil {
			return err
		}
	}
	x.Root = 0
	return nil
}
