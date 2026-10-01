package pagedview

import (
	"context"
	"errors"
	"math"
)

// OrderedWeights permits sparse additions without copying the underlying index.
// A missing Locate returns its insertion prefix alongside ErrMissing.
type OrderedWeights[T any] interface {
	Count(context.Context) (int64, error)
	Extent(context.Context) (int64, error)
	SelectItem(context.Context, int64) (RangeItem[T], error)
	Locate(context.Context, string) (RangeItem[T], int64, error)
}

type WeightedUnion[T any] struct{ Base, Added OrderedWeights[T] }

func (u WeightedUnion[T]) Extent(ctx context.Context) (int64, error) {
	base, err := u.Base.Extent(ctx)
	if err != nil {
		return 0, err
	}
	added, err := u.Added.Extent(ctx)
	if err != nil {
		return 0, err
	}
	return sumWeight(base, added)
}

func sumWeight(left, right int64) (int64, error) {
	if left < 0 || right < 0 || right > math.MaxInt64-left {
		return 0, ErrWeight
	}
	return left + right, nil
}

func (u WeightedUnion[T]) Locate(ctx context.Context, key string) (RangeItem[T], int64, error) {
	base, left, baseErr := u.Base.Locate(ctx, key)
	if baseErr != nil && !errors.Is(baseErr, ErrMissing) {
		return RangeItem[T]{}, 0, baseErr
	}
	added, right, addedErr := u.Added.Locate(ctx, key)
	if addedErr != nil && !errors.Is(addedErr, ErrMissing) {
		return RangeItem[T]{}, 0, addedErr
	}
	rank, err := sumWeight(left, right)
	if err != nil {
		return RangeItem[T]{}, 0, err
	}
	if baseErr != nil && addedErr != nil {
		return RangeItem[T]{}, rank, ErrMissing
	}
	selected := base
	if baseErr != nil {
		selected = added
	}
	selected.Weight, err = sumWeight(base.Weight, added.Weight)
	return selected, rank, err
}

// Select seeks combined prefixes in logarithmic key probes.
func (u WeightedUnion[T]) Select(ctx context.Context, rank int64) (RangeItem[T], int64, error) {
	total, err := u.Extent(ctx)
	if err != nil {
		return RangeItem[T]{}, 0, err
	}
	if rank < 0 || rank >= total {
		return RangeItem[T]{}, 0, ErrRange
	}
	key := ""
	for _, index := range []OrderedWeights[T]{u.Base, u.Added} {
		candidate, err := u.precedingKey(ctx, index, rank)
		if err != nil {
			return RangeItem[T]{}, 0, err
		}
		if candidate > key {
			key = candidate
		}
	}
	item, prefix, err := u.Locate(ctx, key)
	if err != nil {
		return RangeItem[T]{}, 0, err
	}
	if rank-prefix >= item.Weight {
		return RangeItem[T]{}, 0, ErrRange
	}
	return item, rank - prefix, nil
}

func (u WeightedUnion[T]) precedingKey(ctx context.Context, index OrderedWeights[T], rank int64) (string, error) {
	count, err := index.Count(ctx)
	if err != nil {
		return "", err
	}
	low, high := int64(0), count
	key := ""
	for low < high {
		mid := low + (high-low)/2
		item, err := index.SelectItem(ctx, mid)
		if err != nil {
			return "", err
		}
		_, prefix, err := u.Locate(ctx, item.Key)
		if err != nil {
			return "", err
		}
		if prefix <= rank {
			low = mid + 1
			key = item.Key
		} else {
			high = mid
		}
	}
	return key, nil
}

// UnitWeights uses the same keys for a directory whose children are collapsed.
type UnitWeights[T any] struct{ Index *RangeIndex[T] }

func (u UnitWeights[T]) Count(ctx context.Context) (int64, error)  { return u.Index.Count(ctx) }
func (u UnitWeights[T]) Extent(ctx context.Context) (int64, error) { return u.Index.Count(ctx) }
func (u UnitWeights[T]) SelectItem(ctx context.Context, rank int64) (RangeItem[T], error) {
	item, err := u.Index.SelectItem(ctx, rank)
	if err == nil {
		item.Weight = 1
	}
	return item, err
}
func (u UnitWeights[T]) Locate(ctx context.Context, key string) (RangeItem[T], int64, error) {
	item, rank, err := u.Index.LocateItem(ctx, key)
	if err == nil {
		item.Weight = 1
	}
	return item, rank, err
}
