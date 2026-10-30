// Package nativeutility holds the settlement and refusal shapes shared by the
// in-process command utilities, so every frontend reports the same producer
// outcome and catalog rejection for the same fact.
package nativeutility

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
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

// ResourceUnit returns the catalog unit for a resource limit id.
func ResourceUnit(id string) string {
	if catalog, err := toolcontract.LanguageV1(); err == nil {
		for _, limit := range catalog.Language.Limits {
			if limit.ID == id && limit.Unit != "" {
				return limit.Unit
			}
		}
	}
	if strings.HasSuffix(id, "_bytes") {
		return "bytes"
	}
	return "items"
}

// LimitExceeded refuses work above a frozen catalog limit; nothing is truncated.
func LimitExceeded(id string, maximum, observed int64) error {
	return toolrejection.RejectInvalidArguments("COMMAND_LIMIT_EXCEEDED", map[string]any{"limit_id": id, "resource_unit": ResourceUnit(id), "maximum": maximum, "observed": observed})
}

const outputChunkBytes = 32 * 1024

// WriteChunked meters writes against stream_bytes and flushes in 32 KiB chunks.
func WriteChunked(ctx context.Context, w io.Writer, maximum int64, written *int64, p []byte) (int, error) {
	if maximum > 0 && *written+int64(len(p)) > maximum {
		return 0, LimitExceeded("stream_bytes", maximum, *written+int64(len(p)))
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

// Output meters one stdout endpoint against stream_bytes before each write.
// Bytes already written stay written; a refusal makes the result incomplete.
type Output struct {
	ctx              context.Context
	w                io.Writer
	maximum, written int64
}

func NewOutput(ctx context.Context, w io.Writer, maximum int64) *Output {
	if w == nil {
		w = io.Discard
	}
	return &Output{ctx: ctx, w: w, maximum: maximum}
}

func (o *Output) Write(p []byte) (int, error) {
	if err := o.ctx.Err(); err != nil {
		return 0, err
	}
	if o.written+int64(len(p)) > o.maximum {
		return 0, LimitExceeded("stream_bytes", o.maximum, o.written+int64(len(p)))
	}
	n, err := o.w.Write(p)
	o.written += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

// Written reports the bytes accepted so far.
func (o *Output) Written() int64 { return o.written }
