package search

import (
	"bytes"
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

type grepEventSink struct {
	ctx     context.Context
	events  chan<- grepFileEvent
	buffer  []byte
	stderr  bool
	started bool
	buffers *grepBuffers
}

type grepFileWriter struct {
	sink   *grepEventSink
	stderr bool
}

func (w *grepFileWriter) close() {
	if w.sink.buffer != nil {
		w.sink.buffers.put(w.sink.buffer)
		w.sink.buffer = nil
	}
}

func (w *grepFileWriter) errorWriter() *grepFileWriter {
	return &grepFileWriter{sink: w.sink, stderr: true}
}

func (w *grepFileWriter) send(event grepFileEvent) error {
	if err := w.flush(); err != nil {
		return err
	}
	return w.sink.publish(event)
}

func (s *grepEventSink) publish(event grepFileEvent) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case s.events <- event:
		return nil
	}
}

func (w *grepFileWriter) flush() error {
	if len(w.sink.buffer) == 0 {
		return w.sink.ctx.Err()
	}
	event := grepFileEvent{bytes: w.sink.buffer, stderr: w.sink.stderr}
	w.sink.buffer = nil
	if !event.stderr {
		w.sink.started = true
	}
	if err := w.sink.publish(event); err != nil {
		w.sink.buffers.put(event.bytes)
		return err
	}
	return nil
}

func (w *grepFileWriter) Write(raw []byte) (int, error) {
	if err := w.sink.ctx.Err(); err != nil {
		return 0, err
	}
	if w.stderr != w.sink.stderr {
		if err := w.flush(); err != nil {
			return 0, err
		}
		w.sink.stderr = w.stderr
	}
	written := 0
	for len(raw) > 0 {
		if w.sink.buffer == nil {
			var err error
			w.sink.buffer, err = w.sink.buffers.get(w.sink.ctx)
			if err != nil {
				return written, err
			}
		}
		n := min(len(raw), grepOutputChunk-len(w.sink.buffer))
		w.sink.buffer = append(w.sink.buffer, raw[:n]...)
		raw, written = raw[n:], written+n
		if len(w.sink.buffer) == grepOutputChunk || (!w.sink.started || w.stderr) && bytes.ContainsAny(w.sink.buffer, "\n\x00") {
			if err := w.flush(); err != nil {
				return written, err
			}
		}
	}
	return written, nil
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
