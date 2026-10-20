package projectpaths

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Invoke binds path utility operands after normal per-child operation admission.
// It does not run a shell, call a JSON tool, or inherit authority from stdout.
func (p *ProgramPaths) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	output, err := p.commandOutput(ctx, request.OperationID, request.Arguments)
	var pathErr *PathError
	if errors.As(err, &pathErr) {
		span := request.Invocation.Span
		data := map[string]any{"path": pathErr.Operand, "reason": pathErr.Cause.Error(), "source_start_byte": span.StartByte, "source_end_byte": span.EndByte, "source_line": span.Line, "source_column": span.Column, "limit_id": pathErr.LimitID, "maximum": pathErr.Maximum, "observed": pathErr.Observed}
		err = errors.Join(err, &toolrejection.ToolReject{Code: pathErr.Code, Data: data})
	}
	if err == nil && streams.Output != nil {
		_, err = streams.Output.Write(output)
	}
	if err != nil {
		return toolcontract.ProducerResult{Status: 2, Complete: false, PublicationEligible: false, EffectCertainty: "none"}, err
	}
	return toolcontract.ProducerResult{Status: 0, Complete: true, PublicationEligible: true, EffectCertainty: "none"}, nil
}

func (p *ProgramPaths) commandOutput(ctx context.Context, name string, args []string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, arg := range args {
		if int64(len(arg)) > p.argumentLimit {
			return nil, pathLimit("argument_bytes", p.argumentLimit, int64(len(arg)))
		}
		if strings.IndexByte(arg, 0) >= 0 {
			return nil, invalidOperand(arg, "NUL in scalar operand")
		}
	}
	if name == "native_path" {
		return p.convertCommand(ctx, args)
	}
	if name == "readlink" {
		return p.linkCommand(ctx, args)
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	} else if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return nil, invalidOperand(args[0], "unsupported option; use -- before path operands")
	}
	switch name {
	case "basename":
		if len(args) < 1 || len(args) > 2 {
			return nil, invalidOperand(name, "expected PATH and optional SUFFIX")
		}
		suffix := ""
		if len(args) == 2 {
			suffix = args[1]
		}
		return []byte(PathBase(args[0], suffix) + "\n"), nil
	case "dirname":
		if len(args) != 1 {
			return nil, invalidOperand(name, "expected one PATH")
		}
		return []byte(PathDir(args[0]) + "\n"), nil
	case "pwd":
		if len(args) != 0 {
			return nil, invalidOperand(name, "pwd accepts no operands")
		}
		if _, err := p.Bind(ctx, ".", false); err != nil {
			return nil, err
		}
		return []byte(p.cwd.Resolved.Abs + "\n"), nil
	case "cd":
		if len(args) > 1 {
			return nil, invalidOperand(name, "cd accepts at most one PATH")
		}
		target := p.context.ActiveRootPath()
		if len(args) == 1 {
			target = args[0]
		}
		return nil, p.Chdir(ctx, target)
	case "realpath":
		if len(args) == 0 {
			return nil, invalidOperand(name, "expected at least one PATH")
		}
		return p.pathOutput(ctx, args, true)
	default:
		return nil, invalidOperand(name, "not a path operation")
	}
}

func (p *ProgramPaths) convertCommand(ctx context.Context, args []string) ([]byte, error) {
	nul := false
	if len(args) > 0 && args[0] == "-0" {
		nul = true
		args = args[1:]
	}
	if len(args) < 2 || args[0] != "--" {
		return nil, invalidOperand("native_path", "expected [-0] -- PATH ...")
	}
	return p.NativePaths(ctx, args[1:], nul)
}

func (p *ProgramPaths) linkCommand(ctx context.Context, args []string) ([]byte, error) {
	canonical := false
	if len(args) > 0 && args[0] == "-f" {
		canonical = true
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	} else if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return nil, invalidOperand(args[0], "unsupported readlink option")
	}
	if len(args) == 0 {
		return nil, invalidOperand("readlink", "expected at least one PATH")
	}
	return p.pathOutput(ctx, args, canonical)
}

func (p *ProgramPaths) pathOutput(ctx context.Context, args []string, canonical bool) ([]byte, error) {
	var output []byte
	for _, arg := range args {
		var path string
		var err error
		if canonical {
			path, err = p.Canonical(ctx, arg)
		} else {
			path, err = p.LinkTarget(ctx, arg)
		}
		if err != nil {
			return nil, err
		}
		if err := validateOutputPath(path, false); err != nil {
			return nil, err
		}
		total := int64(len(output)) + int64(len(path)) + 1
		if total > p.outputLimit {
			return nil, pathLimit("stream_bytes", p.outputLimit, total)
		}
		output = append(output, []byte(path)...)
		output = append(output, '\n')
	}
	return output, nil
}

var _ commandprogram.OperationDispatcher = (*ProgramPaths)(nil)
