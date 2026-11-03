package projectpaths

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// Invoke binds path utility operands after normal per-child operation admission.
// It does not run a shell, call a JSON tool, or inherit authority from stdout.
func (p *ProgramPaths) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	err := request.Validate()
	status := 0
	if err == nil {
		status, err = p.commandOutput(ctx, request.OperationID, request.Argv, streams)
	}
	var utility *UtilityError
	if errors.As(err, &utility) {
		// An ordinary utility failure is the utility's own stderr and status.
		_, writeErr := io.WriteString(nonNilWriter(streams.Error), request.OperationID+": "+utility.Error()+"\n")
		return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, writeErr
	}
	err = WithPathRejection(err, request.Invocation.Span)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		result := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
		var reject *toolrejection.ToolReject
		if errors.As(err, &reject) {
			result.Status = 2
			result.Terminal = toolcontract.ProducerRefused
			result.StopCode = reject.Code
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Terminal = toolcontract.ProducerInterrupted
		}
		return result, err
	}
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: status == 0, EffectCertainty: toolcontract.NoEffect}, nil
}

// commandOutput runs one path utility and returns its ordinary status;
// per-operand failures are already on stderr.
func (p *ProgramPaths) commandOutput(ctx context.Context, name string, argv commandprogram.Fields, streams commandprogram.Streams) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	for _, arg := range argv[1:] {
		if int64(len(arg.Value)) > p.argumentLimit {
			return 0, pathLimit("argument_bytes", "bytes", p.argumentLimit, int64(len(arg.Value)))
		}
		if strings.IndexByte(arg.Value, 0) >= 0 {
			return 0, invalidOperand(arg.Value, "NUL in scalar operand")
		}
	}
	spec, ok := p.specs[name]
	if !ok {
		return 0, invalidOperand(name, "not a path operation")
	}
	bound, err := commandprogram.CatalogBinder{}.Bind(spec, argv)
	var argvErr *commandprogram.ArgvError
	if errors.As(err, &argvErr) {
		return 0, &PathError{Code: argvErr.Code, Operand: argvErr.Word, Command: name, ExpectedForm: spec.Argv, Cause: errors.New(argvErr.Expected)}
	}
	if err != nil {
		return 0, err
	}
	output := nonNilWriter(streams.Output)
	if bound.Help {
		_, err := commandprogram.WriteCatalogHelp(spec, output)
		return 0, err
	}
	args := bound.Operands().Values()
	usage := func(reason string) error {
		return &PathError{Code: "COMMAND_OPERAND_INVALID", Operand: name, Command: name, ExpectedForm: spec.Argv, Cause: errors.New(reason)}
	}
	if name == "native_path" {
		if !bound.EndOfOptions || len(args) == 0 || !operandsFollowEnd(argv) {
			return 0, usage("expected [-0] -- PATH ...")
		}
		_, nul := bound.Last("-0")
		return 0, p.NativePaths(ctx, args, nul, output)
	}
	output = newPathOutput(ctx, output, p.outputLimit)
	switch name {
	case "basename":
		return 0, basenameOutput(bound, args, output, usage)
	case "dirname":
		if len(args) != 1 {
			return 0, usage("expected one PATH")
		}
		_, err := io.WriteString(output, PathDir(args[0])+"\n")
		return 0, err
	case "pwd":
		if len(args) != 0 {
			return 0, usage("pwd accepts no operands")
		}
		return 0, p.printWorkingDirectory(ctx, physicalForm(bound), output)
	case "cd":
		if len(args) > 1 {
			return 0, usage("cd accepts at most one PATH")
		}
		operand := ""
		if len(args) == 1 {
			operand = args[0]
		}
		return 0, p.changeDirectory(ctx, operand, physicalForm(bound), output)
	case "realpath":
		if len(args) == 0 {
			return 0, usage("expected at least one PATH")
		}
		return p.realpathOutput(ctx, bound, args, output, nonNilWriter(streams.Error))
	case "readlink":
		if len(args) == 0 {
			return 0, usage("expected at least one PATH")
		}
		return p.readlinkOutput(ctx, bound, args, output)
	}
	return 0, invalidOperand(name, "not a path operation")
}

// WithPathRejection joins a catalog path diagnostic with its public rejection,
// located at the invocation span. Other errors are returned unchanged.
func WithPathRejection(err error, span toolcontract.SourceSpan) error {
	var pathErr *PathError
	if !errors.As(err, &pathErr) {
		return err
	}
	data := map[string]any{"path": pathErr.Operand, "reason": pathErr.Cause.Error(), "source_start_byte": span.StartByte, "source_end_byte": span.EndByte, "source_line": span.Line, "source_column": span.Column, "limit_id": pathErr.LimitID, "maximum": pathErr.Maximum, "observed": pathErr.Observed, "command": pathErr.Command, "expected_form": pathErr.ExpectedForm, "resource_unit": pathErr.ResourceUnit}
	return errors.Join(err, &toolrejection.ToolReject{Code: pathErr.Code, Data: data})
}

// operandsFollowEnd reports that every operand follows the "--" native_path
// requires; its only option takes no value, so the first "--" ends options.
func operandsFollowEnd(argv commandprogram.Fields) bool {
	for _, word := range argv[1:] {
		switch word.Value {
		case "--":
			return true
		case "-0":
		default:
			return false
		}
	}
	return false
}

var _ commandprogram.OperationDispatcher = (*ProgramPaths)(nil)

// physicalForm reports the last of -L and -P, as cd and pwd apply them.
func physicalForm(bound commandprogram.BoundArgv) bool {
	physical := false
	for _, item := range bound.Items {
		if item.Option != nil && (item.Option.Key == "-L" || item.Option.Key == "-P") {
			physical = item.Option.Key == "-P"
		}
	}
	return physical
}

func nonNilWriter(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
