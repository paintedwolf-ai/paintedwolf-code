package syntax

import (
	"errors"
	"fmt"
	"math"
	"unsafe"
)

// ErrBraceMixedRange reports a {x..y} sequence with one integer and one letter
// endpoint, which SplitBraces leaves literal.
var ErrBraceMixedRange = errors.New("brace sequence endpoints mix an integer and a letter")

// ClassifyBraces reports a real unquoted brace expansion without mutating word.
// It uses SplitBraces' selected algorithm and charges its temporary allocations
// before they occur. Malformed braces retain their ordinary literal semantics.
func ClassifyBraces(word *Word, pool *AllocationBudget) (expanded bool, err error) {
	split, err := splitBracesBounded(word, pool, nil)
	return split != nil, err
}

// SplitBracesBounded returns a copy of word with its brace expansions as
// BraceExp parts, or nil when the word has none; word is not mutated. It
// charges the same allocations as ClassifyBraces and refuses a mixed-type
// sequence with ErrBraceMixedRange instead of leaving it literal.
func SplitBracesBounded(word *Word, pool *AllocationBudget) (*Word, error) {
	mixed := false
	split, err := splitBracesBounded(word, pool, &mixed)
	if err == nil && mixed {
		return nil, ErrBraceMixedRange
	}
	return split, err
}

func splitBracesBounded(word *Word, pool *AllocationBudget, mixed *bool) (split *Word, err error) {
	if pool == nil {
		return nil, fmt.Errorf("brace classification requires an allocation budget")
	}
	if word == nil {
		return nil, nil
	}
	defer func() {
		if value := recover(); value != nil {
			if abort, ok := value.(budgetAbort); ok {
				split, err = nil, abort.err
				return
			}
			panic(value)
		}
	}()
	copy := *word
	if !splitBraces(&copy, pool, mixed) {
		return nil, nil
	}
	for _, part := range copy.Parts {
		if _, ok := part.(*BraceExp); ok {
			return braceNode(pool, copy), nil
		}
	}
	return nil, nil
}

func braceReserve(pool *AllocationBudget, bytes int64) {
	if pool != nil {
		if err := pool.ReserveHeap(bytes, Pos{}); err != nil {
			panic(budgetAbort{err.(*BudgetError)})
		}
	}
}

func braceNode[T any](pool *AllocationBudget, value T) *T {
	braceReserve(pool, int64(unsafe.Sizeof(value)))
	node := new(T)
	*node = value
	return node
}

func braceAppend[T any](pool *AllocationBudget, dst []T, values ...T) []T {
	if pool == nil {
		return append(dst, values...)
	}
	if len(values) > math.MaxInt-len(dst) {
		braceReserve(pool, math.MaxInt64)
	}
	needed := len(dst) + len(values)
	if needed > cap(dst) {
		capacity := needed
		if cap(dst) <= math.MaxInt/2 && cap(dst)*2 > capacity {
			capacity = cap(dst) * 2
		}
		var value T
		size := int64(unsafe.Sizeof(value))
		if size > 0 && int64(capacity) > math.MaxInt64/size {
			braceReserve(pool, math.MaxInt64)
		}
		braceReserve(pool, int64(capacity)*size)
		replacement := make([]T, len(dst), capacity)
		copy(replacement, dst)
		dst = replacement
	}
	return append(dst, values...)
}

func braceLiteral(word *Word, pool *AllocationBudget) string {
	if pool != nil {
		// Word.Lit joins literal parts; ParseInt may copy an invalid number into
		// its typed diagnostic. Bound both existing algorithms before calling.
		bytes := int64(256)
		for _, part := range word.Parts {
			lit, ok := part.(*Lit)
			if !ok {
				return ""
			}
			if int64(len(lit.Value)) > (math.MaxInt64-bytes-128)/4 {
				braceReserve(pool, math.MaxInt64)
			}
			bytes += 128 + 4*int64(len(lit.Value))
		}
		braceReserve(pool, bytes)
	}
	return word.Lit()
}
