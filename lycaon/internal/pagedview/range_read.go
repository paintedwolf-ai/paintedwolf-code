package pagedview

import (
	"context"
	"sort"
)

// ReadAfter returns a bounded key-ordered page, excluding the cursor key.
func (x *RangeIndex[T]) ReadAfter(ctx context.Context, after string, limit int) ([]RangeItem[T], error) {
	if limit <= 0 {
		return nil, ErrRange
	}
	var items []RangeItem[T]
	if x.Root == 0 {
		return items, nil
	}
	err := x.readAfter(ctx, x.Root, after, limit, &items)
	return items, err
}

func (x *RangeIndex[T]) readAfter(ctx context.Context, id uint64, after string, limit int, items *[]RangeItem[T]) error {
	page, err := x.Store.Read(ctx, id)
	if err != nil {
		return err
	}
	if len(page.Children) == 0 {
		start := sort.Search(len(page.Items), func(i int) bool { return page.Items[i].Key > after })
		end := start + min(len(page.Items)-start, limit-len(*items))
		*items = append(*items, page.Items[start:end]...)
		return nil
	}
	for _, child := range page.Children[childAt(page.Children, after):] {
		if err := x.readAfter(ctx, child.Page, after, limit, items); err != nil {
			return err
		}
		if len(*items) == limit {
			break
		}
	}
	return nil
}
