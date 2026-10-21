package commandsurface

import (
	"context"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"mvdan.cc/sh/v3/syntax"
)

// ParseAdmitted validates one unit with parser scratch charged to the enclosing
// program's working account as the selected parser grows, up to the per-unit
// evaluation_memory_bytes ceiling. Scratch is released once lowering returns;
// the retained plan keeps only its program_state_bytes lease. Concurrent working
// reservations (stream codecs, predicates, background jobs) therefore share the
// aggregate with the parser's actual use rather than a whole-ceiling hold.
func ParseAdmitted(ctx context.Context, memory *toolcontract.ProgramMemory, source []byte, options ParseOptions) (*ParsedProgram, error) {
	if ctx == nil || memory == nil {
		return nil, fmt.Errorf("program parser requires context and scratch admission")
	}
	return parseProgram(ctx, source, options, memory)
}

// scratchLedger is one parse's upstream allocation ledger. With program memory
// it admits growth from the working account in bounded chunks; release returns
// every admitted chunk.
func scratchLedger(ctx context.Context, maximum int64, memory *toolcontract.ProgramMemory) (*syntax.AllocationBudget, func(), error) {
	if memory == nil {
		pool, err := syntax.NewAllocationBudget(maximum)
		return pool, func() {}, err
	}
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
		releases = nil
	}
	pool, err := syntax.NewAdmittedAllocationBudget(maximum, func(n int64) error {
		done, err := memory.Reserve(ctx, n)
		if err != nil {
			return err
		}
		releases = append(releases, done)
		return nil
	})
	return pool, release, err
}

type contextSource struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextSource) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}
