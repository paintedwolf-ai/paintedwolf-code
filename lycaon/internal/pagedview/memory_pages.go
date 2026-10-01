package pagedview

import (
	"context"
	"unsafe"
)

// MemoryPages stores small immutable plans. Mutation is serialized by its adapter.
// Large live indexes implement PageStore on their existing ephemeral database.
type MemoryPages[T any] struct {
	pages map[uint64]RangePage[T]
	next  uint64
}

func (s *MemoryPages[T]) Read(_ context.Context, id uint64) (RangePage[T], error) {
	page, ok := s.pages[id]
	if !ok {
		return RangePage[T]{}, ErrMissing
	}
	return page, nil
}
func (s *MemoryPages[T]) Write(_ context.Context, id uint64, page RangePage[T]) (uint64, error) {
	if s.pages == nil {
		s.pages = make(map[uint64]RangePage[T])
	}
	if id == 0 {
		s.next++
		id = s.next
	}
	items := make([]RangeItem[T], len(page.Items))
	copy(items, page.Items)
	children := make([]Branch, len(page.Children))
	copy(children, page.Children)
	s.pages[id] = RangePage[T]{Items: items, Children: children}
	return id, nil
}
func (s *MemoryPages[T]) Delete(_ context.Context, id uint64) error { delete(s.pages, id); return nil }

// RetainedBytes charges backing capacities and map entries. Adapters account
// for any allocations referenced by their payload values separately.
func (s *MemoryPages[T]) RetainedBytes() int64 {
	bytes := int64(unsafe.Sizeof(*s))
	for _, page := range s.pages {
		bytes += 64 + int64(unsafe.Sizeof(page))
		bytes += int64(cap(page.Items)) * int64(unsafe.Sizeof(RangeItem[T]{})) // #nosec G115 -- A Go value size fits the allocated address space.
		bytes += int64(cap(page.Children)) * int64(unsafe.Sizeof(Branch{}))
		for _, item := range page.Items {
			bytes += int64(len(item.Key))
		}
		for _, branch := range page.Children {
			bytes += int64(len(branch.Key))
		}
	}
	return bytes
}
