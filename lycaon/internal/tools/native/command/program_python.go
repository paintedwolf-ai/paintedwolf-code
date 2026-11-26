package command

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythoncatalog"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// PythonExecutionRequest carries a managed Portable Python command invocation
// from the command evaluator to the Portable Python runtime service.
type PythonExecutionRequest struct {
	Mode     pythonruntime.ExecutionMode
	Code     string
	Script   string
	Module   string
	CLI      string
	CWD      string
	Args     []string
	Env      map[string]string
	Stdin    io.Reader
	ReadOnly bool
	RootPath string
	RootID   string
	ToolCtx  tools.ToolContext
}

// PythonExecutionResponse carries the outcome of a managed Portable Python run.
type PythonExecutionResponse struct {
	ExitCode int
	Stdout   string
	Stderr   string
	StopCode string
	StopMsg  string
}

// PythonRunner executes managed Portable Python command forms (python, python3,
// pytest, markdown-it) in the curated WASI image.
type PythonRunner interface {
	ExecuteCommand(ctx context.Context, req PythonExecutionRequest) (PythonExecutionResponse, error)
}

type portablePythonServiceRunner struct {
	svc *portablepython.Service
}

// NewPortablePythonRunner adapts a live portablepython.Service to PythonRunner.
func NewPortablePythonRunner(svc *portablepython.Service) PythonRunner {
	if svc == nil {
		return nil
	}
	return portablePythonServiceRunner{svc: svc}
}

func (r portablePythonServiceRunner) ExecuteCommand(ctx context.Context, req PythonExecutionRequest) (PythonExecutionResponse, error) {
	if r.svc == nil {
		return PythonExecutionResponse{}, fmt.Errorf("portable python runtime is not configured")
	}
	rootID := req.RootID
	if rootID == "" {
		rootID = "workspace"
	}
	var roots []pythondocuments.HostRootConfig
	if req.RootPath != "" {
		roots = []pythondocuments.HostRootConfig{{
			ID:       rootID,
			HostPath: req.RootPath,
			Primary:  true,
		}}
	}
	resp, err := r.svc.ExecuteTool(ctx, portablepython.ToolRequest{
		Actor:    req.ToolCtx.Identity.Agent,
		Surface:  "command",
		ReadOnly: req.ReadOnly,
		Mode:     req.Mode,
		Code:     req.Code,
		Script:   req.Script,
		Module:   req.Module,
		CLI:      req.CLI,
		CWD:      req.CWD,
		Args:     req.Args,
		Env:      req.Env,
		Stdin:    req.Stdin,
		Roots:    roots,
	})
	if err != nil {
		return PythonExecutionResponse{}, err
	}
	out := PythonExecutionResponse{
		ExitCode: resp.ExitCode,
		Stdout:   resp.Stdout,
		Stderr:   resp.Stderr,
	}
	if resp.Stop != nil {
		out.StopCode = resp.Stop.Code
		out.StopMsg = resp.Stop.Message
	}
	return out, nil
}

func (r *ProgramRuntime) resolveDirectPythonOffered(tool string, tc tools.ToolContext) bool {
	if r.DirectPythonOffered != nil {
		return r.DirectPythonOffered(tc)
	}
	if tool != "command" || r.Boundary == nil {
		return false
	}
	profileID := strings.TrimSpace(tc.Identity.ToolProfile)
	if profileID == "" {
		return false
	}
	return r.Boundary.ProfileAllowsTool(profileID, "python")
}

// refusePythonToolRequired stops a bare `python`/`python3` command launch
// before any interpreter starts, preserving earlier completed program effects.
func (d *ProgramDispatcher) refusePythonToolRequired(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams, dec pythoncatalog.RoutingDecision) (toolcontract.ProducerResult, error) {
	writeHostRefusal(streams, dec.Command, "", dec.Code)
	cause := toolrejection.RejectInvalidArguments(dec.Code, map[string]any{
		"command":           dec.Command,
		"source_start_byte": request.Invocation.Span.StartByte,
		"source_end_byte":   request.Invocation.Span.EndByte,
		"source_line":       request.Invocation.Span.Line,
		"source_column":     request.Invocation.Span.Column,
	})
	spec, ok := d.run.commands["python"]
	if !ok {
		d.shared.mu.Lock()
		if d.shared.rejected == nil {
			d.shared.rejected = &nativeRejection{cause: cause}
		}
		d.shared.mu.Unlock()
		return refusedResult(dec.Code), cause
	}
	child, err := d.prepare(ctx, spec, request, nil)
	if err != nil {
		return refusedResult(dec.Code), joinSettle(cause, err)
	}
	defer child.release()
	result := refusedResult(dec.Code)
	settleErr := d.settle(ctx, child, result, false, "")
	d.shared.mu.Lock()
	if d.shared.rejected == nil {
		d.shared.rejected = &nativeRejection{operation: child.bound, cause: cause}
	}
	d.shared.mu.Unlock()
	return result, joinSettle(cause, settleErr)
}

func pythonNativeOperation(spec toolcontract.CommandSpecification, runner PythonRunner, run *programRun) NativeOperation {
	return NativeOperation{
		Tool:        spec.Name,
		ClosedForms: true,
		Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (NativePrepared, error) {
			argv := request.Argv.Values()
			parsed, err := pythoncatalog.ParsePythonCommandLine(argv)
			if err != nil {
				return NativePrepared{}, toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": spec.Name, "reason": err.Error()})
			}
			mode := selectPythonEffectMode(spec, run)
			review := commandprogram.BoundArgv{Command: spec.Name}.Review(spec.Name)
			return NativePrepared{
				Mode:   mode,
				Review: review,
				Tool:   spec.Name,
				Invoke: func(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
					return runManagedPython(ctx, spec.Name, runner, frame, tc, request, streams, func(req *PythonExecutionRequest) error {
						switch parsed.Mode {
						case pythoncatalog.ModeCode:
							req.Mode = pythonruntime.ModeCode
							req.Code = parsed.Target
						case pythoncatalog.ModeModule:
							req.Mode = pythonruntime.ModeModule
							req.Module = parsed.Target
						case pythoncatalog.ModeScript:
							req.Mode = pythonruntime.ModeScript
							req.Script = parsed.Target
						case pythoncatalog.ModeStdin:
							src, err := io.ReadAll(streams.Input)
							if err != nil {
								return err
							}
							req.Mode = pythonruntime.ModeCode
							req.Code = string(src)
						case pythoncatalog.ModeVersion:
							req.Mode = pythonruntime.ModeCode
							req.Code = "import sys; print('Python ' + sys.version.split()[0])"
						case pythoncatalog.ModeHelp:
							req.Mode = pythonruntime.ModeCode
							req.Code = "import sys; print('usage: python [-c cmd | -m mod | file | -] [arg] ...')"
						default:
							return fmt.Errorf("unsupported python mode %q", parsed.Mode)
						}
						req.Args = parsed.Argv
						return nil
					})
				},
			}, nil
		},
	}
}

func pytestNativeOperation(spec toolcontract.CommandSpecification, runner PythonRunner, run *programRun) NativeOperation {
	return NativeOperation{
		Tool:        spec.Name,
		ClosedForms: true,
		Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (NativePrepared, error) {
			argv := request.Argv.Values()
			args := append([]string(nil), argv[1:]...)
			mode := selectPythonEffectMode(spec, run)
			review := commandprogram.BoundArgv{Command: spec.Name}.Review(spec.Name)
			return NativePrepared{
				Mode:   mode,
				Review: review,
				Tool:   spec.Name,
				Invoke: func(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
					return runManagedPython(ctx, spec.Name, runner, frame, tc, request, streams, func(req *PythonExecutionRequest) error {
						req.Mode = pythonruntime.ModeModule
						req.Module = "pytest"
						req.Args = args
						return nil
					})
				},
			}, nil
		},
	}
}

func markdownItNativeOperation(spec toolcontract.CommandSpecification, runner PythonRunner, run *programRun) NativeOperation {
	return NativeOperation{
		Tool:        spec.Name,
		ClosedForms: true,
		Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (NativePrepared, error) {
			argv := request.Argv.Values()
			args := append([]string(nil), argv[1:]...)
			review := commandprogram.BoundArgv{Command: spec.Name}.Review(spec.Name)
			return NativePrepared{
				Mode:   "default",
				Review: review,
				Tool:   spec.Name,
				Invoke: func(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
					return runManagedPython(ctx, spec.Name, runner, frame, tc, request, streams, func(req *PythonExecutionRequest) error {
						req.Mode = pythonruntime.ModeCLI
						req.CLI = "markdown-it"
						req.Args = args
						req.Stdin = streams.Input
						return nil
					})
				},
			}, nil
		},
	}
}

func selectPythonEffectMode(spec toolcontract.CommandSpecification, run *programRun) string {
	if run != nil && (run.tc.Source.ReadOnly || run.tc.Source.AskMode) {
		for _, eff := range spec.Effects {
			if eff.Mode == "read-only" {
				return "read-only"
			}
		}
	}
	return "default"
}

func runManagedPython(
	ctx context.Context,
	cmdName string,
	runner PythonRunner,
	frame *projectpaths.ProgramPaths,
	tc tools.ToolContext,
	request commandprogram.OperationRequest,
	streams commandprogram.Streams,
	configure func(*PythonExecutionRequest) error,
) (toolcontract.ProducerResult, error) {
	if runner == nil {
		writeHostRefusal(streams, cmdName, "", "COMMAND_PLATFORM_UNAVAILABLE")
		return refusedResult("COMMAND_PLATFORM_UNAVAILABLE"), toolrejection.RejectInvalidArguments("COMMAND_PLATFORM_UNAVAILABLE", map[string]any{
			"command": cmdName,
			"reason":  "portable python runtime is not configured",
		})
	}
	cwd := frame.Cwd()
	env := make(map[string]string, len(request.Environment))
	for _, entry := range request.Environment {
		if k, v, ok := strings.Cut(entry, "="); ok {
			env[k] = v
		}
	}
	req := PythonExecutionRequest{
		ReadOnly: tc.Source.ReadOnly || tc.Source.AskMode,
		RootPath: cwd.Abs,
		RootID:   cwd.RootID,
		Env:      env,
		Stdin:    streams.Input,
		ToolCtx:  tc,
	}
	if err := configure(&req); err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
	}
	resp, err := runner.ExecuteCommand(ctx, req)
	if err != nil {
		return toolcontract.ProducerResult{
			Status:          1,
			Terminal:        toolcontract.ProducerFailed,
			EffectCertainty: toolcontract.EffectUnknown,
		}, err
	}
	if resp.Stdout != "" && streams.Output != nil {
		if _, werr := io.WriteString(streams.Output, resp.Stdout); werr != nil {
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectUnknown}, werr
		}
	}
	if resp.Stderr != "" && streams.Error != nil {
		if _, werr := io.WriteString(streams.Error, resp.Stderr); werr != nil {
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectUnknown}, werr
		}
	}
	if resp.StopCode != "" {
		return refusedResult(resp.StopCode), toolrejection.RejectInvalidArguments(resp.StopCode, map[string]any{
			"command": cmdName,
			"reason":  resp.StopMsg,
		})
	}
	return toolcontract.ProducerResult{
		Status:              resp.ExitCode,
		Terminal:            toolcontract.ProducerCompleted,
		Complete:            true,
		PublicationEligible: resp.ExitCode == 0,
		EffectCertainty:     toolcontract.NoEffect,
	}, nil
}
