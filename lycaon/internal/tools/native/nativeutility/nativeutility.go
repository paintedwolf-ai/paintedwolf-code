// Package nativeutility holds the settlement and refusal shapes shared by the
// in-process command utilities, so every frontend reports the same producer
// outcome and catalog rejection for the same fact.
package nativeutility

import (
	"context"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// Settle projects one utility run onto the producer contract: a typed
// rejection is a refusal stop, cancellation an interruption, any other error a
// failure, and an ordinary status completes. comparison marks a complete
// status-one result (no match, differences) as publishable.
func Settle(err error, status int, comparison bool) toolcontract.ProducerResult {
	if err == nil {
		result := toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}
		switch {
		case status == 0:
			result.PublicationEligible = true
		case comparison && status == 1:
			result.PublicationEligible, result.PublicationBasis = true, toolcontract.NativeComparisonPublication
		}
		return result
	}
	result := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
	if reject := toolrejection.AsToolReject(err); reject != nil {
		result.Status, result.Terminal, result.StopCode = 2, toolcontract.ProducerRefused, reject.Code
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		result.Status, result.Terminal, result.StopCode = 130, toolcontract.ProducerInterrupted, ""
	}
	return result
}

// ArgvRejection maps a catalog binder refusal to its code with the word span.
func ArgvRejection(err error, spec toolcontract.CommandSpecification) error {
	var argvErr *commandprogram.ArgvError
	if !errors.As(err, &argvErr) {
		return err
	}
	return errors.Join(err, toolrejection.RejectInvalidArguments(argvErr.Code, map[string]any{
		"command": spec.Name, "word": argvErr.Word, "expected": argvErr.Expected, "expected_form": spec.Argv,
		"supported_options": toolcontract.CommandSpellings(spec),
		"source_start_byte": argvErr.Span.StartByte, "source_end_byte": argvErr.Span.EndByte, "source_line": argvErr.Span.Line, "source_column": argvErr.Span.Column,
	}))
}

// Usage refuses a form the catalog admits syntactically but whose word is invalid.
func Usage(spec toolcontract.CommandSpecification, word commandprogram.Field, expected string) error {
	return toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{
		"command": spec.Name, "word": word.Value, "expected": expected, "reason": expected, "expected_form": spec.Argv,
		"source_start_byte": word.Span.StartByte, "source_end_byte": word.Span.EndByte, "source_line": word.Span.Line, "source_column": word.Span.Column,
	})
}

const outputChunkBytes = 32 * 1024

// WriteChunked meters writes against the named catalog limit and flushes in 32 KiB chunks.
func WriteChunked(ctx context.Context, w io.Writer, limitID string, maximum int64, written *int64, p []byte) (int, error) {
	if maximum > 0 && *written+int64(len(p)) > maximum {
		return 0, tools.CommandLimitExceeded(limitID, maximum, *written+int64(len(p)))
	}
	sent := 0
	for len(p) > 0 {
		if err := ctx.Err(); err != nil {
			return sent, err
		}
		chunk := p[:min(len(p), outputChunkBytes)]
		n, err := w.Write(chunk)
		if n < 0 || n > len(chunk) {
			return sent, io.ErrShortWrite
		}
		sent += n
		*written += int64(n)
		if err != nil {
			return sent, err
		}
		if n != len(chunk) {
			return sent, io.ErrShortWrite
		}
		p = p[n:]
	}
	return sent, nil
}

// Unsupported refuses a catalogued form this host does not run here.
func Unsupported(spec toolcontract.CommandSpecification, word commandprogram.Field, expected string) error {
	return toolrejection.RejectInvalidArguments("COMMAND_NATIVE_FORM_UNSUPPORTED", map[string]any{
		"command": spec.Name, "word": word.Value, "expected": expected, "expected_form": spec.Argv,
		"source_start_byte": word.Span.StartByte, "source_end_byte": word.Span.EndByte, "source_line": word.Span.Line, "source_column": word.Span.Column,
	})
}

// Output meters one stdout endpoint against stream_bytes before each write and
// records exact source mappings when the endpoint retains them. Bytes already
// written stay written; a refusal makes the result incomplete.
type Output struct {
	ctx              context.Context
	w                io.Writer
	mappings         commandprogram.SourceMappingRecorder
	pending          *toolcontract.StreamSourceMapping
	maximum, written int64
	limitID          string
}

func NewOutput(ctx context.Context, w io.Writer, limitID string, maximum int64) *Output {
	if w == nil {
		w = io.Discard
	}
	o := &Output{ctx: ctx, w: w, limitID: limitID, maximum: maximum}
	o.mappings, _ = w.(commandprogram.SourceMappingRecorder)
	return o
}

func (o *Output) Write(p []byte) (int, error) { return o.writeTo(o.w, p) }

func (o *Output) writeTo(w io.Writer, p []byte) (int, error) {
	if err := o.ctx.Err(); err != nil {
		return 0, err
	}
	if o.written+int64(len(p)) > o.maximum {
		return 0, tools.CommandLimitExceeded(o.limitID, o.maximum, o.written+int64(len(p)))
	}
	n, err := w.Write(p)
	o.written += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

// WriteLiteral writes bytes taken verbatim from a pinned source at offset and
// maps them when the endpoint retains mappings. Adjacent windows of one source
// coalesce into a single mapping.
func (o *Output) WriteLiteral(pin *toolcontract.SourcePin, p []byte, offset int64) error {
	if len(p) == 0 {
		return nil
	}
	outStart := o.written
	if _, err := o.Write(p); err != nil {
		return err
	}
	if o.mappings == nil || pin == nil {
		return nil
	}
	next := toolcontract.StreamSourceMapping{Source: *pin, SourceStartByte: offset, SourceEndByte: offset + int64(len(p)), OutputStartByte: outStart, OutputEndByte: o.written}
	if last := o.pending; last != nil && last.Source == next.Source && last.SourceEndByte == next.SourceStartByte && last.OutputEndByte == next.OutputStartByte {
		last.SourceEndByte, last.OutputEndByte = next.SourceEndByte, next.OutputEndByte
		return nil
	}
	if err := o.Flush(); err != nil {
		return err
	}
	o.pending = &next
	return nil
}

// Flush records the mapping still being extended.
func (o *Output) Flush() error {
	if o.pending == nil {
		return nil
	}
	pending := *o.pending
	o.pending = nil
	return o.mappings.RecordSourceMapping(pending)
}

// Written reports the bytes accepted so far.
func (o *Output) Written() int64 { return o.written }

// RecordSourceMapping forwards source-mapping spans when the underlying writer
// records them.
func (o *Output) RecordSourceMapping(m toolcontract.StreamSourceMapping) error {
	if recorder, ok := o.w.(commandprogram.SourceMappingRecorder); ok {
		return recorder.RecordSourceMapping(m)
	}
	return nil
}

// WithActivity preserves this output's meter while rebinding its stream endpoint.
func (o *Output) WithActivity(activity *commandprogram.ExecutionWait) io.Writer {
	streams := (commandprogram.Streams{Output: o.w}).WithActivity(activity)
	return activityOutput{output: o, writer: streams.Output}
}

type activityOutput struct {
	output *Output
	writer io.Writer
}

func (o activityOutput) Write(data []byte) (int, error) { return o.output.writeTo(o.writer, data) }

func (o activityOutput) RecordSourceMapping(mapping toolcontract.StreamSourceMapping) error {
	return o.output.RecordSourceMapping(mapping)
}

func (o activityOutput) WithActivity(activity *commandprogram.ExecutionWait) io.Writer {
	return o.output.WithActivity(activity)
}
