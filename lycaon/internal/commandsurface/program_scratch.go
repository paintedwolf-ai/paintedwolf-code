package commandsurface

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ProgramParser holds its working-memory reservation through validation and host lowering.
// Callers acquire it before source capture and close it after parsing. A parser
// must not be shared concurrently or used after Close.
type ProgramParser struct {
	ctx     context.Context
	release func()
	once    sync.Once
	closed  bool
	memory  *toolcontract.ProgramMemory
}

func NewProgramParser(ctx context.Context, memory *toolcontract.ProgramMemory) (*ProgramParser, error) {
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
	release, err := memory.Reserve(ctx, languageLimit(catalog, "evaluation_memory_bytes"))
	if err != nil {
		return nil, err
	}
	return &ProgramParser{ctx: ctx, release: release, memory: memory}, nil
}

func (p *ProgramParser) Close() error {
	p.once.Do(func() { p.closed = true; p.release() })
	return nil
}

func (p *ProgramParser) Parse(source []byte, options ParseOptions) (*ParsedProgram, error) {
	if p.closed {
		return nil, fmt.Errorf("program parser reservation is closed")
	}
	return parseProgram(p.ctx, source, options, p.memory)
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
