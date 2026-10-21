package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// ExternalStages launches one confined external stage per request through the
// existing launch plan, confinement lease and process-tree supervision. Argv
// stays literal: no shell, no PATH re-resolution by the host, no fallback.
type ExternalStages struct {
	ToolContext     tools.ToolContext
	ExtraWriteRoots []string
}

var _ commandprogram.ExternalLauncher = ExternalStages{}

// Launch runs argv (after an optional "external --") with the request's
// streams and settles with the process's actual status.
func (l ExternalStages) Launch(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	argv := externalArgv(request.Argv.Values())
	if len(argv) == 0 {
		return refusedResult("COMMAND_OPERAND_INVALID"), fmt.Errorf("external launch requires a program")
	}
	tctx := l.ToolContext
	cwd, _, err := projectpaths.CommandCwd(ctx, tctx, request.Invocation.Scope.Directory)
	if err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
	}
	if err := tools.ValidateWorkerBranch(ctx, tctx); err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), err
	}
	commandLine := (lycexec.Stage{Name: argv[0], Args: argv[1:]}).CommandLine()
	bound, err := bindCommandForegroundConfine(ctx, tctx, l.ExtraWriteRoots, commandLine, "command", true)
	if err != nil {
		return refusedResult("COMMAND_EXEC_DENIED"), err
	}
	defer bound.close(context.WithoutCancel(ctx))
	opts := lycexec.ExecOpts{
		Launch:         agentCommandLaunch(tctx, "command", bound.confinement, bound.lease, commandLine),
		Dir:            cwd,
		NoTimeout:      true,
		InlineEnv:      request.Environment,
		PathExtra:      append([]string(nil), tctx.Host.HostResourcePathExtra...),
		SeparateStderr: true,
		Stdout:         nonNilWriter(streams.Output),
	}
	if streams.Input != nil {
		opts.Stdin = &lycexec.StdinSpec{Reader: streams.Input}
	}
	if err := tctx.Effects.Secrets.HandOff(ctx, nil); err != nil {
		return refusedResult("COMMAND_EXEC_DENIED"), err
	}
	async, err := lycexec.StartPipelineAsync(ctx, []lycexec.Stage{{Name: argv[0], Args: argv[1:]}}, opts, nonNilWriter(streams.Output), nonNilWriter(streams.Error))
	if err != nil {
		return toolcontract.ProducerResult{Status: 127, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
	}
	select {
	case <-async.Done():
	case <-ctx.Done():
		async.Kill()
	}
	run, waitErr := async.Wait()
	if ctx.Err() != nil {
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.EffectUnknown}, errors.Join(ctx.Err(), waitErr)
	}
	if run == nil {
		return toolcontract.ProducerResult{Status: 127, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectUnknown}, waitErr
	}
	status := run.ExitCode
	if status < 0 || status > 255 {
		status = 255
	}
	// An external process may have done anything it was confined to do.
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: status == 0, EffectCertainty: toolcontract.EffectAttempted}, waitErr
}

// externalArgv drops the explicit "external --" selector.
func externalArgv(argv []string) []string {
	if len(argv) >= 2 && argv[0] == "external" && argv[1] == "--" {
		return argv[2:]
	}
	return argv
}

func nonNilWriter(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func refusedResult(code string) toolcontract.ProducerResult {
	return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: strings.TrimSpace(code), EffectCertainty: toolcontract.NoEffect}
}
