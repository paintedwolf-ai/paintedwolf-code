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
	"github.com/lycaon/lycaon/internal/observability"
)

var log = observability.LazyComponent("decide")

const (
	methodHello  = "hello"
	methodDecide = "decide"
	methodRank   = "rank"
)

// startTimeout bounds the first handshake, which loads model weights.
const startTimeout = 90 * time.Second

// Client serializes concurrent requests through one supervised engine process.
type Client struct {
	cfg Config

	mu      sync.Mutex
	proc    *process
	seq     atomic.Int64
	started bool
	engine  decide.Engine
	// warmErr is the last handshake failure; cleared once an engine answers.
	warmErr error
}

// Reason says why the engine cannot answer; empty when it can.
type Reason string

const (
	ReasonDisabled      Reason = "disabled"
	ReasonBinaryMissing Reason = "binary_missing"
	ReasonModelMissing  Reason = "model_missing"
	ReasonUnusable      Reason = "unusable"
)

// Status reports whether the engine can answer, and why not.
func (c *Client) Status() Reason {
	if c == nil || c.cfg.Disabled {
		return ReasonDisabled
	}
	if c.cfg.Resolve() == "" {
		return ReasonBinaryMissing
	}
	if !modelReady(c.cfg.ModelDir) {
		return ReasonModelMissing
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.warmErr != nil && !c.started {
		return ReasonUnusable
	}
	return ""
}

// Config returns the configuration the client runs with.
func (c *Client) Config() Config {
	if c == nil {
		return Config{}
	}
	return c.cfg
}

type process struct {
	cmd     *exec.Cmd
	cleanup func()
	stdin   io.WriteCloser
	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[int64]chan response
	done      chan struct{}
	exitErr   error
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

// New builds a client; the engine starts on first use or Warm.
func New(cfg Config) *Client {
	return &Client{cfg: cfg}
}

// Available reports whether the engine executable and its checkpoint are
// installed and the engine is not disabled.
func (c *Client) Available() bool {
	return c != nil && c.cfg.Resolve() != "" && modelReady(c.cfg.ModelDir)
}

// Engine reports the identity learned from the handshake, once started.
func (c *Client) Engine() decide.Engine {
	if c == nil {
		return decide.Engine{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.engine
}

// Loaded reports whether the handshake listed head among the loaded heads;
// before the handshake nothing is.
func (c *Client) Loaded(head decide.Head) bool {
	return c.Engine().Loaded(head)
}

// Warm starts the engine and completes the handshake so the first decision
// does not pay for weight loading.
func (c *Client) Warm(ctx context.Context) error {
	_, err := c.ensure(ctx)
	c.mu.Lock()
	c.warmErr = err
	c.mu.Unlock()
	return err
}

// Decide answers every question over state in one engine call of head.
func (c *Client) Decide(ctx context.Context, head decide.Head, state any, questions map[string]decide.Question) (decide.Result, error) {
	if len(questions) == 0 {
		return decide.Result{Engine: c.Engine()}, nil
	}
	started := time.Now()
	resp, err := c.call(ctx, request{Method: methodDecide, Head: head, State: state, Questions: questions})
	if err != nil {
		return decide.Result{}, err
	}
	for id := range questions {
		if _, ok := resp.Answers[id]; !ok {
			return decide.Result{}, fmt.Errorf("%w: no answer for %q", decide.ErrEngine, id)
		}
	}
	return decide.Result{Answers: resp.Answers, Engine: c.Engine(), Elapsed: time.Since(started)}, nil
}

// Rank scores candidates against task with the named head.
func (c *Client) Rank(ctx context.Context, head decide.Head, task string, candidates []string) ([]float64, decide.Engine, error) {
	if len(candidates) == 0 {
		return nil, c.Engine(), nil
	}
	resp, err := c.call(ctx, request{Method: methodRank, Head: head, Task: task, Candidates: candidates})
	if err != nil {
		return nil, decide.Engine{}, err
	}
	if len(resp.Scores) != len(candidates) {
		return nil, decide.Engine{}, fmt.Errorf("%w: %d scores for %d candidates", decide.ErrEngine, len(resp.Scores), len(candidates))
	}
	return resp.Scores, c.Engine(), nil
}

// Close stops the engine process.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc != nil {
		c.proc.stop()
		c.proc = nil
	}
	c.started = false
	return nil
}

func (c *Client) call(ctx context.Context, req request) (response, error) {
	if err := ctx.Err(); err != nil {
		return response{}, err
	}
	proc, err := c.ensure(ctx)
	if err != nil {
		return response{}, err
	}
	req.ID = c.seq.Add(1)
	resp, err := proc.roundTrip(ctx, req)
	if err != nil {
		if errors.Is(err, errEngineExited) {
			c.forget(proc)
		}
		return response{}, err
	}
	if resp.Error != "" {
		return response{}, fmt.Errorf("%w: %s", decide.ErrEngine, resp.Error)
	}
	return resp, nil
}

func (c *Client) forget(proc *process) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc == proc {
		c.proc = nil
		c.started = false
	}
}

func (c *Client) ensure(ctx context.Context) (*process, error) {
	if c == nil {
		return nil, decide.ErrUnavailable
	}
	if c.cfg.Disabled {
		return nil, decide.ErrDisabled
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started && c.proc != nil {
		return c.proc, nil
	}
	bin := c.cfg.Resolve()
	if bin == "" {
		return nil, decide.ErrUnavailable
	}
	if !modelReady(c.cfg.ModelDir) {
		return nil, fmt.Errorf("%w: checkpoint not installed at %q", decide.ErrUnavailable, c.cfg.ModelDir)
	}
	proc, err := startProcess(ctx, bin, c.cfg.Args())
	if err != nil {
		return nil, err
	}
	helloCtx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	hello, err := proc.roundTrip(helloCtx, request{ID: c.seq.Add(1), Method: methodHello})
	if err != nil {
		proc.stop()
		return nil, fmt.Errorf("decide: engine handshake: %w", err)
	}
	if hello.Engine == nil {
		proc.stop()
		return nil, fmt.Errorf("%w: handshake carried no engine identity", decide.ErrEngine)
	}
	c.engine = *hello.Engine
	c.proc = proc
	c.started = true
	log.Info("decision engine ready", "engine", c.engine.Name, "base_model", c.engine.Model, "device", c.engine.Device, "head", c.engine.Head)
	return proc, nil
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
	close(p.done)
	p.pendingMu.Lock()
	for id, ch := range p.pending {
		delete(p.pending, id)
		close(ch)
	}
	p.pendingMu.Unlock()
	_ = p.cmd.Wait()
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
