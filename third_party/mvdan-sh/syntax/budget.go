package syntax

import (
	"fmt"
	"math"
	"unicode/utf8"
	"unsafe"
)

// Budget bounds scratch allocation and active parser frames during Parse.
// It does not count final syntax nodes or AST parent-edge depth. Callers must
// reserve MaxBytes against their aggregate memory budget before constructing
// the parser, then apply exact selected-tree limits before admitting their plan.
type Budget struct {
	MaxBytes int64
	Pool     *AllocationBudget
	implicit bool
}

// WithBudget applies a caller-reserved scratch budget to Parse. Other parsing
// entry points refuse this option rather than silently running without bounds.
func WithBudget(budget Budget) ParserOption {
	return func(p *Parser) { p.budget = budget; p.budgetConfigured = true }
}

// BudgetError reports a refused reservation, without a partial parsed tree.
type BudgetError struct {
	Kind              string
	Maximum, Observed int64
	Pos               Pos
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("%s exceeded: maximum %d, requested %d", e.Kind, e.Maximum, e.Observed)
}

type budgetAbort struct{ err *BudgetError }

// A frame reserves 4 KiB before entering upstream parser/lexer/arithmetic
// methods. The selected source's fixed locals are smaller than this envelope;
// variable payloads are independently charged. The envelope includes a next-call
// prologue before that callee executes its entry guard, register spills and call
// bookkeeping. This is a byte reservation, not a grammar nesting ceiling.
const parserFrameBytes int64 = 4096

func (p *Parser) beginBudget() {
	p.scratchUsed, p.scratchFrames = 0, 0
	p.budgetActive = p.budgetConfigured
	if p.budgetActive && p.budget.Pool == nil {
		p.budget.Pool = &AllocationBudget{maximum: p.budget.MaxBytes}
		p.budget.implicit = true
	}
	p.reserveHeap(int64(unsafe.Sizeof(*p)))
}
func (p *Parser) unsupportedBudgetEntry() error {
	return &BudgetError{Kind: "parser_entry_unsupported", Maximum: p.budget.MaxBytes}
}
func (p *Parser) finishBudget(file **File, err *error) {
	p.budgetActive = false
	if p.budget.implicit {
		p.budget.Pool = nil
		p.budget.implicit = false
	}
	if value := recover(); value != nil {
		if abort, ok := value.(budgetAbort); ok {
			*file, *err = nil, abort.err
			p.f, p.litBatch, p.wordBatch, p.litBs = nil, nil, nil, nil
			return
		}
		panic(value)
	}
}
func (p *Parser) reserveScratch(bytes int64) {
	if !p.budgetActive {
		return
	}
	if err := p.budget.Pool.ReserveBytes(bytes, p.pos); err != nil {
		panic(budgetAbort{err.(*BudgetError)})
	}
	p.scratchUsed += bytes
}
func (p *Parser) enterFrame() {
	if !p.budgetActive {
		return
	}
	p.reserveScratch(parserFrameBytes)
	p.scratchUsed -= parserFrameBytes
	p.scratchFrames += parserFrameBytes
}
func (p *Parser) leaveFrame() {
	if p.budgetActive {
		p.scratchFrames -= parserFrameBytes
		p.budget.Pool.releaseFrame(parserFrameBytes)
	}
}

// Allocations reserve a rounded payload plus object bookkeeping. Old and new
// backing arrays both remain charged; this also bounds allocator churn before GC.
func (p *Parser) reserveHeap(bytes int64) {
	if !p.budgetActive {
		return
	}
	before := p.budget.Pool.UsedBytes()
	if err := p.budget.Pool.ReserveHeap(bytes, p.pos); err != nil {
		panic(budgetAbort{err.(*BudgetError)})
	}
	p.scratchUsed += p.budget.Pool.UsedBytes() - before
}
func budgetNode[T any](p *Parser, value T) *T {
	p.reserveHeap(int64(unsafe.Sizeof(value)))
	node := new(T)
	*node = value
	return node
}
func budgetMake[T any](p *Parser, length, capacity int) []T {
	var value T
	size := int64(unsafe.Sizeof(value))
	if size > 0 && int64(capacity) > math.MaxInt64/size {
		p.reserveScratch(math.MaxInt64)
	}
	p.reserveHeap(int64(capacity) * size)
	return make([]T, length, capacity)
}
func budgetAppend[T any](p *Parser, dst []T, values ...T) []T {
	if !p.budgetActive {
		return append(dst, values...)
	}
	if len(values) > math.MaxInt-len(dst) {
		p.reserveScratch(math.MaxInt64)
	}
	needed := len(dst) + len(values)
	if needed > cap(dst) {
		capacity := needed
		if cap(dst) <= math.MaxInt/2 && cap(dst)*2 > capacity {
			capacity = cap(dst) * 2
		}
		replacement := budgetMake[T](p, len(dst), capacity)
		copy(replacement, dst)
		dst = replacement
	}
	return append(dst, values...)
}
func budgetAppendString(p *Parser, dst []byte, value string) []byte {
	if !p.budgetActive {
		return append(dst, value...)
	}
	if len(value) > math.MaxInt-len(dst) {
		p.reserveScratch(math.MaxInt64)
	}
	needed := len(dst) + len(value)
	if needed > cap(dst) {
		capacity := needed
		if cap(dst) <= math.MaxInt/2 && cap(dst)*2 > capacity {
			capacity = cap(dst) * 2
		}
		replacement := budgetMake[byte](p, len(dst), capacity)
		copy(replacement, dst)
		dst = replacement
	}
	return append(dst, value...)
}
func (p *Parser) budgetString(value []byte) string {
	p.reserveHeap(int64(len(value)))
	return string(value)
}
func (p *Parser) budgetRune(value rune) string {
	n := utf8.RuneLen(value)
	if n < 0 {
		n = 3
	}
	p.reserveHeap(int64(n))
	return string(value)
}
func (p *Parser) budgetFormat(format string, args ...any) string {
	bytes := int64(len(format) + 4096)
	for _, arg := range args {
		if value, ok := arg.(string); ok {
			bytes += int64(len(value)) * 12
		}
	}
	p.reserveHeap(bytes)
	return fmt.Sprintf(format, args...)
}

// ScratchBytes reports this parse's cumulative charged heap allocations. Shared
// pool usage additionally includes caller allocations and subsequent phases.
func (p *Parser) ScratchBytes() int64 { return p.scratchUsed }
