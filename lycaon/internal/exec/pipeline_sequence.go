package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	osexec "os/exec"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/argv"
)

// sequenceRun drives pipe groups and sequenced stages in order.
type sequenceRun struct {
	stages    []Stage
	groups    []StageGroup
	opts      ExecOpts
	call      callStreams
	stdin     io.ReadCloser
	waitDelay time.Duration

	mu        sync.Mutex
	live      []wiredStage
	liveFiles *stageFiles
	killed    bool
	// commitErr stops the plan: a later group must not read a file that did not land.
	commitErr error

	waitErrs []error
	skipped  []bool
	started  time.Time
	timeout  *TimeoutError

	// lastExit is the exit code of the most recently executed group.
	lastExit int
}

func newSequenceRun(
	stages []Stage,
	opts ExecOpts,
	call callStreams,
	stdin io.ReadCloser,
	waitDelay time.Duration,
) *sequenceRun {
	skipped := make([]bool, len(stages))
	for i := range skipped {
		skipped[i] = true
	}
	return &sequenceRun{
		stages:    stages,
		groups:    GroupStages(stages),
		opts:      opts,
		call:      call,
		stdin:     stdin,
		waitDelay: waitDelay,
		waitErrs:  make([]error, len(stages)),
		skipped:   skipped,
	}
}

// start launches the first group and returns any launch error immediately.
func (s *sequenceRun) start(ctx context.Context) error {
	s.started = time.Now()
	if len(s.groups) == 0 {
		return errors.New("pipeline requires at least one stage")
	}
	return s.startGroup(ctx, s.groups[0])
}

// drive waits the first group and runs remaining groups in order.
func (s *sequenceRun) drive(ctx context.Context) {
	s.waitGroup(ctx, s.groups[0])
	for _, group := range s.groups[1:] {
		if !s.shouldRun(group) {
			continue
		}
		if err := s.startGroup(ctx, group); err != nil {
			// Launch failure leaves remaining stages unrun and records exit -1.
			if !errors.Is(err, context.Canceled) {
				s.recordLaunchFailure(group, err)
			}
			return
		}
		s.waitGroup(ctx, group)
	}
}

// shouldRun evaluates whether connector conditions permit running the group.
func (s *sequenceRun) shouldRun(group StageGroup) bool {
	if s.timeout != nil || s.commitErr != nil || s.stopped() {
		return false
	}
	switch group.Connector {
	case argv.ConnectorAnd:
		return s.lastExit == 0
	case argv.ConnectorOr:
		return s.lastExit != 0
	default:
		return true
	}
}

func (s *sequenceRun) startGroup(ctx context.Context, group StageGroup) error {
	s.mu.Lock()
	if s.killed {
		s.mu.Unlock()
		return context.Canceled
	}
	s.mu.Unlock()

	stages := s.stages[group.Start:group.End]
	files, err := openStageFiles(s.opts.Redirect, stages)
	if err != nil {
		return err
	}
	wired, err := wirePipelineStages(ctx, stages, s.opts, s.call, files)
	if err != nil {
		files.close()
		return err
	}
	for i := range wired {
		wired[i].cmd.WaitDelay = s.waitDelay
	}
	if err := s.wireStdin(group, stages, wired, files); err != nil {
		releaseWired(wired)
		files.close()
		return err
	}

	s.mu.Lock()
	if s.killed {
		s.mu.Unlock()
		releaseWired(wired)
		files.close()
		return context.Canceled
	}
	s.live, s.liveFiles = wired, files
	s.mu.Unlock()

	if err := startWiredStages(wired); err != nil {
		s.mu.Lock()
		s.live, s.liveFiles = nil, nil
		s.mu.Unlock()
		releaseWired(wired)
		files.close()
		return err
	}
	for i := group.Start; i < group.End; i++ {
		s.skipped[i] = false
	}
	return nil
}

// wireStdin feeds each stage its own redirection, the previous stage's pipe,
// or, for the plan's first stage, the call's stdin.
func (s *sequenceRun) wireStdin(group StageGroup, stages []Stage, wired []wiredStage, files *stageFiles) error {
	for i, stage := range stages {
		source := stage.Streams.Stdin
		switch {
		case source.Null:
			wired[i].cmd.Stdin = nil
		case source.Path != "":
			file, err := files.openStdin(source.Path)
			if err != nil {
				return err
			}
			wired[i].cmd.Stdin = file
		case i > 0:
			wired[i].cmd.Stdin = wired[i-1].stdout
		case group.Start == 0 && s.stdin != nil:
			wired[i].cmd.Stdin = s.stdin
		}
	}
	return nil
}

func (s *sequenceRun) waitGroup(ctx context.Context, group StageGroup) {
	s.mu.Lock()
	wired, files := s.live, s.liveFiles
	s.mu.Unlock()
	if len(wired) == 0 {
		return
	}

	timedOut, waitErrs := waitWiredStages(ctx, wired)
	copy(s.waitErrs[group.Start:group.End], waitErrs)
	if timedOut {
		s.timeout = commandTimeout(ctx, s.started)
	}
	s.lastExit = s.groupExit(group)

	s.mu.Lock()
	s.live, s.liveFiles = nil, nil
	s.mu.Unlock()
	releaseWired(wired)
	// The commit outlives the run deadline so output written before it still lands.
	if err := files.commit(context.WithoutCancel(ctx)); err != nil {
		// A group whose output did not land failed, whatever its exit status.
		s.commitErr = fmt.Errorf("%w: %w", ErrRedirectCommit, err)
		s.lastExit = -1
		_, _ = fmt.Fprintf(s.call.stderr, "%v\n", s.commitErr)
	}
	files.close()
}

// recordLaunchFailure marks an unstarted group with a non-zero failure exit
// and keeps the launch error for the run result.
func (s *sequenceRun) recordLaunchFailure(group StageGroup, err error) {
	for i := group.Start; i < group.End; i++ {
		s.skipped[i] = false
		s.waitErrs[i] = fmt.Errorf("%w: %w", ErrStageNotLaunched, err)
	}
	s.lastExit = -1
}

// groupExit returns the exit code of the group's first failed stage.
func (s *sequenceRun) groupExit(group StageGroup) int {
	for i := group.Start; i < group.End; i++ {
		if s.stageFailed(group, i) {
			return exitCodeFromWait(s.waitErrs[i])
		}
	}
	return 0
}

// stageFailed reports a non-zero status, except SIGPIPE ending a stage whose
// reader finished first: that is a successful early-consumer exit.
func (s *sequenceRun) stageFailed(group StageGroup, i int) bool {
	if !group.Final(i) && killedBySIGPIPE(s.waitErrs[i]) {
		return false
	}
	return exitCodeFromWait(s.waitErrs[i]) != 0
}

func (s *sequenceRun) stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.killed
}

// terminate tears down the running group and prevents subsequent groups from starting.
func (s *sequenceRun) terminate() {
	s.mu.Lock()
	s.killed = true
	wired := s.live
	s.mu.Unlock()
	terminateWired(wired)
}

// ErrStageNotLaunched marks a sequenced stage whose process never started.
var ErrStageNotLaunched = errors.New("stage did not launch")

// ErrRedirectCommit marks redirected output that did not land.
var ErrRedirectCommit = errors.New("commit redirected output")

// finalize builds the execution result from recorded stage state.
func (s *sequenceRun) finalize(buf []byte, truncated bool, maxOut int) (*PipelineResult, error) {
	stageRuns := make([]StageRun, len(s.stages))
	for _, group := range s.groups {
		for i := group.Start; i < group.End; i++ {
			stage := s.stages[i]
			stageRuns[i] = StageRun{
				Command: stage.EchoLine(),
				Skipped: s.skipped[i],
			}
			if i > 0 {
				stageRuns[i].Connector = stage.Connector.OrPipe()
			}
			if !s.skipped[i] {
				code := exitCodeFromWait(s.waitErrs[i])
				stageRuns[i].ExitCode = &code
				stageRuns[i].Failed = s.stageFailed(group, i)
			}
		}
	}
	res := &PipelineResult{
		Stages:   stageRuns,
		ExitCode: s.lastExit,
		Output:   buf,
		TimedOut: s.timeout != nil,
	}
	if s.timeout != nil {
		return res, s.timeout
	}
	if s.commitErr != nil {
		return res, s.commitErr
	}
	// A stage that never started has no exit status to explain the failure.
	for _, err := range s.waitErrs {
		if errors.Is(err, ErrStageNotLaunched) {
			return res, err
		}
	}
	if truncated {
		return res, fmt.Errorf("%w at %d bytes", ErrOutputTruncated, maxOut)
	}
	if res.ExitCode != 0 {
		return res, nil
	}
	for i, err := range s.waitErrs {
		if s.skipped[i] || err == nil || errors.Is(err, osexec.ErrWaitDelay) {
			continue
		}
		var exitErr *osexec.ExitError
		if errors.As(err, &exitErr) {
			continue
		}
		return res, err
	}
	return res, nil
}
