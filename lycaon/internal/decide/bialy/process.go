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
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/inference"
)

// process is one running engine: requests go out on stdin as lines, replies
// come back on stdout matched by id. One request is admitted at a time.
type process struct {
	cmd      *exec.Cmd
	cleanup  func()
	stdin    io.WriteCloser
	admitted chan struct{}

	pendingMu  sync.Mutex
	pending    map[int64]*pendingRequest
	done       chan struct{}
	exitErr    error
	exitCode   int
	device     *inference.Device
	generation uint64
	lifetime   context.Context
	reaped     context.CancelFunc
	startup    *startupGrant
}

var processSequence atomic.Uint64

type pendingRequest struct {
	response chan response
	permit   *inference.Permit
}

func (p *pendingRequest) complete() {
	if p.permit != nil {
		p.permit.Complete()
	}
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
	ID             int64                    `json:"id"`
	Answers        decide.Answers           `json:"answers,omitempty"`
	Scores         []float64                `json:"scores,omitempty"`
	Engine         *decide.Engine           `json:"engine,omitempty"`
	Error          string                   `json:"error,omitempty"`
	PhysicalDevice inference.PhysicalDevice `json:"physical_device"`
}

func startProcess(ctx context.Context, bin string, args []string, grant *startupGrant) (*process, error) {
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
	if err := execpkg.StartPrepared(ctx, cmd); err != nil {
		cleanup()
		return nil, fmt.Errorf("decide: start engine: %w", err)
	}
	lifetime, reaped := context.WithCancel(context.Background())
	proc := &process{
		lifetime: lifetime, reaped: reaped, generation: processSequence.Add(1),
		cmd: cmd,
		cleanup: func() {
			cleanup()
		},
		stdin:    stdin,
		admitted: make(chan struct{}, 1),
		pending:  make(map[int64]*pendingRequest),
		done:     make(chan struct{}),
	}
	proc.startup = grant
	if grant != nil {
		proc.generation = grant.generation
	}
	go proc.read(stdout)
	return proc, nil
}

var errEngineExited = errors.New("decide: engine exited")

func (p *process) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var protocolErr error
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var resp response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			protocolErr = fmt.Errorf("decide: invalid engine response: %w", err)
			break
		}
		p.pendingMu.Lock()
		ch := p.pending[resp.ID]
		delete(p.pending, resp.ID)
		p.pendingMu.Unlock()
		if ch != nil {
			<-p.admitted
			ch.complete()
			ch.response <- resp
		}
	}
	p.exitErr = errors.Join(errEngineExited, protocolErr, scanner.Err())
	// A closed response pipe cannot acknowledge outstanding physical work.
	p.kill()
	// Reaped before done closes, so a waiter on done reads the exit status.
	_ = p.cmd.Wait()
	if p.cmd.ProcessState != nil {
		p.exitCode = p.cmd.ProcessState.ExitCode()
	}
	p.reaped()
	p.startup.release()
	p.pendingMu.Lock()
	for id, ch := range p.pending {
		delete(p.pending, id)
		ch.complete()
		close(ch.response)
	}
	close(p.done)
	p.pendingMu.Unlock()
	p.cleanup()
}

func (p *process) roundTrip(ctx context.Context, req request) (response, error) {
	if err := ctx.Err(); err != nil {
		return response{}, err
	}
	select {
	case p.admitted <- struct{}{}:
	case <-p.done:
		return response{}, p.exitErr
	case <-ctx.Done():
		return response{}, ctx.Err()
	}
	// Cancellation and process exit can race with admission.
	if err := ctx.Err(); err != nil {
		<-p.admitted
		return response{}, err
	}
	select {
	case <-p.done:
		<-p.admitted
		return response{}, p.exitErr
	default:
	}
	permit, err := p.acquireDevice(ctx)
	if err != nil {
		<-p.admitted
		return response{}, err
	}
	ch := make(chan response, 1)
	p.pendingMu.Lock()
	select {
	case <-p.done:
		p.pendingMu.Unlock()
		if permit != nil {
			permit.Complete()
		}
		<-p.admitted
		return response{}, p.exitErr
	default:
	}
	p.pending[req.ID] = &pendingRequest{response: ch, permit: permit}
	p.pendingMu.Unlock()
	raw, err := json.Marshal(req)
	if err != nil {
		p.drop(req.ID)
		return response{}, fmt.Errorf("decide: encode request: %w", err)
	}
	raw = append(raw, '\n')
	_, err = p.stdin.Write(raw)
	if err != nil {
		// A partial write may already have submitted work.
		p.kill()
		<-p.done
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
		// Native work still owns admission until its matching response arrives.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return response{}, decide.ErrDeadline
		}
		return response{}, ctx.Err()
	}
}

func (p *process) drop(id int64) {
	p.pendingMu.Lock()
	if pending, ok := p.pending[id]; ok {
		pending.complete()
		delete(p.pending, id)
		<-p.admitted
	}
	p.pendingMu.Unlock()
}

func (p *process) stop() {
	_ = p.stdin.Close()
	select {
	case <-p.done:
		return
	case <-time.After(2 * time.Second):
	}
	p.kill()
	<-p.done
}

func (p *process) kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
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
