package mcp

import (
	"context"
	"io"
	osexec "os/exec"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	execpkg "github.com/lycaon/lycaon/internal/exec"
)

const stdioTerminateGrace = 5 * time.Second

// stdioTransport starts an MCP stdio server under an exec session guard so the
// guard snapshots surviving session members before the leader is reaped.
type stdioTransport struct {
	cmd   *osexec.Cmd
	grace time.Duration

	mu      sync.Mutex
	process *stdioProcessPipe
}

func (t *stdioTransport) Connect(ctx context.Context) (sdkmcp.Connection, error) {
	stdout, err := t.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := t.cmd.StdinPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, err
	}
	kill, wait, err := execpkg.StartInOwnGroup(t.cmd)
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	grace := t.grace
	if grace <= 0 {
		grace = stdioTerminateGrace
	}
	proc := &stdioProcessPipe{
		stdin: stdin,
		kill:  kill,
		wait:  wait,
		grace: grace,
	}
	t.mu.Lock()
	t.process = proc
	t.mu.Unlock()
	ioTransport := &sdkmcp.IOTransport{
		Reader: io.NopCloser(stdout),
		Writer: proc,
	}
	return ioTransport.Connect(ctx)
}

func (t *stdioTransport) abort() {
	t.mu.Lock()
	proc := t.process
	t.mu.Unlock()
	if proc == nil {
		return
	}
	proc.kill()
	_ = proc.Close()
}

func (t *stdioTransport) close() error {
	t.mu.Lock()
	proc := t.process
	t.mu.Unlock()
	if proc == nil {
		return nil
	}
	return proc.Close()
}

type stdioProcessPipe struct {
	stdin     io.WriteCloser
	kill      func()
	wait      func() error
	grace     time.Duration
	closeOnce sync.Once
	closeErr  error
}

func (p *stdioProcessPipe) Write(b []byte) (int, error) {
	return p.stdin.Write(b)
}

func (p *stdioProcessPipe) Close() error {
	p.closeOnce.Do(func() {
		_ = p.stdin.Close()
		done := make(chan error, 1)
		go func() {
			done <- p.wait()
		}()
		timer := time.NewTimer(p.grace)
		defer timer.Stop()
		select {
		case p.closeErr = <-done:
			return
		case <-timer.C:
			p.kill()
			p.closeErr = <-done
		}
	})
	return p.closeErr
}
