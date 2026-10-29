package literal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// invocation is one utility run: its bound argv, streams and pins.
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
	// comparison marks diff's complete status-one result as publishable.
	comparison bool
	diffVisits int64
	pinMu      sync.Mutex
	pins       []toolcontract.ExecutionRevision
}

func newInvocation(ctx context.Context, p Prepared, tc tools.ToolContext, streams commandprogram.Streams) *invocation {
	inv := &invocation{ctx: ctx, owners: p.owners, tc: tc, paths: p.paths, request: p.request, spec: p.spec, argv: p.Argv, input: streams.Input, stderr: streams.Error}
	if inv.input == nil {
		inv.input = commandprogram.NullInput()
	}
	if inv.stderr == nil {
		inv.stderr = io.Discard
	}
	inv.out = newOutput(ctx, streams.Output, p.owners.streamBytes())
	return inv
}

func (inv *invocation) name() string { return inv.spec.Name }

func (inv *invocation) flag(key string) bool {
	_, ok := inv.argv.Last(key)
	return ok
}

func (inv *invocation) value(key string) (string, bool) {
	option, ok := inv.argv.Last(key)
	if !ok || len(option.Values) == 0 {
		return "", false
	}
	return option.Values[0].Value, true
}

func (inv *invocation) operands() []string { return inv.argv.Operands().Values() }

// fail reports one operand's ordinary failure on stderr and keeps going, as
// the utilities do; the final status records it.
func (inv *invocation) fail(status int, operand, reason string) {
	switch {
	case operand == "":
		_, _ = fmt.Fprintf(inv.stderr, "%s: %s\n", inv.name(), reason)
	case failureForms[inv.name()] != "":
		_, _ = fmt.Fprintf(inv.stderr, "%s: "+failureForms[inv.name()]+"\n", inv.name(), operand, reason)
	default:
		_, _ = fmt.Fprintf(inv.stderr, "%s: %s: %s\n", inv.name(), operand, reason)
	}
	inv.status = max(inv.status, status)
}

// failureForms are the GNU diagnostics that do not use "NAME: OPERAND: REASON".
var failureForms = map[string]string{
	"head": "cannot open '%s' for reading: %s",
	"tail": "cannot open '%s' for reading: %s",
	"ls":   "cannot access '%s': %s",
	"stat": "cannot stat '%s': %s",
}

// usage refuses a form the catalog admits syntactically but whose value is invalid.
func (inv *invocation) usage(word, reason string) error {
	return &toolrejection.ToolReject{Code: "COMMAND_OPERAND_INVALID", Data: map[string]any{
		"command": inv.name(), "word": word, "expected": reason, "reason": reason, "expected_form": inv.spec.Argv,
	}}
}

// limitExceeded refuses work above a frozen catalog limit; nothing is truncated.
func limitExceeded(id string, maximum, observed int64) error {
	return &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": id, "resource_unit": nativeutility.ResourceUnit(id), "maximum": maximum, "observed": observed}}
}

// pinOpened records the published revision of a file a domain owner opens.
func (inv *invocation) pinOpened(resolved projectpaths.Resolved, file *os.File) error {
	revision, ok, err := inv.owners.Revisions.PinOpened(resolved, file)
	if err != nil || !ok {
		return err
	}
	return inv.recordPin(revision)
}

// platformUnavailable refuses metadata this platform cannot observe; it never
// substitutes another representation.
func platformUnavailable(command, capability string) error {
	return &toolrejection.ToolReject{Code: "COMMAND_PLATFORM_UNAVAILABLE", Data: map[string]any{"command": command, "capability": capability}}
}

// ordinary classifies an input failure the utility reports on stderr and
// survives. Authority, budget and platform refusals stay typed stops.
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
	var pathErr *projectpaths.PathError
	if errors.As(err, &pathErr) {
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

// settle projects the run onto the producer contract.
func (inv *invocation) settle(err error) (toolcontract.ProducerResult, error) {
	err = projectpaths.WithPathRejection(err, inv.request.Invocation.Span)
	if err != nil {
		result := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
		if reject := toolrejection.AsToolReject(err); reject != nil {
			result.Status, result.Terminal, result.StopCode = 2, toolcontract.ProducerRefused, reject.Code
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Status, result.Terminal, result.StopCode = 130, toolcontract.ProducerInterrupted, ""
		}
		return result, err
	}
	result := toolcontract.ProducerResult{Status: inv.status, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}
	switch {
	case inv.status == 0:
		result.PublicationEligible = true
	case inv.comparison && inv.status == 1:
		result.PublicationEligible, result.PublicationBasis = true, toolcontract.NativeComparisonPublication
	}
	return result, nil
}

// Invoke prepares and runs one request, settling a binding refusal as the
// producer outcome so callers without a separate review step see one result.
func (o *Owners) Invoke(ctx context.Context, tc tools.ToolContext, paths *projectpaths.ProgramPaths, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	prepared, err := o.Prepare(paths, request)
	if err != nil {
		result := toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
		if reject := toolrejection.AsToolReject(err); reject != nil {
			result.Terminal, result.StopCode = toolcontract.ProducerRefused, reject.Code
		}
		return result, err
	}
	return prepared.InvokeOwner(ctx, tc, streams)
}
