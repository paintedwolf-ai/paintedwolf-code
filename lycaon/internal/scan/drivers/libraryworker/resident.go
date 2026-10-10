package libraryworker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
)

// resident is one live worker process and its pipes.
type resident struct {
	pipeline    *exec.AsyncPipeline
	stdin       *io.PipeWriter
	stdout      *bufio.Reader
	output      *io.PipeReader
	stderr      *tailBuffer
	done        <-chan struct{}
	pipesClosed <-chan struct{}
}

func startResident(ctx context.Context, priority exec.ProcessPriority) (*resident, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve scan worker executable: %w", err)
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	stderr := &tailBuffer{limit: stderrTail}
	// Requests share this process; its working directory outlives individual inputs.
	pipeline, err := exec.StartPipelineAsync(context.WithoutCancel(ctx), []exec.Stage{{Name: executable, Args: []string{"internal-scan-worker"}}}, exec.ExecOpts{
		Launch: exec.HostLaunch("library scanner worker"), Dir: os.TempDir(), NoTimeout: true,
		MaxOutputBytes: exec.DefaultMaxScanOutputBytes, Stdin: &exec.StdinSpec{Reader: stdinReader},
		ProcessPriority: priority,
	}, stdoutWriter, stderr)
	if err != nil {
		_ = stdinWriter.Close()
		_ = stdoutWriter.Close()
		return nil, fmt.Errorf("start library scan worker: %w", err)
	}
	pipesClosed := make(chan struct{})
	worker := &resident{pipeline: pipeline, stdin: stdinWriter, stdout: bufio.NewReaderSize(stdoutReader, 64<<10), output: stdoutReader, stderr: stderr, done: pipeline.Done(), pipesClosed: pipesClosed}
	go func() {
		defer close(pipesClosed)
		<-pipeline.Done()
		// Unblock pending exchanges when the worker exits.
		_ = stdinWriter.Close()
		_ = stdoutWriter.Close()
	}()
	return worker, nil
}

// exchange writes one request line and reads one response line.
func (w *resident) exchange(ctx context.Context, payload []byte) ([]byte, error) {
	type answer struct {
		line []byte
		err  error
	}
	reply := make(chan answer, 1)
	go func() {
		if _, err := w.stdin.Write(append(payload, '\n')); err != nil {
			reply <- answer{err: fmt.Errorf("write request: %w", err)}
			return
		}
		line, err := w.stdout.ReadBytes('\n')
		if errors.Is(err, io.EOF) && len(line) > 0 {
			err = nil
		}
		if err != nil {
			err = fmt.Errorf("read response: %w", err)
		}
		reply <- answer{line: line, err: err}
	}()
	select {
	case a := <-reply:
		return a.line, a.err
	case <-ctx.Done():
		w.stop()
		<-reply
		return nil, ctx.Err()
	}
}

func (w *resident) exited() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

// stop allows two seconds for shutdown before killing the worker.
func (w *resident) stop() {
	_ = w.stdin.Close()
	_ = w.output.Close()
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		w.pipeline.Kill()
		<-w.done
	}
	if w.pipesClosed != nil {
		<-w.pipesClosed
	}
}

func (w *resident) stderrBytes() []byte { return w.stderr.bytes() }
