package query

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

// invocation is one utility run: its bound argv, streams, pins and effects.
type invocation struct {
	ctx     context.Context
	owners  *Owners
	tc      tools.ToolContext
	paths   *projectpaths.ProgramPaths
	request commandprogram.OperationRequest
	spec    toolcontract.CommandSpecification
	argv    commandprogram.BoundArgv
	input   io.Reader
	stderr  io.Writer
	out     *output
	status  int
	// published reports that a write reached the destination owner.
	published bool
	releases  []func()
}

func newInvocation(ctx context.Context, p Prepared, tc tools.ToolContext, streams commandprogram.Streams) *invocation {
	inv := &invocation{ctx: ctx, owners: p.owners, tc: tc, paths: p.program.Paths, request: p.request, spec: p.spec, argv: p.Argv,
		input: streams.Input, stderr: streams.Error}
	if inv.input == nil {
		inv.input = commandprogram.NullInput()
	}
	if inv.stderr == nil {
		inv.stderr = io.Discard
	}
	inv.out = &output{ctx: ctx, w: streams.Output, maximum: p.owners.limit("stream_throughput_bytes"), limitID: "stream_throughput_bytes", maxSteps: p.owners.limit("query_work_steps")}
	if inv.out.w == nil {
		inv.out.w = io.Discard
	}
	return inv
}

func (inv *invocation) step() error {
	inv.out.steps++
	if inv.out.maxSteps > 0 && inv.out.steps > inv.out.maxSteps {
		return tools.CommandLimitExceeded("query_work_steps", inv.out.maxSteps, inv.out.steps)
	}
	return nil
}

func (inv *invocation) name() string { return inv.spec.Name }

func (inv *invocation) flag(key string) bool {
	_, ok := inv.argv.Last(key)
	return ok
}

func (inv *invocation) value(key string) (string, bool) {
	field, ok := inv.valueField(key)
	return field.Value, ok
}

func (inv *invocation) valueField(key string) (commandprogram.Field, bool) {
	option, ok := inv.argv.Last(key)
	if !ok || len(option.Values) == 0 {
		return commandprogram.Field{}, false
	}
	return option.Values[0], true
}

func (inv *invocation) usage(word, reason string) error {
	return &toolrejection.ToolReject{Code: "COMMAND_OPERAND_INVALID", Data: map[string]any{
		"command": inv.name(), "word": word, "expected": reason, "reason": reason, "expected_form": inv.spec.Argv}}
}

// fail reports an ordinary input or query failure on stderr: jq's status 2.
func (inv *invocation) fail(format string, args ...any) {
	_, _ = fmt.Fprintf(inv.stderr, "%s: error: "+format+"\n", append([]any{inv.name()}, args...)...)
	inv.status = 2
}

// ordinary classifies an input failure the utility reports and survives.
func ordinary(err error) (string, bool) {
	if reject := toolrejection.AsToolReject(err); reject != nil {
		switch reject.Code {
		case "READ_PATH_NOT_FOUND":
			return "No such file or directory", true
		case "READ_IS_DIRECTORY":
			return "Is a directory", true
		}
		return "", false
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "No such file or directory", true
	case errors.Is(err, fs.ErrPermission):
		return "Permission denied", true
	}
	return "", false
}

// source is one captured file operand's text and its pin.
type source struct {
	label, abs, text string
	encoding         string
	revision         toolcontract.ExecutionRevision
}

// unreadable is an operand the utility reports as an ordinary failure.
type unreadable struct {
	operand, reason string
}

func (e *unreadable) Error() string { return e.operand + ": " + e.reason }

// capture pins one PATH operand's published text for jq. An unreadable
// operand is reported as jq does and returns nil.
func (inv *invocation) capture(operand commandprogram.Field) (*source, error) {
	captured, err := inv.captureSource(operand)
	var failure *unreadable
	if errors.As(err, &failure) {
		inv.fail("Could not open file %s: %s", operand.Value, failure.reason)
		return nil, nil
	}
	return captured, err
}

// captureSource pins one PATH operand's accepted text before it is read.
func (inv *invocation) captureSource(operand commandprogram.Field) (*source, error) {
	if reader, ok := operand.ProcessSubstitution().OpenReader(); ok {
		maximum := safecmd.JQCaps().InputBytes
		data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
		_ = reader.Close()
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > maximum {
			return nil, safecmd.Reject("JQ_INPUT_TOO_LARGE", map[string]any{"path": operand.Value, "max_bytes": maximum})
		}
		return &source{label: operand.Value, text: string(data)}, nil
	}
	bound, err := inv.paths.Bind(inv.ctx, operand.Value, false)
	if err == nil && bound.Null {
		return &source{label: operand.Value}, nil
	}
	var captured native.ExecutionSource
	if err == nil {
		if tools.IsSensitivePath(bound.Resolved.DisplayPath) {
			return nil, safecmd.Reject("JQ_PATH_DENIED", map[string]any{"path": bound.Resolved.DisplayPath})
		}
		captured, err = inv.owners.Revisions.CaptureSource(inv.ctx, inv.tc, bound.Resolved.Abs)
	}
	if err != nil {
		err = projectpaths.PathOperationError(operand.Value, err)
		if reason, ok := ordinary(err); ok {
			return nil, &unreadable{operand: operand.Value, reason: reason}
		}
		return nil, err
	}
	inv.releases = append(inv.releases, captured.Close)
	if maximum := safecmd.JQCaps().InputBytes; int64(len(captured.Content)) > maximum {
		return nil, safecmd.Reject("JQ_INPUT_TOO_LARGE", map[string]any{"path": captured.Path, "max_bytes": maximum})
	}
	if err := inv.recordPin(captured.Revision); err != nil {
		return nil, err
	}
	return &source{label: operand.Value, abs: bound.Resolved.Abs, text: captured.Content, encoding: captured.Encoding, revision: captured.Revision}, nil
}

// recordPin records each capture before its bytes are used; the invocation
// keeps no duplicate pin history.
func (inv *invocation) recordPin(revision toolcontract.ExecutionRevision) error {
	execution := inv.request.Invocation.Identity.ExecutionID
	if inv.owners.Pins == nil || execution == "" {
		return fmt.Errorf("query reads require a program execution and pin recorder")
	}
	return inv.owners.Pins.AppendRevisionPin(inv.ctx, execution, revision)
}

// settle projects the run onto the producer contract. A published edit is a
// completed effect even when a later step stops the run.
func (inv *invocation) settle(err error) (toolcontract.ProducerResult, error) {
	var domain toolcontract.ProducerFailure
	if errors.As(err, &domain) {
		return domain.ProducerResult(), err
	}
	err = tools.CommandResourceRejection(err)
	certainty := toolcontract.NoEffect
	if inv.published {
		certainty = toolcontract.EffectCompleted
	}
	err = projectpaths.WithPathRejection(err, inv.request.Invocation.Span)
	if err != nil {
		result := toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: certainty}
		if reject := toolrejection.AsToolReject(err); reject != nil {
			result.Terminal, result.StopCode = toolcontract.ProducerRefused, reject.Code
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Status, result.Terminal, result.StopCode = 130, toolcontract.ProducerInterrupted, ""
		}
		return result, err
	}
	result := toolcontract.ProducerResult{Status: inv.status, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: certainty}
	result.PublicationEligible = inv.status == 0
	return result, nil
}

// output meters stdout against stream_bytes before each write.
type output struct {
	ctx              context.Context
	w                io.Writer
	maximum, written int64
	limitID          string
	steps, maxSteps  int64
}

func (o *output) Write(p []byte) (int, error) {
	if err := o.ctx.Err(); err != nil {
		return 0, err
	}
	if o.maximum > 0 && o.written+int64(len(p)) > o.maximum {
		id := o.limitID
		if id == "" {
			id = "stream_bytes"
		}
		return 0, tools.CommandLimitExceeded(id, o.maximum, o.written+int64(len(p)))
	}
	n, err := o.w.Write(p)
	o.written += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}
