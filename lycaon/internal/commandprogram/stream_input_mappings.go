package commandprogram

import (
	"context"
	"io"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// LocatedWriter reports where each write starts in its stream, so a nested
// writer can rebase the mappings it records into stream coordinates.
type LocatedWriter interface {
	io.Writer
	SourceMappingRecorder
	WriteLocated(p []byte) (n int, start int64, err error)
	MappingAdmission() (context.Context, *toolcontract.ProgramMemory)
}

// MappedInput is a piped stdin whose producer recorded exact source mappings.
type MappedInput interface {
	// SettledSourceMappings stops consumption, waits for the producer to settle
	// and returns its mappings in stream coordinates.
	SettledSourceMappings(ctx context.Context) ([]toolcontract.StreamSourceMapping, error)
}

type inputSpan struct{ input, output, length int64 }

// InputProjection carries a producer's mappings through a stage that copies
// exact slices of its stdin. Spans are input and output byte ranges of equal
// length; everything else the stage writes stays unmapped.
type InputProjection struct {
	ctx      context.Context
	input    MappedInput
	memory   *toolcontract.ProgramMemory
	spans    []inputSpan
	release  func()
	disabled bool
}

// NewInputProjection returns nil when input carries no mappings.
func NewInputProjection(ctx context.Context, input io.Reader, memory *toolcontract.ProgramMemory) *InputProjection {
	mapped, ok := input.(MappedInput)
	if !ok || memory == nil || ctx == nil {
		return nil
	}
	return &InputProjection{ctx: ctx, input: mapped, memory: memory}
}

// Copied records that output bytes [output, output+length) equal input bytes
// [input, input+length).
func (p *InputProjection) Copied(input, output, length int64) {
	if p == nil || p.disabled || length <= 0 || input < 0 || output < 0 {
		return
	}
	if n := len(p.spans); n > 0 {
		last := &p.spans[n-1]
		if last.input+last.length == input && last.output+last.length == output {
			last.length += length
			return
		}
	}
	if len(p.spans) == cap(p.spans) {
		capacity := max(8, cap(p.spans)*2)
		release, err := p.memory.ReserveProgramState(p.ctx, int64(capacity)*24+256)
		if err != nil {
			p.close()
			p.disabled = true
			return
		}
		spans := make([]inputSpan, len(p.spans), capacity)
		copy(spans, p.spans)
		if p.release != nil {
			p.release()
		}
		p.spans, p.release = spans, release
	}
	p.spans = append(p.spans, inputSpan{input: input, output: output, length: length})
}

// Record waits for the producer's mappings and records their projection
// through the copied spans. Call it after the stage's last copy.
func (p *InputProjection) Record(recorder SourceMappingRecorder) error {
	if p == nil {
		return nil
	}
	defer p.close()
	if p.disabled || len(p.spans) == 0 || recorder == nil {
		return nil
	}
	mappings, err := p.input.SettledSourceMappings(p.ctx)
	if err != nil || len(mappings) == 0 {
		return err
	}
	for _, span := range p.spans {
		lease, err := ProjectStreamMappings(p.ctx, p.memory, mappings, span.input, span.length, MappingExactSlice)
		if err != nil {
			return err
		}
		for _, m := range lease.Mappings {
			m.OutputStartByte += span.output
			m.OutputEndByte += span.output
			if err := recorder.RecordSourceMapping(m); err != nil {
				_ = lease.Close()
				return err
			}
		}
		_ = lease.Close()
	}
	return nil
}

func (p *InputProjection) close() {
	if p.release != nil {
		p.release()
		p.release = nil
	}
	p.spans = nil
}
