package projectpaths

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// Invoke binds path utility operands after normal per-child operation admission.
// It does not run a shell, call a JSON tool, or inherit authority from stdout.
func (p *ProgramPaths) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	err := request.Validate()
	if err == nil {
		err = p.commandOutput(ctx, request.OperationID, request.Argv, streams.Output)
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
	return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
}

func (p *ProgramPaths) commandOutput(ctx context.Context, name string, argv commandprogram.Fields, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, arg := range argv[1:] {
		if int64(len(arg.Value)) > p.argumentLimit {
			return pathLimit("argument_bytes", "bytes", p.argumentLimit, int64(len(arg.Value)))
		}
		if strings.IndexByte(arg.Value, 0) >= 0 {
			return invalidOperand(arg.Value, "NUL in scalar operand")
		}
	}
	spec, ok := p.specs[name]
	if !ok {
		return invalidOperand(name, "not a path operation")
	}
	bound, err := commandprogram.CatalogBinder{}.Bind(spec, argv)
	var argvErr *commandprogram.ArgvError
	if errors.As(err, &argvErr) {
		return &PathError{Code: argvErr.Code, Operand: argvErr.Word, Command: name, ExpectedForm: spec.Argv, Cause: errors.New(argvErr.Expected)}
	}
	if err != nil {
		return err
	}
	args := bound.Operands().Values()
	usage := func(reason string) error {
		return &PathError{Code: "COMMAND_OPERAND_INVALID", Operand: name, Command: name, ExpectedForm: spec.Argv, Cause: errors.New(reason)}
	}
	if name == "native_path" {
		if !bound.EndOfOptions || len(args) == 0 || !operandsFollowEnd(argv) {
			return usage("expected [-0] -- PATH ...")
		}
		_, nul := bound.Last("-0")
		return p.NativePaths(ctx, args, nul, output)
	}
	output = newPathOutput(ctx, output, p.outputLimit)
	switch name {
	case "basename":
		if len(args) < 1 || len(args) > 2 {
			return usage("expected PATH and optional SUFFIX")
		}
		suffix := ""
		if len(args) == 2 {
			suffix = args[1]
		}
		_, err := io.WriteString(output, PathBase(args[0], suffix)+"\n")
		return err
	case "dirname":
		if len(args) != 1 {
			return usage("expected one PATH")
		}
		_, err := io.WriteString(output, PathDir(args[0])+"\n")
		return err
	case "pwd":
		if len(args) != 0 {
			return usage("pwd accepts no operands")
		}
		if _, err := p.Bind(ctx, ".", false); err != nil {
			return err
		}
		_, err := io.WriteString(output, p.cwd.Resolved.DisplayPath+"\n")
		return err
	case "cd":
		if len(args) > 1 {
			return usage("cd accepts at most one PATH")
		}
		if len(args) == 0 {
			info, err := directoryInfo(p.initial)
			if err != nil {
				return err
			}
			if !os.SameFile(info, p.initialInfo) {
				return invalidOperand("cd", "initial invocation directory identity changed")
			}
			p.cwd = p.initial
			p.cwdInfo = info
			return nil
		}
		return p.Chdir(ctx, args[0])
	case "realpath":
		if len(args) == 0 {
			return usage("expected at least one PATH")
		}
		return p.pathOutput(ctx, args, true, output)
	case "readlink":
		if len(args) == 0 {
			return usage("expected at least one PATH")
		}
		_, canonical := bound.Last("-f")
		return p.pathOutput(ctx, args, canonical, output)
	default:
		return invalidOperand(name, "not a path operation")
	}
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

func (p *ProgramPaths) pathOutput(ctx context.Context, args []string, canonical bool, output io.Writer) error {
	for _, arg := range args {
		var path string
		var err error
		if canonical {
			path, err = p.Canonical(ctx, arg)
		} else {
			path, err = p.LinkTarget(ctx, arg)
		}
		if err != nil {
			return err
		}
		if err := validateOutputPath(path, false); err != nil {
			return err
		}
		if _, err := io.WriteString(output, path+"\n"); err != nil {
			return err
		}
	}
	return nil
}

var _ commandprogram.OperationDispatcher = (*ProgramPaths)(nil)
