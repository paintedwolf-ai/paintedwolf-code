package command

import (
	"context"
	"io"
	"sort"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Located writers return coordinates in their own mapping domain. Nested views
// compose those coordinates, including writes interleaved by other children.
type locatedSourceWriter interface {
	writeLocated([]byte) (int, int64, error)
	RecordSourceMapping(toolcontract.StreamSourceMapping) error
}

type outputWriteSpan struct{ local, global, length int64 }

type childStreamView struct {
	target   locatedSourceWriter
	ctx      context.Context
	memory   *toolcontract.ProgramMemory
	copy     io.Writer
	mu       sync.Mutex
	size     int64
	spans    []outputWriteSpan
	release  func()
	disabled bool
}

func newChildStreamView(writer io.Writer) *childStreamView {
	switch source := writer.(type) {
	case *programStream:
		return &childStreamView{target: source, ctx: source.ctx, memory: source.mappings.memory}
	case *childStreamView:
		return &childStreamView{target: source, ctx: source.ctx, memory: source.memory}
	default:
		return nil
	}
}

func (v *childStreamView) Write(p []byte) (int, error) { n, _, err := v.writeLocated(p); return n, err }

func (v *childStreamView) writeLocated(p []byte) (int, int64, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	local := v.size
	n, start, err := v.target.writeLocated(p)
	if n > 0 {
		if !v.disabled && v.memory != nil {
			span := outputWriteSpan{local: local, global: start, length: int64(n)}
			if len(v.spans) > 0 {
				last := &v.spans[len(v.spans)-1]
				if last.local+last.length == span.local && last.global+last.length == span.global {
					last.length += span.length
				} else {
					v.appendSpan(span)
				}
			} else {
				v.appendSpan(span)
			}
		}
		v.size += int64(n)
		if v.copy != nil {
			copied, copyErr := v.copy.Write(p[:n])
			if err == nil {
				err = copyErr
				if copied != n && err == nil {
					err = io.ErrShortWrite
				}
			}
		}
	}
	return n, local, err
}

func (v *childStreamView) RecordSourceMapping(mapping toolcontract.StreamSourceMapping) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.disabled || v.memory == nil {
		return nil
	}
	first := sort.Search(len(v.spans), func(i int) bool { return v.spans[i].local+v.spans[i].length > mapping.OutputStartByte })
	for _, span := range v.spans[first:] {
		if span.local >= mapping.OutputEndByte {
			break
		}
		start, end := max(span.local, mapping.OutputStartByte), min(span.local+span.length, mapping.OutputEndByte)
		if end <= start {
			continue
		}
		projected := mapping
		projected.OutputStartByte = span.global + start - span.local
		projected.OutputEndByte = projected.OutputStartByte + end - start
		projected.SourceStartByte = mapping.SourceStartByte + start - mapping.OutputStartByte
		projected.SourceEndByte = projected.SourceStartByte + end - start
		if err := v.target.RecordSourceMapping(projected); err != nil {
			return err
		}
	}
	return nil
}

func (v *childStreamView) appendSpan(span outputWriteSpan) {
	if len(v.spans) == cap(v.spans) {
		capacity := max(4, cap(v.spans)*2)
		release, err := v.memory.ReserveProgramState(v.ctx, int64(capacity)*32+256)
		if err != nil {
			v.disable()
			return
		}
		spans := make([]outputWriteSpan, len(v.spans), capacity)
		copy(spans, v.spans)
		if v.release != nil {
			v.release()
		}
		v.spans, v.release = spans, release
	}
	v.spans = append(v.spans, span)
}

func (v *childStreamView) disable() {
	if v.release != nil {
		v.release()
		v.release = nil
	}
	v.spans = nil
	v.disabled = true
}
func (v *childStreamView) Close() { v.mu.Lock(); defer v.mu.Unlock(); v.disable() }
