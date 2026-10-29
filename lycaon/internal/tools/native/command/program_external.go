package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// ExternalStages launches one confined external stage per request through the
// existing launch plan, confinement lease and process-tree supervision. Argv
// stays literal: no shell, no PATH re-resolution by the host, no fallback.
type ExternalStages struct {
	ToolContext     tools.ToolContext
	Dir             string
	ExtraWriteRoots []string
	BaseEnvironment []string
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
	cwd := l.Dir
	if cwd == "" {
		return refusedResult("COMMAND_OPERAND_INVALID"), fmt.Errorf("external launch requires a bound directory frame")
	}
	if err := tools.ValidateWorkerBranch(ctx, tctx); err != nil {
		if ctx.Err() != nil {
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, errors.Join(ctx.Err(), err)
		}
		return externalLaunchFailure(err, refusedResult("COMMAND_OPERAND_INVALID")), err
	}
	commandLine := (lycexec.Stage{Name: argv[0], Args: argv[1:]}).CommandLine()
	bound, err := bindCommandForegroundConfine(ctx, tctx, l.ExtraWriteRoots, commandLine, "command", true)
	if err != nil {
		if ctx.Err() != nil {
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, errors.Join(ctx.Err(), err)
		}
		return externalLaunchFailure(err, refusedResult("COMMAND_EXEC_DENIED")), err
	}
	defer bound.close(context.WithoutCancel(ctx))
	launch := agentCommandLaunch(tctx, "command", bound.confinement, bound.lease, commandLine).WithTrustedEnvironment(l.BaseEnvironment)
	opts, err := lycexec.AgentLaunchOptions(launch, cwd, request.Environment, tctx.Host.HostResourcePathExtra)
	if err != nil {
		return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "external", "reason": err.Error(), "expected": "an environment without host-reserved keys"})
	}
	opts.NoTimeout, opts.SeparateStderr, opts.Stdout, opts.HostConsumer = true, true, nonNilWriter(streams.Output), true
	if streams.Input != nil && !commandprogram.IsNullInput(streams.Input) {
		opts.Stdin = &lycexec.StdinSpec{Reader: streams.Input}
	}
	if err := tctx.Effects.Secrets.HandOff(ctx, nil); err != nil {
		if ctx.Err() != nil {
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, errors.Join(ctx.Err(), err)
		}
		return externalLaunchFailure(err, refusedResult("COMMAND_EXEC_DENIED")), err
	}
	opts.Started = func(pid int) { noteJobProcess(ctx, pid, argv) }
	async, err := lycexec.StartPipelineAsync(ctx, []lycexec.Stage{{Name: argv[0], Args: argv[1:]}}, opts, nonNilWriter(streams.Output), nonNilWriter(streams.Error))
	if err != nil {
		if ctx.Err() != nil {
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.EffectUnknown}, errors.Join(ctx.Err(), err)
		}
		return externalLaunchFailure(err, toolcontract.ProducerResult{Status: 127, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}), err
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
	result := toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: status == 0, EffectCertainty: toolcontract.EffectAttempted}
	if len(run.Stages) == 1 && run.Stages[0].ExpectedConsumerClosure() {
		// The executor's own SIGPIPE classification travels to pipeline settlement.
		return result, errors.Join(waitErr, launchClosure{run.Stages[0]})
	}
	return result, waitErr
}

// Installed reports whether the confined launch would find name: the same
// final PATH, launch directory and environment as Launch.
func (l ExternalStages) Installed(name string, environment []string) bool {
	opts, err := lycexec.AgentLaunchOptions(lycexec.LaunchPlan{}.WithTrustedEnvironment(l.BaseEnvironment), l.Dir, environment, l.ToolContext.Host.HostResourcePathExtra)
	if err != nil {
		return false
	}
	_, err = lycexec.LaunchLookup(name, opts)
	return err == nil
}

// programEnvironment builds the program's initial environment from the same
// AgentLaunchOptions and ProcessEnvironment pipeline that ExternalStages.Launch
// uses for confined external child launches.
func programEnvironment(tc tools.ToolContext) ([]string, error) {
	confinement, _ := confine.DefaultConfinement(hitl.ActionConfineRequest(tools.ActionConfineInputsForContext(tc, nil)))
	launch := agentCommandLaunch(tc, "command", confinement, nil, "")
	opts, err := lycexec.AgentLaunchOptions(launch, "", nil, tc.Host.HostResourcePathExtra)
	if err != nil {
		return nil, err
	}
	return lycexec.FinalProcessEnvironment(opts, nil)
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

func externalLaunchFailure(err error, fallback toolcontract.ProducerResult) toolcontract.ProducerResult {
	if rejection := toolrejection.AsToolReject(err); rejection != nil {
		return refusedResult(rejection.Code)
	}
	return fallback
}

func refusedResult(code string) toolcontract.ProducerResult {
	return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: strings.TrimSpace(code), EffectCertainty: toolcontract.NoEffect}
}

// launchClosure reports the executor's classification that an external
// producer ended by SIGPIPE after its host consumer stopped reading.
type launchClosure struct{ lycexec.StageRun }

func (launchClosure) Error() string { return "external producer stopped after its consumer closed" }
