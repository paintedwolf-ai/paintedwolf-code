package command

import (
	"context"
	"io"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type outputStreamClass struct {
	topLevel         bool
	piped            bool
	redirectPath     string
	redirectOperator string
}

type trackedStagedOutput struct {
	commandprogram.StagedOutput
	path     string
	operator string
}

type trackedOutputPublication struct {
	inner commandprogram.OutputPublication
	paths *projectpaths.ProgramPaths
	run   *programRun
}

func (p *trackedOutputPublication) Open(ctx context.Context, destination commandprogram.OutputDestination) (commandprogram.StagedOutput, error) {
	staged, err := p.inner.Open(ctx, destination)
	if err != nil || staged == nil {
		return staged, err
	}
	rawPath := destination.Target.Value
	if p.paths != nil && rawPath != "" {
		if bound, bindErr := p.paths.Bind(ctx, rawPath, true); bindErr == nil && bound.Resolved.DisplayPath != "" {
			rawPath = bound.Resolved.DisplayPath
		}
	}
	return &trackedStagedOutput{
		StagedOutput: staged,
		path:         p.run.normalizeProvenancePath("", rawPath),
		operator:     destination.Redirect.Operator,
	}, nil
}

type trackedInputRedirection struct {
	inner commandprogram.InputRedirection
	paths *projectpaths.ProgramPaths
	run   *programRun
}

func (in *trackedInputRedirection) OpenInput(ctx context.Context, destination commandprogram.InputDestination) (io.ReadCloser, error) {
	rc, err := in.inner.OpenInput(ctx, destination)
	if err != nil || rc == nil {
		return rc, err
	}
	rawPath := destination.Target.Value
	if in.paths != nil && rawPath != "" {
		if bound, bindErr := in.paths.Bind(ctx, rawPath, false); bindErr == nil && bound.Resolved.DisplayPath != "" {
			rawPath = bound.Resolved.DisplayPath
		}
	}
	return &trackedInputReader{
		ReadCloser: rc,
		run:        in.run,
		tracker:    opTrackerFromContext(ctx),
		path:       in.run.normalizeProvenancePath("", rawPath),
	}, nil
}

type trackedInputReader struct {
	io.ReadCloser
	run     *programRun
	tracker *opProvenanceTracker
	path    string
}

func (r *trackedInputReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 && r.run != nil && r.path != "" {
		r.run.noteInputRedirectRead(r.tracker, r.path)
	}
	return n, err
}

func (d *ProgramDispatcher) wrapOutputPublication(pub commandprogram.OutputPublication, paths *projectpaths.ProgramPaths) commandprogram.OutputPublication {
	if pub == nil || d == nil || d.run == nil {
		return pub
	}
	return &trackedOutputPublication{inner: pub, paths: paths, run: d.run}
}

func (d *ProgramDispatcher) wrapInputRedirection(in commandprogram.InputRedirection, paths *projectpaths.ProgramPaths) commandprogram.InputRedirection {
	if in == nil || d == nil || d.run == nil {
		return in
	}
	return &trackedInputRedirection{inner: in, paths: paths, run: d.run}
}

func classifyOutputStream(w io.Writer) outputStreamClass {
	if w == nil || w == io.Discard {
		return outputStreamClass{}
	}
	if newChildStreamView(w) != nil {
		return outputStreamClass{topLevel: true}
	}
	if staged, ok := w.(*trackedStagedOutput); ok {
		return outputStreamClass{redirectPath: staged.path, redirectOperator: staged.operator}
	}
	return outputStreamClass{piped: true}
}

type countingStreamWriter struct {
	w     io.Writer
	bytes atomic.Int64
}

func (c *countingStreamWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.bytes.Add(int64(n))
	}
	return n, err
}

func (c *countingStreamWriter) writeLocated(p []byte) (int, int64, error) {
	if lw, ok := c.w.(locatedSourceWriter); ok {
		n, start, err := lw.writeLocated(p)
		if n > 0 {
			c.bytes.Add(int64(n))
		}
		return n, start, err
	}
	n, err := c.Write(p)
	return n, 0, err
}

func (c *countingStreamWriter) RecordSourceMapping(mapping toolcontract.StreamSourceMapping) error {
	if lw, ok := c.w.(locatedSourceWriter); ok {
		return lw.RecordSourceMapping(mapping)
	}
	return nil
}

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
	case *countingStreamWriter:
		return newChildStreamView(source.w)
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
