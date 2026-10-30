package search

import (
	"context"
	"io"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

type grepFileEvent struct {
	bytes   []byte
	stderr  bool
	mapping *toolcontract.StreamSourceMapping
	pins    []toolcontract.ExecutionRevision
}

type grepFileWriter struct {
	ctx    context.Context
	events chan<- grepFileEvent
	stderr bool
}

func (w *grepFileWriter) errorWriter() *grepFileWriter {
	return &grepFileWriter{ctx: w.ctx, events: w.events, stderr: true}
}

func (w *grepFileWriter) send(event grepFileEvent) error {
	select {
	case <-w.ctx.Done():
		return w.ctx.Err()
	case w.events <- event:
		return nil
	}
}

func (w *grepFileWriter) Write(raw []byte) (int, error) {
	written := 0
	for len(raw) > 0 {
		n := min(len(raw), grepOutputChunk)
		part := append([]byte(nil), raw[:n]...)
		if err := w.send(grepFileEvent{bytes: part, stderr: w.stderr}); err != nil {
			return written, err
		}
		raw, written = raw[n:], written+n
	}
	return written, w.ctx.Err()
}

func (w *grepFileWriter) RecordSourceMapping(mapping toolcontract.StreamSourceMapping) error {
	return w.send(grepFileEvent{mapping: &mapping})
}

func (p *grepFiles) apply(event grepFileEvent, base int64) error {
	inv := p.run.inv
	for _, pin := range event.pins {
		if err := inv.recordPin(pin); err != nil {
			return err
		}
	}
	if event.mapping != nil && inv.out.mappings != nil {
		mapping := *event.mapping
		mapping.OutputStartByte += base
		mapping.OutputEndByte += base
		return inv.out.mappings.RecordSourceMapping(mapping)
	}
	if event.stderr {
		n, err := inv.stderr.Write(event.bytes)
		if err == nil && n != len(event.bytes) {
			err = io.ErrShortWrite
		}
		return err
	}
	_, err := inv.out.Write(event.bytes)
	return err
}
