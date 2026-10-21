package projectpaths

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Invoke binds path utility operands after normal per-child operation admission.
// It does not run a shell, call a JSON tool, or inherit authority from stdout.
func (p *ProgramPaths) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	err := request.Validate()
	if err == nil {
		err = p.commandOutput(ctx, request.OperationID, request.Argv.Values()[1:], streams.Output)
	}
	var pathErr *PathError
	if errors.As(err, &pathErr) {
		span := request.Invocation.Span
		data := map[string]any{"path": pathErr.Operand, "reason": pathErr.Cause.Error(), "source_start_byte": span.StartByte, "source_end_byte": span.EndByte, "source_line": span.Line, "source_column": span.Column, "limit_id": pathErr.LimitID, "maximum": pathErr.Maximum, "observed": pathErr.Observed, "command": pathErr.Command, "expected_form": pathErr.ExpectedForm, "resource_unit": pathErr.ResourceUnit}
		err = errors.Join(err, &toolrejection.ToolReject{Code: pathErr.Code, Data: data})
	}
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

func (p *ProgramPaths) commandOutput(ctx context.Context, name string, args []string, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, arg := range args {
		if int64(len(arg)) > p.argumentLimit {
			return pathLimit("argument_bytes", "bytes", p.argumentLimit, int64(len(arg)))
		}
		if strings.IndexByte(arg, 0) >= 0 {
			return invalidOperand(arg, "NUL in scalar operand")
		}
	}
	if name == "native_path" {
		return p.convertCommand(ctx, args, output)
	}
	if name == "readlink" {
		return p.linkCommand(ctx, args, output)
	}
	output = newPathOutput(ctx, output, p.outputLimit)
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	} else if len(args) > 0 && len(args[0]) > 1 && strings.HasPrefix(args[0], "-") {
		return p.unsupportedOption(name, args[0])
	}
	switch name {
	case "basename":
		if len(args) < 1 || len(args) > 2 {
			return invalidOperand(name, "expected PATH and optional SUFFIX")
		}
		suffix := ""
		if len(args) == 2 {
			suffix = args[1]
		}
		_, err := io.WriteString(output, PathBase(args[0], suffix)+"\n")
		return err
	case "dirname":
		if len(args) != 1 {
			return invalidOperand(name, "expected one PATH")
		}
		_, err := io.WriteString(output, PathDir(args[0])+"\n")
		return err
	case "pwd":
		if len(args) != 0 {
			return invalidOperand(name, "pwd accepts no operands")
		}
		if _, err := p.Bind(ctx, ".", false); err != nil {
			return err
		}
		_, err := io.WriteString(output, p.cwd.Resolved.DisplayPath+"\n")
		return err
	case "cd":
		if len(args) > 1 {
			return invalidOperand(name, "cd accepts at most one PATH")
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
			return invalidOperand(name, "expected at least one PATH")
		}
		return p.pathOutput(ctx, args, true, output)
	default:
		return invalidOperand(name, "not a path operation")
	}
}

func (p *ProgramPaths) convertCommand(ctx context.Context, args []string, output io.Writer) error {
	nul := false
	if len(args) > 0 && args[0] == "-0" {
		nul = true
		args = args[1:]
	}
	if len(args) > 0 && args[0] != "--" && len(args[0]) > 1 && strings.HasPrefix(args[0], "-") {
		return p.unsupportedOption("native_path", args[0])
	}
	if len(args) < 2 || args[0] != "--" {
		return invalidOperand("native_path", "expected [-0] -- PATH ...")
	}
	return p.NativePaths(ctx, args[1:], nul, output)
}

func (p *ProgramPaths) linkCommand(ctx context.Context, args []string, output io.Writer) error {
	canonical := false
	if len(args) > 0 && args[0] == "-f" {
		canonical = true
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	} else if len(args) > 0 && len(args[0]) > 1 && strings.HasPrefix(args[0], "-") {
		return p.unsupportedOption("readlink", args[0])
	}
	if len(args) == 0 {
		return invalidOperand("readlink", "expected at least one PATH")
	}
	return p.pathOutput(ctx, args, canonical, newPathOutput(ctx, output, p.outputLimit))
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

func (p *ProgramPaths) unsupportedOption(command, word string) error {
	return &PathError{Code: "COMMAND_OPTION_UNSUPPORTED", Operand: word, Command: command, ExpectedForm: p.forms[command], Cause: errors.New("unsupported native option")}
}
