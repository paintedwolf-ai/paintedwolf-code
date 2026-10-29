package mutation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// invocation is one utility run: its bound argv, streams and effects so far.
type invocation struct {
	ctx     context.Context
	owners  *Owners
	tc      tools.ToolContext
	paths   *projectpaths.ProgramPaths
	barrier *native.PublicationBarrier
	request commandprogram.OperationRequest
	spec    toolcontract.CommandSpecification
	argv    commandprogram.BoundArgv
	input   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	status  int
	// effects counts mutations that committed, so a later stop reports them.
	effects int
}

func newInvocation(ctx context.Context, p Prepared, tc tools.ToolContext, streams commandprogram.Streams) *invocation {
	inv := &invocation{ctx: ctx, owners: p.owners, tc: tc, paths: p.program.Paths, barrier: p.program.Barrier, request: p.request, spec: p.spec, argv: p.Argv,
		input: streams.Input, stdout: streams.Output, stderr: streams.Error}
	if inv.input == nil {
		inv.input = commandprogram.NullInput()
	}
	if inv.stdout == nil {
		inv.stdout = io.Discard
	}
	if inv.stderr == nil {
		inv.stderr = io.Discard
	}
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

// fail reports one operand's ordinary failure as the utility would and keeps
// going; the final status records it.
func (inv *invocation) fail(operand, reason string) {
	_, _ = fmt.Fprintf(inv.stderr, "%s: %s: %s\n", inv.name(), operand, reason)
	inv.status = max(inv.status, 1)
}

func (inv *invocation) usage(word, reason string) error {
	return &toolrejection.ToolReject{Code: "COMMAND_OPERAND_INVALID", Data: map[string]any{
		"command": inv.name(), "word": word, "expected": reason, "reason": reason, "expected_form": inv.spec.Argv}}
}

// unsupportedForm refuses a catalogued spelling this frontend cannot perform
// honestly; it never approximates it or falls back to an installed binary.
func (inv *invocation) unsupportedForm(word, reason string) error {
	return &toolrejection.ToolReject{Code: "COMMAND_NATIVE_FORM_UNSUPPORTED", Data: map[string]any{
		"command": inv.name(), "word": word, "expected": reason, "expected_form": inv.spec.Argv}}
}

// bind resolves one operand through the invocation cwd under mutation or
// read scope and returns the absolute address the owner re-resolves.
func (inv *invocation) bind(operand string, write bool) (projectpaths.BoundPath, error) {
	bound, err := inv.paths.Bind(inv.ctx, operand, write)
	if err != nil {
		return projectpaths.BoundPath{}, err
	}
	if bound.Null {
		return projectpaths.BoundPath{}, inv.usage(operand, "a filesystem path, not the null device")
	}
	return bound, nil
}

// recordPin retains one capture for this execution before its effect.
func (inv *invocation) recordPin(pin toolcontract.ExecutionRevision) error {
	return inv.owners.Pins.AppendRevisionPin(inv.ctx, inv.request.Invocation.Identity.ExecutionID, pin)
}

// ordinaryCodes are owner refusals the utilities report on stderr and survive.
var ordinaryCodes = map[string]string{
	"DELETE_NOT_FOUND": "No such file or directory", "MOVE_NOT_FOUND": "No such file or directory", "COPY_NOT_FOUND": "No such file or directory",
	"READ_PATH_NOT_FOUND": "No such file or directory", "DELETE_NOT_EMPTY": "Directory not empty", "DELETE_IS_DIRECTORY": "Is a directory",
	"MOVE_IS_DIRECTORY": "Is a directory", "COPY_IS_DIRECTORY": "Is a directory", "MKDIR_FILE_EXISTS": "File exists",
}

// ordinary classifies an operand failure the utility survives. Authority,
// review, budget and conflict refusals stay typed stops.
func ordinary(err error) (string, bool) {
	if reject := toolrejection.AsToolReject(err); reject != nil {
		reason, ok := ordinaryCodes[reject.Code]
		return reason, ok
	}
	var pathErr *projectpaths.PathError
	if errors.As(err, &pathErr) {
		return "", false
	}
	if errors.Is(err, os.ErrNotExist) {
		return "No such file or directory", true
	}
	return "", false
}

// settle turns the utility's ordinary status or stop into one producer result.
func (inv *invocation) settle(err error) (toolcontract.ProducerResult, error) {
	certainty := toolcontract.NoEffect
	if inv.effects > 0 {
		certainty = toolcontract.EffectCompleted
	}
	switch {
	case err == nil:
		return toolcontract.ProducerResult{Status: inv.status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: inv.status == 0, EffectCertainty: certainty}, nil
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		if certainty == toolcontract.NoEffect {
			certainty = toolcontract.EffectUnknown
		}
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: certainty}, err
	}
	if reject := toolrejection.AsToolReject(err); reject != nil {
		if reject.Data == nil {
			reject.Data = map[string]any{}
		}
		if reject.Data["command"] == nil {
			reject.Data["command"] = inv.name()
		}
		_, _ = fmt.Fprintf(inv.stderr, "%s: %s [%s]\n", inv.name(), reject.Error(), reject.Code)
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: reject.Code, EffectCertainty: certainty}, err
	}
	var pathErr *projectpaths.PathError
	if errors.As(err, &pathErr) {
		err = wrapPathError(inv.name(), err)
		_, _ = fmt.Fprintf(inv.stderr, "%s: %s [%s]\n", inv.name(), pathErr.Error(), pathErr.Code)
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: pathErr.Code, EffectCertainty: certainty}, err
	}
	if certainty == toolcontract.NoEffect {
		certainty = toolcontract.EffectUnknown
	}
	return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: certainty}, err
}

// destinationEntry resolves a copy or move destination: an existing directory
// receives the source's base name; several sources require one.
// destinationDisplay names a destination entry as the program wrote DEST.
func (inv *invocation) destinationDisplay(dest, to string) string {
	bound, err := inv.paths.Bind(inv.ctx, dest, true)
	if err != nil {
		return to
	}
	return destinationDisplay(dest, bound.Resolved.Abs, to)
}

func (inv *invocation) destinationEntry(dest string, source string, many bool) (string, error) {
	bound, err := inv.bind(dest, true)
	if err != nil {
		return "", err
	}
	info, statErr := os.Stat(bound.Resolved.Abs)
	if statErr == nil && info.IsDir() {
		return filepath.Join(bound.Resolved.Abs, filepath.Base(source)), nil
	}
	if many {
		return "", inv.usage(dest, "an existing directory for several sources")
	}
	return bound.Resolved.Abs, nil
}
