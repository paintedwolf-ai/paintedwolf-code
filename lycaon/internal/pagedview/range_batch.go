package pagedview

import (
	"context"
	"sort"
)

// SetBatch repairs each affected page once. Callers bound a batch independently
// of the complete inventory and commit its root with the source observations.
func (x *RangeIndex[T]) SetBatch(ctx context.Context, items []RangeItem[T]) error {
	if len(items) == 0 {
		return nil
	}
	ordered := append([]RangeItem[T](nil), items...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key < ordered[j].Key })
	for i, item := range ordered {
		if item.Key == "" || item.Weight < 0 || item.Unresolved < 0 || i > 0 && ordered[i-1].Key == item.Key {
			return ErrWeight
		}
	}
	branches, err := x.setBatch(ctx, x.Root, ordered)
	if err != nil {
		return err
	}
	for len(branches) > 1 {
		branches, err = x.savePages(ctx, 0, RangePage[T]{Children: branches})
		if err != nil {
			return err
		}
	}
	x.Root = branches[0].Page
	return nil
}

func (x *RangeIndex[T]) setBatch(ctx context.Context, id uint64, items []RangeItem[T]) ([]Branch, error) {
	var page RangePage[T]
	var err error
	if id != 0 {
		page, err = x.Store.Read(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	if len(page.Children) == 0 {
		merged := make([]RangeItem[T], 0, len(page.Items)+len(items))
		left, right := 0, 0
		for left < len(page.Items) || right < len(items) {
			switch {
			case right == len(items):
				merged = append(merged, page.Items[left:]...)
				left = len(page.Items)
			case left == len(page.Items):
				merged = append(merged, items[right:]...)
				right = len(items)
			case page.Items[left].Key < items[right].Key:
				merged = append(merged, page.Items[left])
				left++
			default:
				if page.Items[left].Key == items[right].Key {
					left++
				}
				merged = append(merged, items[right])
				right++
			}
		}
		return x.savePages(ctx, id, RangePage[T]{Items: merged})
	}
	children := make([]Branch, 0, len(page.Children)+len(items)/PageFanout+1)
	at := 0
	for i, child := range page.Children {
		start := at
		for at < len(items) && (i+1 == len(page.Children) || items[at].Key < page.Children[i+1].Key) {
			at++
		}
		if at == start {
			children = append(children, child)
			continue
		}
		changed, err := x.setBatch(ctx, child.Page, items[start:at])
		if err != nil {
			return nil, err
		}
		children = append(children, changed...)
	}
	return x.savePages(ctx, id, RangePage[T]{Children: children})
}

func (x *RangeIndex[T]) savePages(ctx context.Context, id uint64, page RangePage[T]) ([]Branch, error) {
	count := max(len(page.Items), len(page.Children))
	branches := make([]Branch, 0, (count+PageFanout-1)/PageFanout)
	for start := 0; start < count; start += PageFanout {
		end := min(count, start+PageFanout)
		var part RangePage[T]
		if len(page.Items) > 0 {
			part.Items = page.Items[start:end]
		} else {
			part.Children = page.Children[start:end]
		}
		branch, err := pageBranch(id, part)
		if err != nil {
			return nil, err
		}
		saved, err := x.Store.Write(ctx, id, part)
		if err != nil {
			return nil, err
		}
		branch.Page = saved
		branches = append(branches, branch)
		id = 0
	}
	return branches, nil
}
