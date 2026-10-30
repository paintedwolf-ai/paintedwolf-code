package commandsurface

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"mvdan.cc/sh/v3/syntax"
)

// ProgramStateAdmission charges live plans and snapshots to their enclosing account.
type ProgramStateAdmission interface {
	ReserveProgramState(context.Context, int64) (func(), error)
}

// ProgramMemoryAdmission shares actual parser growth and retained state with the program.
type ProgramMemoryAdmission interface {
	ProgramStateAdmission
	Reserve(context.Context, int64) (func(), error)
}

// ProgramParser admits one unit's initial scratch before capture. Its same ledger
// meters subsequent allocations; closing it leaves retained plan leases intact.
type ProgramParser struct {
	ctx          context.Context
	memory       ProgramMemoryAdmission
	pool         *syntax.AllocationBudget
	release      func()
	mu           sync.Mutex
	used, closed bool
}

func NewProgramParser(ctx context.Context, memory ProgramMemoryAdmission) (*ProgramParser, error) {
	if ctx == nil || memory == nil {
		return nil, fmt.Errorf("program parser requires context and scratch admission")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	catalog, err := programLanguage()
	if err != nil {
		return nil, err
	}
	maximum := languageLimit(catalog, "evaluation_memory_bytes")
	pool, release, err := scratchLedger(ctx, maximum, memory, min(maximum, 64<<10))
	if err != nil {
		release()
		return nil, err
	}
	return &ProgramParser{ctx: ctx, memory: memory, pool: pool, release: release}, nil
}

func (p *ProgramParser) Parse(source []byte, options ParseOptions) (*ParsedProgram, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.used {
		return nil, fmt.Errorf("program parser reservation is closed or consumed")
	}
	p.used = true
	return parseProgram(p.ctx, source, options, p.memory, p.pool)
}

func (p *ProgramParser) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		p.release()
		p.ctx, p.memory, p.pool, p.release = nil, nil, nil, nil
	}
	return nil
}

// ParseAdmitted is the immediate-input path through the same staged parser.
func ParseAdmitted(ctx context.Context, memory *toolcontract.ProgramMemory, source []byte, options ParseOptions) (*ParsedProgram, error) {
	if memory == nil {
		return nil, fmt.Errorf("program parser requires context and scratch admission")
	}
	parser, err := NewProgramParser(ctx, memory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = parser.Close() }()
	return parser.Parse(source, options)
}

func scratchLedger(ctx context.Context, maximum int64, memory ProgramMemoryAdmission, initial int64) (*syntax.AllocationBudget, func(), error) {
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
	reserve := func(n int64) error {
		done, err := memory.Reserve(ctx, n)
		if err != nil {
			return err
		}
		if done == nil {
			return fmt.Errorf("parser scratch admission returned no release")
		}
		releases = append(releases, done)
		return nil
	}
	if initial > 0 {
		if err := reserve(initial); err != nil {
			return nil, release, err
		}
	}
	credit := initial
	pool, err := syntax.NewAdmittedAllocationBudget(maximum, func(n int64) error {
		covered := min(n, credit)
		if n > covered {
			if err := reserve(n - covered); err != nil {
				return err
			}
		}
		credit -= covered
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
