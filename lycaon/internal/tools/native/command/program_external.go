package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/bgprocess"
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
	ObserveStage    func(tools.ToolContext, confine.StampedRefusal, []confine.EgressHost, bool)
}

var _ commandprogram.ExternalLauncher = ExternalStages{}

// Launch runs argv (after an optional "external --") with the request's
// streams and settles with the process's actual status.
func (l ExternalStages) Launch(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	fields := externalFields(request.Argv)
	argv := fields.Values()
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
	if _, err := lycexec.LaunchLookup(argv[0], opts); confine.IsCommandNotFound(err) {
		_, _ = fmt.Fprintf(nonNilWriter(streams.Error), "bash: %s: command not found\n", argv[0])
		return toolcontract.ProducerResult{Status: 127, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
	}
	opts.NoTimeout, opts.SeparateStderr, opts.Stdout, opts.HostConsumer = true, true, nonNilWriter(streams.Output), true
	opts.Child = confine.ProcessAttributes{Limits: childLimits(request.Process.Limits), IgnoreHangup: request.Process.IgnoreHangup}
	if streams.Input != nil && !commandprogram.IsNullInput(streams.Input) {
		opts.Stdin = &lycexec.StdinSpec{Reader: streams.Input}
	}
	if err := tctx.Effects.Secrets.HandOff(ctx, nil); err != nil {
		if ctx.Err() != nil {
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, errors.Join(ctx.Err(), err)
		}
		return externalLaunchFailure(err, refusedResult("COMMAND_EXEC_DENIED")), err
	}
	launchArgv, cleanupProcSubst, err := materializeExternalArgv(tctx.Host.SessionScratchDir, fields)
	if err != nil {
		return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
	}
	defer cleanupProcSubst()
	opts.Started = func(pid int) { noteJobProcess(ctx, pid, launchArgv) }
	async, err := lycexec.StartPipelineAsync(ctx, []lycexec.Stage{{Name: launchArgv[0], Args: launchArgv[1:]}}, opts, nonNilWriter(streams.Output), nonNilWriter(streams.Error))
	if err != nil {
		if ctx.Err() != nil {
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.EffectUnknown}, errors.Join(ctx.Err(), err)
		}
		if errors.Is(err, exec.ErrNotFound) || confine.IsCommandNotFound(err) {
			_, _ = fmt.Fprintf(nonNilWriter(streams.Error), "bash: %s: command not found\n", argv[0])
			return toolcontract.ProducerResult{Status: 127, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
		}
		return externalLaunchFailure(err, toolcontract.ProducerResult{Status: 127, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}), err
	}
	detach := request.Process.Signals.Attach(func(sig commandprogram.Signal) bool {
		return async.Signal(syscall.Signal(sig.Number))
	})
	unblock, blockErr := async.ObserveWait(func() (func(), error) { return request.Wait.Process(ctx) })
	if blockErr != nil {
		async.Kill()
		_, _ = async.Wait()
		detach()
		return ownerFailure(blockErr), blockErr
	}
	defer unblock()
	select {
	case <-async.Done():
	case <-ctx.Done():
		async.Kill()
	}
	run, waitErr := async.Wait()
	detach()
	l.settleStageConfine(ctx, tctx, bound, commandLine, run)
	if run != nil && len(run.Stages) == 1 {
		request.Process.Times.Add(run.Stages[0].UserTime, run.Stages[0].SystemTime)
	}
	if ctx.Err() != nil {
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.EffectUnknown}, errors.Join(ctx.Err(), waitErr)
	}
	if run == nil {
		return toolcontract.ProducerResult{Status: 127, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectUnknown}, waitErr
	}
	status := run.ExitCode
	if len(run.Stages) == 1 {
		status = run.Stages[0].ExitStatus()
		if run.Stages[0].ExpectedConsumerClosure() {
			status = 141
		}
	}
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

// externalFields drops the explicit "external --" selector while keeping the
// remaining Field values and any attached ProcessSubstitution references.
func externalFields(argv commandprogram.Fields) commandprogram.Fields {
	if len(argv) >= 2 && argv[0].Value == "external" && argv[1].Value == "--" {
		return argv[2:]
	}
	return argv
}

// materializeExternalArgv creates one FIFO per distinct ProcessSubstitution
// referenced in fields and replaces only the FieldPart spans carrying a
// non-nil Substitution with the corresponding FIFO path. Literal /dev/fd/N
// strings in ordinary fields remain untouched.
func materializeExternalArgv(scratchDir string, fields commandprogram.Fields) ([]string, func(), error) {
	var streams []lycexec.ProcessSubstitutionStream
	indexBySubst := map[*commandprogram.ProcessSubstitution]int{}
	for _, field := range fields {
		for _, part := range field.Parts() {
			subst := part.Substitution
			if subst == nil {
				continue
			}
			if _, seen := indexBySubst[subst]; seen {
				continue
			}
			stream := lycexec.ProcessSubstitutionStream{}
			switch {
			case subst.IsInput():
				reader, ok := subst.OpenReader()
				if !ok {
					continue
				}
				stream.Reader = reader
			case subst.IsOutput():
				writer, ok := subst.OpenWriter()
				if !ok {
					continue
				}
				stream.Writer = writer
			default:
				continue
			}
			indexBySubst[subst] = len(streams)
			streams = append(streams, stream)
		}
	}
	if len(streams) == 0 {
		return fields.Values(), func() {}, nil
	}
	fifoPaths, cleanup, err := lycexec.BindProcessSubstitutions(scratchDir, streams)
	if err != nil {
		return nil, func() {}, err
	}
	argv := make([]string, len(fields))
	for i, field := range fields {
		parts := field.Parts()
		if len(parts) == 0 {
			argv[i] = field.Value
			continue
		}
		var b strings.Builder
		for _, part := range parts {
			if idx, ok := indexBySubst[part.Substitution]; ok {
				b.WriteString(fifoPaths[idx])
			} else {
				b.WriteString(part.Value)
			}
		}
		argv[i] = b.String()
	}
	return argv, cleanup, nil
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

func (l ExternalStages) settleStageConfine(ctx context.Context, tctx tools.ToolContext, bound commandForegroundConfine, commandLine string, run *lycexec.PipelineResult) {
	settleCtx := context.WithoutCancel(ctx)
	refusals := bound.lease.SettledRefusals(settleCtx)
	network := bound.close(settleCtx)
	boundary := confine.BoundaryOf(bound.confinement)
	if !boundary.Applied {
		return
	}
	termination := string(bgprocess.TerminationExited)
	var failed []string
	switch {
	case ctx.Err() != nil:
		termination = string(bgprocess.TerminationStopped)
	case run == nil:
		failed = []string{commandLine}
	case len(run.Stages) == 1:
		if run.Stages[0].Failed {
			failed = []string{commandLine}
		}
	case run.ExitCode != 0:
		failed = []string{commandLine}
	}
	report := CommandConfinementReport(boundary, "", tools.LocalNetworkGrantOf(tctx), tctx.Files.PackageExecution)
	stamped := confine.StampRefusal("command", tctx.Identity.SessionID, boundary, confine.RefusalContext{
		MediatedNetwork:        network,
		RemotePackageExecution: report.RemotePackageExecution,
		FailedStages:           failed,
		TerminationReason:      termination,
		Refusals:               refusals,
	})
	if l.ObserveStage != nil {
		l.ObserveStage(tctx, stamped, network, bound.directIPApplied)
		return
	}
	if tctx.Effects.Out != nil {
		tctx.Effects.Out.Facts = tools.ApplyRefusalFacts(tctx.Effects.Out.Facts, stamped)
		tools.CaptureExternalAccess(tctx, network, bound.directIPApplied)
	}
}

func (d *ProgramDispatcher) recordExternalStage(admitted tools.ToolContext, stamped confine.StampedRefusal, hosts []confine.EgressHost, directIPApplied bool) {
	if d == nil || d.run == nil || d.run.tc.Effects.Out == nil {
		return
	}
	d.shared.mu.Lock()
	defer d.shared.mu.Unlock()
	d.run.tc.Effects.Out.Facts = tools.ApplyRefusalFacts(d.run.tc.Effects.Out.Facts, stamped)
	childTC := admitted
	childTC.Effects.Out = d.run.tc.Effects.Out
	tools.CaptureExternalAccess(childTC, hosts, directIPApplied)
}
