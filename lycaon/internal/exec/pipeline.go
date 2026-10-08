package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/confine"
)

// pipelineWaitDelay bounds Wait when descendants keep output pipes open.
const pipelineWaitDelay = 5 * time.Second

// StageRun records one stage's outcome after a run.
type StageRun struct {
	Command string
	// ExitCode is nil for skipped stages.
	ExitCode *int
	// Failed reports a status that fails the stage's group: non-zero, except
	// SIGPIPE ending a stage whose reader finished first.
	Failed bool
	// Skipped means the preceding group's exit status did not satisfy the connector.
	Skipped bool
	// Connector is empty for the first stage and defaults to a pipe for subsequent stages.
	Connector argv.Connector
}

// ExitStatus reports the stage's exit code, or -1 for a stage that never ran.
func (s StageRun) ExitStatus() int {
	if s.ExitCode == nil {
		return -1
	}
	return *s.ExitCode
}

// PipelineResult is the outcome of RunPipeline.
type PipelineResult struct {
	Stages   []StageRun
	ExitCode int
	Output   []byte
	// Stderr is separate only when requested.
	Stderr   []byte
	TimedOut bool
}

func buildExecCmd(ctx context.Context, name string, args []string, opts ExecOpts) (*exec.Cmd, func(), error) {
	confinement := opts.Launch.Confinement
	if confinement != nil && confine.Available() {
		self, err := os.Executable()
		if err != nil {
			return nil, nil, fmt.Errorf("locate self for sandbox: %w", err)
		}
		confined, cleanup, err := confine.Command(ctx, self, name, args, *confinement)
		if err != nil {
			return nil, nil, fmt.Errorf("build sandboxed command: %w", err)
		}
		return confined, cleanup, nil
	}
	return exec.CommandContext(ctx, name, args...), func() {}, nil //nolint:gosec // G204 — validated argv stages, no shell
}

func configureCmdEnv(cmd *exec.Cmd, opts ExecOpts) error {
	return configureStageCmdEnv(cmd, opts, nil)
}

func configureStageCmdEnv(cmd *exec.Cmd, opts ExecOpts, stageEnv map[string]string) error {
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	provided := opts.Env
	if opts.Launch.Environment == EnvironmentReduced {
		provided = ReducedEnviron()
	}
	inline := opts.InlineEnv
	if len(stageEnv) > 0 {
		merged := make(map[string]string, len(inline)+len(stageEnv))
		for k, v := range inline {
			merged[k] = v
		}
		for k, v := range stageEnv {
			merged[k] = v
		}
		inline = merged
	}
	env, err := buildProcessEnv(provided, inline, opts.PathExtra)
	if err != nil {
		return err
	}
	if env == nil && provided != nil {
		// A non-nil empty environment prevents inheritance from the sidecar.
		env = []string{}
	}
	cmd.Env = env
	for _, e := range opts.AppendEnv {
		if strings.TrimSpace(e) != "" {
			cmd.Env = append(cmd.Env, e)
		}
	}
	for _, e := range opts.Launch.ExtraEnv {
		if strings.TrimSpace(e) != "" {
			cmd.Env = append(cmd.Env, e)
		}
	}
	if opts.Launch.Confinement != nil {
		confinement := opts.Launch.Confinement
		cmd.Env = confine.ProcessEnvironment(cmd.Env, *confinement)
		if confinement.Network != confine.NetworkDeny {
			if self, err := os.Executable(); err == nil {
				cmd.Env = overlayProcessEnv(cmd.Env,
					confine.GitSSHEnv(*confinement, self, confine.SSHKnownHostsPath(*confinement)))
			}
		}
	}
	return nil
}

// PrepareCommand constructs a caller-managed subprocess.
func PrepareCommand(ctx context.Context, name string, args []string, opts ExecOpts) (*exec.Cmd, func(), error) {
	if err := opts.Launch.validate(); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(name) == "" {
		return nil, nil, argv.ErrCommandRequired
	}
	if argv.ContainsShellMetacharacters(name) {
		return nil, nil, fmt.Errorf("%w: command name", argv.ErrShellMetacharacters)
	}
	cmd, cleanup, err := buildExecCmd(ctx, name, args, opts)
	if err != nil {
		return nil, nil, err
	}
	if err := configureCmdEnv(cmd, opts); err != nil {
		cleanup()
		return nil, nil, err
	}
	return cmd, cleanup, nil
}

// overlayProcessEnv replaces duplicate keys before appending overrides.
func overlayProcessEnv(base, overrides []string) []string {
	if len(overrides) == 0 {
		return base
	}
	keys := make(map[string]struct{}, len(overrides))
	for _, entry := range overrides {
		key, _, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			keys[key] = struct{}{}
		}
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, replaced := keys[key]; replaced {
			continue
		}
		out = append(out, entry)
	}
	return append(out, overrides...)
}

type wiredStage struct {
	cmd     *exec.Cmd
	guard   runGuard
	cleanup func()
	// stdout is the read end of the pipe into the next stage.
	stdout *os.File
	// pipeWriter is the parent's copy of the pipe's write end, closed once the stage starts.
	pipeWriter *os.File
}

// redirectFiles holds the call-level copies of the output streams.
type redirectFiles struct {
	spec   *RedirectSpec
	stdout *os.File
	stderr *os.File
}

func validatePipelineStages(stages []Stage) error {
	if len(stages) == 0 {
		return fmt.Errorf("pipeline requires at least one stage")
	}
	for i, stage := range stages {
		if strings.TrimSpace(stage.Name) == "" {
			return fmt.Errorf("pipeline stage %d: %w", i, argv.ErrCommandRequired)
		}
		if argv.ContainsShellMetacharacters(stage.Name) {
			return fmt.Errorf("pipeline stage %d: %w in command name", i, argv.ErrShellMetacharacters)
		}
	}
	return ValidateStreams(stages)
}

func openRedirectFiles(spec *RedirectSpec) (redirectFiles, error) {
	files := redirectFiles{spec: spec}
	if spec == nil {
		return files, nil
	}
	var err error
	if spec.Stdout != nil {
		files.stdout, err = openOutputFile(spec, *spec.Stdout)
		if err != nil {
			return files, err
		}
	}
	if spec.Stderr != nil {
		// Shared targets need one descriptor to preserve write offsets.
		if spec.Stdout != nil && spec.Stderr.Location == spec.Stdout.Location {
			files.stderr = files.stdout
			return files, nil
		}
		files.stderr, err = openOutputFile(spec, *spec.Stderr)
		if err != nil {
			files.close()
			return files, err
		}
	}
	return files, nil
}

func (f redirectFiles) commit(ctx context.Context) error {
	if f.spec == nil || f.spec.Commit == nil {
		return nil
	}
	for i, pair := range []struct {
		file   *os.File
		target *OutputTarget
	}{{f.stdout, f.spec.Stdout}, {f.stderr, f.spec.Stderr}} {
		if pair.file == nil || pair.target == nil || (i == 1 && f.stderr == f.stdout) {
			continue
		}
		if err := commitOutputFile(ctx, f.spec, pair.file, *pair.target); err != nil {
			return err
		}
	}
	return nil
}

// openOutputFile opens a review buffer when output commits through Commit,
// and the destination itself otherwise.
func openOutputFile(spec *RedirectSpec, target OutputTarget) (*os.File, error) {
	if spec.Commit != nil {
		return os.CreateTemp("", "command-output-*")
	}
	return openRedirectFile(target.Location, target.Append)
}

func commitOutputFile(ctx context.Context, spec *RedirectSpec, file *os.File, target OutputTarget) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return spec.Commit(ctx, target.Location, file, target.Append)
}

func closeOutputFile(spec *RedirectSpec, file *os.File) {
	if file == nil {
		return
	}
	_ = file.Close()
	if spec != nil && spec.Commit != nil {
		_ = os.Remove(file.Name())
	}
}

func (f redirectFiles) close() {
	closeOutputFile(f.spec, f.stdout)
	if f.stderr != f.stdout {
		closeOutputFile(f.spec, f.stderr)
	}
}

// callStreams are the call's output writers every stage's default streams reach.
type callStreams struct {
	stdout io.Writer
	stderr io.Writer
}

func newCallStreams(opts ExecOpts, stdout, stderr io.Writer, redirect redirectFiles) callStreams {
	if opts.Stdout != nil {
		stdout = opts.Stdout
	}
	if redirect.stdout != nil {
		stdout = io.MultiWriter(stdout, redirect.stdout)
	}
	if redirect.stderr != nil {
		stderr = io.MultiWriter(stderr, redirect.stderr)
	}
	return callStreams{stdout: stdout, stderr: stderr}
}

func wirePipelineStages(
	runCtx context.Context,
	stages []Stage,
	opts ExecOpts,
	call callStreams,
	files *stageFiles,
) ([]wiredStage, error) {
	wired := make([]wiredStage, len(stages))
	for i, stage := range stages {
		guard, err := newRunGuard(opts.ProcessPriority)
		if err != nil {
			releaseWired(wired[:i])
			return nil, err
		}
		cmd, cleanup, err := buildExecCmd(runCtx, stage.Name, stage.Args, opts)
		if err != nil {
			guard.release()
			releaseWired(wired[:i])
			return nil, err
		}
		cmd, cleanup, err = superviseCommand(cmd, cleanup, opts.ProcessPriority)
		if err != nil {
			guard.release()
			releaseWired(wired[:i])
			return nil, err
		}
		if err := configureStageCmdEnv(cmd, opts, stage.Env); err != nil {
			cleanup()
			guard.release()
			releaseWired(wired[:i])
			return nil, err
		}
		guard.configure(cmd)
		wired[i] = wiredStage{cmd: cmd, guard: guard, cleanup: cleanup}
		if i < len(stages)-1 {
			reader, writer, err := os.Pipe()
			if err != nil {
				releaseWired(wired[:i+1])
				return nil, err
			}
			wired[i].stdout, wired[i].pipeWriter = reader, writer
		}
		cmd.Stdout = stageSinkWriter(stage.Streams.StdoutSink(), wired[i].pipeWriter, call, files)
		cmd.Stderr = stageSinkWriter(stage.Streams.StderrSink(), wired[i].pipeWriter, call, files)
	}
	return wired, nil
}

// stageSinkWriter maps one descriptor's destination to a writer. A nil writer
// gives the child the null device.
func stageSinkWriter(sink argv.Sink, pipe *os.File, call callStreams, files *stageFiles) io.Writer {
	switch sink.Kind {
	case argv.SinkStdout:
		if pipe != nil {
			return pipe
		}
		return call.stdout
	case argv.SinkFile:
		return files.writer(sink.Path)
	case argv.SinkNull:
		return nil
	default:
		return call.stderr
	}
}

func startWiredStages(wired []wiredStage) error {
	for i := range wired {
		if err := wired[i].cmd.Start(); err != nil {
			killWired(wired[:i])
			reapWired(wired[:i])
			releaseWired(wired[:i+1])
			return err
		}
		if wired[i].pipeWriter != nil {
			_ = wired[i].pipeWriter.Close()
			wired[i].pipeWriter = nil
		}
		if err := wired[i].guard.onStarted(wired[i].cmd); err != nil {
			killWired(wired[:i+1])
			reapWired(wired[:i+1])
			releaseWired(wired[:i+1])
			return err
		}
	}
	// Parent pipe handles must close after every stage starts.
	for i := range wired {
		if wired[i].stdout != nil {
			_ = wired[i].stdout.Close()
			wired[i].stdout = nil
		}
	}
	return nil
}

// reapWired waits on every started stage after setup fails.
func reapWired(stages []wiredStage) {
	for i := range stages {
		if stages[i].cmd != nil && stages[i].cmd.Process != nil {
			_ = stages[i].cmd.Wait()
		}
	}
}

func waitWiredStages(runCtx context.Context, wired []wiredStage) (timedOut bool, waitErrs []error) {
	waitErrs = make([]error, len(wired))
	waitDone := make(chan struct{})
	go func() {
		for i := range wired {
			waitErrs[i] = wired[i].cmd.Wait()
		}
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-runCtx.Done():
		timedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
		// Synchronous teardown keeps the following wait ordered.
		terminateWired(wired)
		<-waitDone
	}
	return timedOut, waitErrs
}

// RunPipeline executes connected stages and returns the first counted failure.
func RunPipeline(ctx context.Context, stages []Stage, opts ExecOpts) (*PipelineResult, error) {
	return runPipeline(ctx, stages, opts, pipelineWaitDelay)
}

func runPipeline(ctx context.Context, stages []Stage, opts ExecOpts, waitDelay time.Duration) (*PipelineResult, error) {
	if err := opts.Launch.validate(); err != nil {
		return nil, err
	}
	if err := validatePipelineStages(stages); err != nil {
		return nil, err
	}

	timeout := opts.Timeout
	if timeout <= 0 && !opts.NoTimeout {
		timeout = DefaultGitTimeout
	}
	maxOut := opts.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = DefaultMaxOutputBytes
	}

	var runCtx context.Context
	var cancel context.CancelFunc
	if opts.NoTimeout {
		runCtx, cancel = context.WithCancel(ctx)
	} else {
		runCtx, cancel = context.WithTimeoutCause(ctx, timeout, errCommandDeadline)
	}
	defer cancel()

	stdinReader, err := openStdin(opts.Stdin)
	if err != nil {
		return nil, err
	}
	if stdinReader != nil {
		defer func() { _ = stdinReader.Close() }()
	}

	redirect, err := openRedirectFiles(opts.Redirect)
	if err != nil {
		return nil, err
	}
	defer redirect.close()

	capture := newCappedOutput(maxOut, opts.KeepOutputTail)
	errCapture := capture
	if opts.SeparateStderr || opts.Stdout != nil {
		errCapture = newCappedOutput(maxOut, opts.KeepOutputTail)
	}

	if waitDelay <= 0 {
		waitDelay = pipelineWaitDelay
	}
	call := newCallStreams(opts, capture, errCapture, redirect)
	run := newSequenceRun(stages, opts, call, stdinReader, waitDelay)
	if err := run.start(runCtx); err != nil {
		return nil, err
	}
	run.drive(runCtx)

	res, err := run.finalize(capture.Bytes(), capture.Truncated(), maxOut)
	if res != nil && opts.SeparateStderr {
		res.Stderr = errCapture.Bytes()
	}
	if commitErr := redirect.commit(ctx); commitErr != nil {
		return res, fmt.Errorf("%w: %w", ErrRedirectCommit, commitErr)
	}
	return res, err
}

func exitCodeFromWait(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func killWired(stages []wiredStage) {
	for i := range stages {
		if stages[i].guard != nil {
			stages[i].guard.kill(stages[i].cmd)
		}
	}
}

// terminateWired asks every stage's tree to exit, with the guard's kill fallback.
func terminateWired(stages []wiredStage) {
	for i := range stages {
		if stages[i].guard != nil {
			stages[i].guard.terminate(stages[i].cmd)
		}
	}
}

func releaseWired(stages []wiredStage) {
	for i := range stages {
		if stages[i].stdout != nil {
			_ = stages[i].stdout.Close()
		}
		if stages[i].pipeWriter != nil {
			_ = stages[i].pipeWriter.Close()
		}
		if stages[i].cleanup != nil {
			stages[i].cleanup()
		}
		if stages[i].guard != nil {
			stages[i].guard.release()
		}
	}
}
