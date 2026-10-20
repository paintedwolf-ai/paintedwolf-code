package commandsurface

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// ScratchAdmission reserves parser working storage against the enclosing
// program's resource ledger. It supplies accounting, never operation authority.
// Captured source and retained plans additionally consume program_state_bytes.
type ScratchAdmission interface {
	ReserveParserScratch(context.Context, int64) (release func(), err error)
}

// ProgramParser holds its reservation through validation and host lowering.
// Callers acquire it before source capture and close it after parsing. A parser
// must not be shared concurrently or used after Close.
type ProgramParser struct {
	ctx     context.Context
	release func()
	once    sync.Once
	closed  bool
}

func NewProgramParser(ctx context.Context, admission ScratchAdmission) (*ProgramParser, error) {
	if ctx == nil || admission == nil {
		return nil, fmt.Errorf("program parser requires context and scratch admission")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	catalog, err := programLanguage()
	if err != nil {
		return nil, err
	}
	release, err := admission.ReserveParserScratch(ctx, languageLimit(catalog, "evaluation_memory_bytes"))
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("parser scratch admission returned no release")
	}
	return &ProgramParser{ctx: ctx, release: release}, nil
}

func (p *ProgramParser) Close() error {
	p.once.Do(func() { p.closed = true; p.release() })
	return nil
}

func (p *ProgramParser) Parse(source []byte, options ParseOptions) (*ParsedProgram, error) {
	if p.closed {
		return nil, fmt.Errorf("program parser reservation is closed")
	}
	return parseProgram(p.ctx, source, options)
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
