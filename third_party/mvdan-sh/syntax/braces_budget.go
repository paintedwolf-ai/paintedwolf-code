package syntax

import (
	"fmt"
	"math"
	"unsafe"
)

// ClassifyBraces reports a real unquoted brace expansion without mutating word.
// It uses SplitBraces' selected algorithm and charges its temporary allocations
// before they occur. Malformed braces retain their ordinary literal semantics.
func ClassifyBraces(word *Word, pool *AllocationBudget) (expanded bool, err error) {
	if pool == nil {
		return false, fmt.Errorf("brace classification requires an allocation budget")
	}
	if word == nil {
		return false, nil
	}
	defer func() {
		if value := recover(); value != nil {
			if abort, ok := value.(budgetAbort); ok {
				expanded, err = false, abort.err
				return
			}
			panic(value)
		}
	}()
	copy := *word
	if !splitBraces(&copy, pool) {
		return false, nil
	}
	for _, part := range copy.Parts {
		if _, ok := part.(*BraceExp); ok {
			return true, nil
		}
	}
	return false, nil
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
