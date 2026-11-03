package command

import (
	"context"
	"math"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Source attribution is optional. Its retained capacity shares program state;
// losing admission removes attribution, never the actual command bytes.
type sourceMappingBuffer struct {
	ctx        context.Context
	memory     *toolcontract.ProgramMemory
	values     []toolcontract.StreamSourceMapping
	maxStrings int64
	release    func()
	disabled   bool
}

func newSourceMappingBuffer(ctx context.Context, memory *toolcontract.ProgramMemory) *sourceMappingBuffer {
	return &sourceMappingBuffer{ctx: ctx, memory: memory, disabled: memory == nil}
}

func (b *sourceMappingBuffer) Append(m toolcontract.StreamSourceMapping) {
	if b.disabled || m.OutputEndByte <= m.OutputStartByte || m.SourceEndByte <= m.SourceStartByte {
		return
	}
	if n := len(b.values); n > 0 {
		last := &b.values[n-1]
		if last.Source == m.Source && last.OutputEndByte == m.OutputStartByte && last.SourceEndByte == m.SourceStartByte {
			last.OutputEndByte, last.SourceEndByte = m.OutputEndByte, m.SourceEndByte
			return
		}
	}
	stringsBytes := int64(len(m.Source.SourceID)) + int64(len(m.Source.Revision)) + int64(len(m.Source.Representation)) + int64(len(m.Source.SHA256))
	capacity := cap(b.values)
	if len(b.values) == capacity {
		capacity = max(4, capacity*2)
	}
	maxStrings := max(b.maxStrings, stringsBytes)
	if capacity != cap(b.values) || maxStrings != b.maxStrings {
		if maxStrings > math.MaxInt64-256 || int64(capacity) > math.MaxInt64/(256+maxStrings) {
			b.Close()
			return
		}
		release, err := b.memory.ReserveProgramState(b.ctx, int64(capacity)*(256+maxStrings))
		if err != nil {
			b.Close()
			return
		}
		values := make([]toolcontract.StreamSourceMapping, len(b.values), capacity)
		copy(values, b.values)
		if b.release != nil {
			b.release()
		}
		b.values, b.release, b.maxStrings = values, release, maxStrings
	}
	m.Source.SourceID = strings.Clone(m.Source.SourceID)
	m.Source.Revision = strings.Clone(m.Source.Revision)
	m.Source.Representation = toolcontract.SourceRepresentation(strings.Clone(string(m.Source.Representation)))
	m.Source.SHA256 = strings.Clone(m.Source.SHA256)
	b.values = append(b.values, m)
}

func (b *sourceMappingBuffer) Snapshot(ctx context.Context, length int64) (*commandprogram.MappingLease, error) {
	if b.disabled {
		return &commandprogram.MappingLease{}, nil
	}
	return commandprogram.ProjectStreamMappings(ctx, b.memory, b.values, 0, length, commandprogram.MappingExactSlice)
}

func (b *sourceMappingBuffer) Close() {
	if b.release != nil {
		b.release()
		b.release = nil
	}
	b.values = nil
	b.disabled = true
}
