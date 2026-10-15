package bialy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	execpkg "github.com/lycaon/lycaon/internal/exec"
)

// process is one running engine: requests go out on stdin as lines, replies
// come back on stdout matched by id.
type process struct {
	cmd     *exec.Cmd
	cleanup func()
	stdin   io.WriteCloser
	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[int64]chan response
	done      chan struct{}
	exitErr   error
	exitCode  int
}

type request struct {
	ID         int64                      `json:"id"`
	Method     string                     `json:"method"`
	Head       decide.Head                `json:"head,omitempty"`
	State      any                        `json:"state,omitempty"`
	Questions  map[string]decide.Question `json:"questions,omitempty"`
	Task       string                     `json:"task,omitempty"`
	Candidates []string                   `json:"candidates,omitempty"`
}

type response struct {
	ID      int64          `json:"id"`
	Answers decide.Answers `json:"answers,omitempty"`
	Scores  []float64      `json:"scores,omitempty"`
	Engine  *decide.Engine `json:"engine,omitempty"`
	Error   string         `json:"error,omitempty"`
}

func startProcess(ctx context.Context, bin string, args []string) (*process, error) {
	opts := execpkg.ExecOpts{
		Launch:    execpkg.HostLaunch("decide_engine"),
		NoTimeout: true,
	}
	cmd, cleanup, err := execpkg.PrepareCommand(context.WithoutCancel(ctx), bin, args, opts)
	if err != nil {
		return nil, fmt.Errorf("decide: prepare engine: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("decide: engine stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("decide: engine stdout: %w", err)
	}
	cmd.Stderr = stderrLogger{}
	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, fmt.Errorf("decide: start engine: %w", err)
	}
	untrack := execpkg.TrackProcess(cmd.Process.Pid)
	proc := &process{
		cmd: cmd,
		cleanup: func() {
			untrack()
			cleanup()
		},
		stdin:   stdin,
		pending: make(map[int64]chan response),
		done:    make(chan struct{}),
	}
	go proc.read(stdout)
	return proc, nil
}

var errEngineExited = errors.New("decide: engine exited")

func (p *process) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var resp response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			log.Warn("decision engine wrote an unreadable line", "error", err)
			continue
		}
		p.pendingMu.Lock()
		ch := p.pending[resp.ID]
		delete(p.pending, resp.ID)
		p.pendingMu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
	p.exitErr = errors.Join(errEngineExited, scanner.Err())
	// Reaped before done closes, so a waiter on done reads the exit status.
	_ = p.cmd.Wait()
	if p.cmd.ProcessState != nil {
		p.exitCode = p.cmd.ProcessState.ExitCode()
	}
	close(p.done)
	p.pendingMu.Lock()
	for id, ch := range p.pending {
		delete(p.pending, id)
		close(ch)
	}
	p.pendingMu.Unlock()
	p.cleanup()
}

func (p *process) roundTrip(ctx context.Context, req request) (response, error) {
	if err := ctx.Err(); err != nil {
		return response{}, err
	}
	select {
	case <-p.done:
		return response{}, p.exitErr
	default:
	}
	ch := make(chan response, 1)
	p.pendingMu.Lock()
	p.pending[req.ID] = ch
	p.pendingMu.Unlock()
	raw, err := json.Marshal(req)
	if err != nil {
		p.drop(req.ID)
		return response{}, fmt.Errorf("decide: encode request: %w", err)
	}
	raw = append(raw, '\n')
	p.writeMu.Lock()
	_, err = p.stdin.Write(raw)
	p.writeMu.Unlock()
	if err != nil {
		p.drop(req.ID)
		return response{}, errors.Join(errEngineExited, err)
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return response{}, p.exitErr
		}
		if err := ctx.Err(); err != nil {
			return response{}, err
		}
		return resp, nil
	case <-p.done:
		return response{}, p.exitErr
	case <-ctx.Done():
		p.drop(req.ID)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return response{}, decide.ErrDeadline
		}
		return response{}, ctx.Err()
	}
}

func (p *process) drop(id int64) {
	p.pendingMu.Lock()
	delete(p.pending, id)
	p.pendingMu.Unlock()
}

func (p *process) stop() {
	_ = p.stdin.Close()
	select {
	case <-p.done:
		return
	case <-time.After(2 * time.Second):
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	<-p.done
}

// stderrLogger forwards engine diagnostics line by line.
type stderrLogger struct{}

func (stderrLogger) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			log.Debug("decision engine", "stderr", line)
		}
	}
	return len(b), nil
}
