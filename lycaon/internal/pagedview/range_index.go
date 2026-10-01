package pagedview

import (
	"context"
	"errors"
	"math"
	"sort"
)

const PageFanout = 128

var ErrRange = errors.New("presentation coordinate outside extent")
var ErrWeight = errors.New("invalid presentation weight")

// RangeItem refers to domain data. Weight is its number of visible rows.
type RangeItem[T any] struct {
	Key                 string
	Value               T
	Weight              int64
	Unresolved          int64
	Fingerprint         Fingerprint
	BaselineFingerprint Fingerprint
}
type Branch struct {
	Key                 string
	Page                uint64
	Weight, Count       int64
	Unresolved          int64
	Fingerprint         Fingerprint
	BaselineFingerprint Fingerprint
}
type RangePage[T any] struct {
	Items    []RangeItem[T]
	Children []Branch
}

// PageStore operations belong to the adapter's transaction. A write either commits
// all modified pages and the new root, or rolls them all back.
type PageStore[T any] interface {
	Read(context.Context, uint64) (RangePage[T], error)
	Write(context.Context, uint64, RangePage[T]) (uint64, error)
	Delete(context.Context, uint64) error
}

type RangeIndex[T any] struct {
	Store PageStore[T]
	Root  uint64
}

func pageBranch[T any](id uint64, page RangePage[T]) (Branch, error) {
	branch := Branch{Page: id}
	add := func(key string, weight, unresolved int64) error {
		if weight < 0 || weight > math.MaxInt64-branch.Weight || unresolved < 0 || unresolved > math.MaxInt64-branch.Unresolved {
			return ErrWeight
		}
		if branch.Key == "" {
			branch.Key = key
		}
		branch.Weight += weight
		branch.Unresolved += unresolved
		return nil
	}
	for _, item := range page.Items {
		if err := add(item.Key, item.Weight, item.Unresolved); err != nil {
			return Branch{}, err
		}
		branch.Count++
		branch.Fingerprint = branch.Fingerprint.Combine(item.Fingerprint)
		branch.BaselineFingerprint = branch.BaselineFingerprint.Combine(item.BaselineFingerprint)

	}
	for _, child := range page.Children {
		if err := add(child.Key, child.Weight, child.Unresolved); err != nil {
			return Branch{}, err
		}
		if child.Count < 0 || child.Count > math.MaxInt64-branch.Count {
			return Branch{}, ErrWeight
		}
		branch.Count += child.Count
		branch.Fingerprint = branch.Fingerprint.Combine(child.Fingerprint)
		branch.BaselineFingerprint = branch.BaselineFingerprint.Combine(child.BaselineFingerprint)

	}
	return branch, nil
}

func (x *RangeIndex[T]) Extent(ctx context.Context) (int64, error) {
	if x.Root == 0 {
		return 0, nil
	}
	page, err := x.Store.Read(ctx, x.Root)
	if err != nil {
		return 0, err
	}
	branch, err := pageBranch(x.Root, page)
	return branch.Weight, err
}

// Select descends by aggregate weights, without walking preceding entries.
func (x *RangeIndex[T]) Select(ctx context.Context, rank int64) (RangeItem[T], int64, error) {
	return x.selectRank(ctx, rank, false)
}

func (x *RangeIndex[T]) SelectItem(ctx context.Context, rank int64) (RangeItem[T], error) {
	item, _, err := x.selectRank(ctx, rank, true)
	return item, err
}

func (x *RangeIndex[T]) selectRank(ctx context.Context, rank int64, items bool) (RangeItem[T], int64, error) {
	var zero RangeItem[T]
	if rank < 0 || x.Root == 0 {
		return zero, 0, ErrRange
	}
	id := x.Root
	for {
		page, err := x.Store.Read(ctx, id)
		if err != nil {
			return zero, 0, err
		}
		if len(page.Children) == 0 {
			for _, item := range page.Items {
				weight := item.Weight
				if items {
					weight = 1
				}
				if rank < weight {
					return item, rank, nil
				}
				rank -= weight
			}
			return zero, 0, ErrRange
		}
		found := false
		for _, child := range page.Children {
			weight := child.Weight
			if items {
				weight = child.Count
			}
			if rank < weight {
				id = child.Page
				found = true
				break
			}
			rank -= weight
		}
		if !found {
			return zero, 0, ErrRange
		}
	}
}

func childAt(children []Branch, key string) int {
	return max(0, sort.Search(len(children), func(i int) bool { return children[i].Key > key })-1)
}

// Locate returns the prefix weight, including zero-weight entries for collapsed data.
func (x *RangeIndex[T]) Locate(ctx context.Context, key string) (RangeItem[T], int64, error) {
	return x.locate(ctx, key, false)
}

func (x *RangeIndex[T]) LocateItem(ctx context.Context, key string) (RangeItem[T], int64, error) {
	return x.locate(ctx, key, true)
}

func (x *RangeIndex[T]) locate(ctx context.Context, key string, items bool) (RangeItem[T], int64, error) {
	var zero RangeItem[T]
	if x.Root == 0 {
		return zero, 0, ErrMissing
	}
	id, rank := x.Root, int64(0)
	for {
		page, err := x.Store.Read(ctx, id)
		if err != nil {
			return zero, 0, err
		}
		if len(page.Children) == 0 {
			for _, item := range page.Items {
				if item.Key == key {
					return item, rank, nil
				}
				if item.Key > key {
					break
				}
				if items {
					rank++
				} else {
					rank += item.Weight
				}
			}
			return zero, rank, ErrMissing
		}
		at := childAt(page.Children, key)
		for _, child := range page.Children[:at] {
			if items {
				rank += child.Count
			} else {
				rank += child.Weight
			}
		}
		id = page.Children[at].Page
	}
}

func (x *RangeIndex[T]) Set(ctx context.Context, item RangeItem[T]) error {
	if item.Key == "" || item.Weight < 0 || item.Unresolved < 0 {
		return ErrWeight
	}
	branches, err := x.set(ctx, x.Root, item)
	if err != nil {
		return err
	}
	if len(branches) == 1 {
		x.Root = branches[0].Page
		return nil
	}
	id, err := x.Store.Write(ctx, 0, RangePage[T]{Children: branches})
	if err == nil {
		x.Root = id
	}
	return err
}

func (x *RangeIndex[T]) set(ctx context.Context, id uint64, item RangeItem[T]) ([]Branch, error) {
	var page RangePage[T]
	var err error
	if id != 0 {
		page, err = x.Store.Read(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	if len(page.Children) == 0 {
		at := sort.Search(len(page.Items), func(i int) bool { return page.Items[i].Key >= item.Key })
		items := append([]RangeItem[T](nil), page.Items...)
		if at < len(items) && items[at].Key == item.Key {
			items[at] = item
		} else {
			items = append(items, RangeItem[T]{})
			copy(items[at+1:], items[at:])
			items[at] = item
		}
		page.Items = items
	} else {
		at := childAt(page.Children, item.Key)
		children, err := x.set(ctx, page.Children[at].Page, item)
		if err != nil {
			return nil, err
		}
		next := make([]Branch, 0, len(page.Children)+len(children)-1)
		next = append(next, page.Children[:at]...)
		next = append(next, children...)
		next = append(next, page.Children[at+1:]...)
		page.Children = next
	}
	return x.saveSplit(ctx, id, page)
}

func (x *RangeIndex[T]) saveSplit(ctx context.Context, id uint64, page RangePage[T]) ([]Branch, error) {
	pages := []RangePage[T]{page}
	if len(page.Items) > PageFanout {
		middle := len(page.Items) / 2
		pages = []RangePage[T]{{Items: page.Items[:middle]}, {Items: page.Items[middle:]}}
	} else if len(page.Children) > PageFanout {
		middle := len(page.Children) / 2
		pages = []RangePage[T]{{Children: page.Children[:middle]}, {Children: page.Children[middle:]}}
	}
	branches := make([]Branch, 0, len(pages))
	for _, part := range pages {
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

func (x *RangeIndex[T]) Remove(ctx context.Context, key string) error {
	if x.Root == 0 {
		return nil
	}
	branch, err := x.remove(ctx, x.Root, key)
	if err != nil {
		return err
	}
	x.Root = branch.Page
	if x.Root != 0 {
		page, err := x.Store.Read(ctx, x.Root)
		if err != nil {
			return err
		}
		if len(page.Children) == 1 {
			old := x.Root
			x.Root = page.Children[0].Page
			return x.Store.Delete(ctx, old)
		}
	}
	return nil
}

func (x *RangeIndex[T]) remove(ctx context.Context, id uint64, key string) (Branch, error) {
	page, err := x.Store.Read(ctx, id)
	if err != nil {
		return Branch{}, err
	}
	if len(page.Children) == 0 {
		at := sort.Search(len(page.Items), func(i int) bool { return page.Items[i].Key >= key })
		if at < len(page.Items) && page.Items[at].Key == key {
			items := append([]RangeItem[T](nil), page.Items[:at]...)
			page.Items = append(items, page.Items[at+1:]...)
		}
	} else {
		at := childAt(page.Children, key)
		child, err := x.remove(ctx, page.Children[at].Page, key)
		if err != nil {
			return Branch{}, err
		}
		children := append([]Branch(nil), page.Children...)
		if child.Page == 0 {
			children = append(children[:at], children[at+1:]...)
		} else {
			children[at] = child
		}
		page.Children = children
	}
	if len(page.Children)+len(page.Items) == 0 {
		return Branch{}, x.Store.Delete(ctx, id)
	}
	saved, err := x.Store.Write(ctx, id, page)
	if err != nil {
		return Branch{}, err
	}
	return pageBranch(saved, page)
}

func (x *RangeIndex[T]) Count(ctx context.Context) (int64, error) {
	if x.Root == 0 {
		return 0, nil
	}
	page, err := x.Store.Read(ctx, x.Root)
	if err != nil {
		return 0, err
	}
	branch, err := pageBranch(x.Root, page)
	return branch.Count, err
}

// Unresolved counts incomplete contributions without walking their rows.
func (x *RangeIndex[T]) Unresolved(ctx context.Context) (int64, error) {
	if x.Root == 0 {
		return 0, nil
	}
	page, err := x.Store.Read(ctx, x.Root)
	if err != nil {
		return 0, err
	}
	branch, err := pageBranch(x.Root, page)
	return branch.Unresolved, err
}

// VisitUnresolved skips complete branches; false stops the walk.
func (x *RangeIndex[T]) VisitUnresolved(ctx context.Context, visit func(RangeItem[T]) (bool, error)) error {
	var walk func(uint64) (bool, error)
	walk = func(id uint64) (bool, error) {
		if id == 0 {
			return true, nil
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		page, err := x.Store.Read(ctx, id)
		if err != nil {
			return false, err
		}
		for _, item := range page.Items {
			if item.Unresolved == 0 {
				continue
			}
			more, err := visit(item)
			if err != nil || !more {
				return more, err
			}
		}
		for _, child := range page.Children {
			if child.Unresolved == 0 {
				continue
			}
			more, err := walk(child.Page)
			if err != nil || !more {
				return more, err
			}
		}
		return true, nil
	}
	_, err := walk(x.Root)
	return err
}
