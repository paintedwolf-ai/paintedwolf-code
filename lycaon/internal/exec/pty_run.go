package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// RunPTY runs a bounded command through a pseudo-terminal.
func RunPTY(ctx context.Context, name string, args []string, opts PTYOpts) ([]byte, int, error) {
	if err := validatePTYCommand(name); err != nil {
		return nil, -1, err
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
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	session, err := StartPTY(runCtx, name, args, opts)
	if err != nil {
		return nil, -1, err
	}
	defer func() { _ = session.Close() }()

	var buf bytes.Buffer
	capture := &limitedWriter{w: &buf, limit: maxOut}

	copyDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(capture, session)
		copyDone <- copyErr
	}()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- session.Wait()
	}()

	var waitErr error
	timedOut := false
	select {
	case waitErr = <-waitDone:
		cancel()
		<-copyDone
	case <-runCtx.Done():
		timedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
		session.Kill()
		waitErr = <-waitDone
		<-copyDone
	}

	exitCode := exitCodeFromWait(waitErr)
	if timedOut {
		return buf.Bytes(), -1, fmt.Errorf("%w after %v", ErrTimeout, timeout)
	}
	if capture.truncated {
		return buf.Bytes(), exitCode, fmt.Errorf("%w at %d bytes", ErrOutputTruncated, maxOut)
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return buf.Bytes(), exitCode, nil
		}
		if exitCode < 0 {
			return buf.Bytes(), -1, waitErr
		}
	}
	return buf.Bytes(), exitCode, nil
}
