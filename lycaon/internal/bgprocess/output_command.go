package bgprocess

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ReadCommandOutput projects the complete window before selecting raw cursors.
func (r *Output) ReadCommandOutput(ctx context.Context, sessionID, handle string, cursor int64) (OutputSnapshot, error) {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return OutputSnapshot{}, err
	}
	snapshot, err := r.ReadRawOutput(sessionID, handle, 0)
	if err != nil {
		return OutputSnapshot{}, err
	}
	proc.publishMu.Lock()
	projection := r.projectWindow(ctx, proc, cursor)
	proc.publishMu.Unlock()
	snapshot.Output.From = cursor
	snapshot.Output.Next = projection.Next
	snapshot.Output.Truncated = projection.Truncated
	snapshot.Output.Chunks = projection.Chunks
	if !projection.Screened && cursor < projection.Next {
		snapshot.Output.Chunks = []OutputChunk{{Cursor: max(cursor, projection.Head), Stream: "combined", Text: captureUnavailableText}}
	}
	return snapshot, nil
}

func (r *Output) screenWindow(ctx context.Context, proc *Process, projector *captureprojection.Projector, raw string) (string, []secretmatch.StreamSpan, func(), error) {
	memory, err := r.screenMemory()
	if err != nil {
		return "", nil, func() {}, err
	}
	out := projectionText{ctx: ctx, memory: memory}
	var spans []secretmatch.StreamSpan
	charged := int64(0)
	release := func() { out.data = nil; memory.ReleaseBytes(charged + out.charged) }
	options := captureprojection.StreamOptions{
		WorkingMemory: memory,
		Policy:        secretmatch.StreamPolicy{ManagedEchoes: proc.originTool == "command" || proc.originTool == "verify" || proc.kind == processKindProgram},
		Bounds:        secretmatch.StreamBounds{InputBytes: 32 << 20, OutputBytes: 32 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 30},
		Observe: func(ctx context.Context, span secretmatch.StreamSpan) error {
			if err := memory.ReserveBytes(ctx, 128); err != nil {
				return err
			}
			charged += 128
			spans = append(spans, span)
			return nil
		},
	}
	_, err = projector.ScreenStream(ctx, processCaptureScope(proc), "capture.process", strings.NewReader(raw), &out, options)
	if err != nil {
		return "", spans, release, err
	}
	if err = memory.ReserveBytes(ctx, int64(len(out.data))); err != nil {
		return "", spans, release, err
	}
	charged += int64(len(out.data))
	return string(out.data), spans, release, nil
}

// projectedChunks assigns each replacement once while keeping source offsets.
func projectedChunks(chunks []OutputChunk, head, cursor int64, raw, safe string, spans []secretmatch.StreamSpan) []OutputChunk {
	var out []OutputChunk
	index, last := 0, -1
	for _, chunk := range chunks {
		start := max(chunk.Cursor, head, cursor)
		end := chunk.Cursor + int64(len(chunk.Text))
		if start >= end {
			continue
		}
		at := start
		var text strings.Builder
		for at < end {
			for index < len(spans) && head+spans[index].InputEnd <= at {
				index++
			}
			if index >= len(spans) || head+spans[index].InputStart >= end {
				text.WriteString(raw[at-head : end-head])
				break
			}
			span := spans[index]
			before := max(at, head+span.InputStart)
			text.WriteString(raw[at-head : before-head])
			if last != index {
				text.WriteString(safe[span.OutputStart:span.OutputEnd])
				last = index
			}
			at = min(end, head+span.InputEnd)
		}
		if text.Len() > 0 {
			out = append(out, OutputChunk{Cursor: start, Stream: chunk.Stream, Text: text.String()})
		}
	}
	return out
}

// projectionText retains screened bytes under the shared publication account.
type projectionText struct {
	ctx     context.Context
	memory  *toolcontract.ProgramMemory
	data    []byte
	charged int64
}

func (w *projectionText) Write(p []byte) (int, error) {
	needed := len(w.data) + len(p)
	if needed > cap(w.data) {
		capacity := max(needed, 2*cap(w.data))
		if err := w.memory.ReserveBytes(w.ctx, int64(capacity)); err != nil {
			return 0, err
		}
		next := make([]byte, len(w.data), capacity)
		copy(next, w.data)
		w.data = next
		w.memory.ReleaseBytes(w.charged)
		w.charged = int64(capacity)
	}
	w.data = append(w.data, p...)
	return len(p), nil
}
