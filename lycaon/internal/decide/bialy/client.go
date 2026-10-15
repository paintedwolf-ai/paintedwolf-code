package bialy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/observability"
)

var log = observability.LazyComponent("decide")

const (
	methodHello  = "hello"
	methodDecide = "decide"
	methodRank   = "rank"
)

// startTimeout bounds one engine start. A first launch measures every device
// and compiles GPU kernels, which can take minutes; later launches reuse the
// cached verdict and kernels.
const startTimeout = 5 * time.Minute

// exitNoCapableDevice is the engine's exit status when no device meets the
// speed requirement (EXIT_NO_CAPABLE_DEVICE in native/src/protocol.rs).
const exitNoCapableDevice = 3

// ErrNoCapableDevice reports that no device on this machine runs the engine
// fast enough. The hardware does not change while the host runs, so the engine
// is not started again.
var ErrNoCapableDevice = fmt.Errorf("%w: no device meets the speed requirement", decide.ErrUnavailable)

// Client serializes concurrent requests through one supervised engine process.
type Client struct {
	cfg Config

	mu      sync.Mutex
	proc    *process
	seq     atomic.Int64
	started bool
	engine  decide.Engine
	// starting is closed when the start in flight finishes; nil when none is.
	starting chan struct{}
	startErr error
	// generation advances on Close, so a start that finishes after it stops its
	// own process instead of installing it.
	generation int
	incapable  bool
	// warmErr is the last handshake failure; cleared once an engine answers.
	warmErr error
}

// Reason says why the engine cannot answer; empty when it can.
type Reason string

const (
	ReasonDisabled        Reason = "disabled"
	ReasonBinaryMissing   Reason = "binary_missing"
	ReasonModelMissing    Reason = "model_missing"
	ReasonNoCapableDevice Reason = "no_capable_device"
	ReasonUnusable        Reason = "unusable"
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
	if c.incapable {
		return ReasonNoCapableDevice
	}
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
	c.generation++
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

// ensure returns the running engine, starting it when none runs. The start
// belongs to the client, not the caller: a caller whose context ends stops
// waiting, and the start carries on for the next one.
func (c *Client) ensure(ctx context.Context) (*process, error) {
	if c == nil {
		return nil, decide.ErrUnavailable
	}
	if c.cfg.Disabled {
		return nil, decide.ErrDisabled
	}
	c.mu.Lock()
	if c.started && c.proc != nil {
		proc := c.proc
		c.mu.Unlock()
		return proc, nil
	}
	if c.incapable {
		c.mu.Unlock()
		return nil, ErrNoCapableDevice
	}
	if c.starting == nil {
		bin := c.cfg.Resolve()
		if bin == "" {
			c.mu.Unlock()
			return nil, decide.ErrUnavailable
		}
		if !modelReady(c.cfg.ModelDir) {
			c.mu.Unlock()
			return nil, fmt.Errorf("%w: checkpoint not installed at %q", decide.ErrUnavailable, c.cfg.ModelDir)
		}
		c.starting = make(chan struct{})
		go c.start(context.WithoutCancel(ctx), bin, c.starting, c.generation)
	}
	starting := c.starting
	c.mu.Unlock()

	select {
	case <-starting:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, decide.ErrDeadline
		}
		return nil, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started && c.proc != nil {
		return c.proc, nil
	}
	return nil, c.startErr
}

// start launches the engine and completes its handshake, then installs it
// unless the client closed meanwhile.
func (c *Client) start(ctx context.Context, bin string, done chan struct{}, generation int) {
	defer close(done)
	proc, engine, err := c.handshake(ctx, bin)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starting = nil
	if generation != c.generation {
		if proc != nil {
			proc.stop()
		}
		c.startErr = fmt.Errorf("%w: the client closed during start", decide.ErrUnavailable)
		return
	}
	c.startErr = err
	if errors.Is(err, ErrNoCapableDevice) {
		c.incapable = true
		log.Info("decision engine has no device fast enough; decisions fall back")
		return
	}
	if err != nil {
		return
	}
	c.engine = engine
	c.proc = proc
	c.started = true
	log.Info("decision engine ready", "engine", engine.Name, "base_model", engine.Model, "device", engine.Device, "adapter", engine.Adapter, "head", engine.Head)
}

func (c *Client) handshake(ctx context.Context, bin string) (*process, decide.Engine, error) {
	proc, err := startProcess(ctx, bin, c.cfg.Args())
	if err != nil {
		return nil, decide.Engine{}, err
	}
	helloCtx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	hello, err := proc.roundTrip(helloCtx, request{ID: c.seq.Add(1), Method: methodHello})
	if err != nil {
		proc.stop()
		if proc.exitCode == exitNoCapableDevice {
			return nil, decide.Engine{}, ErrNoCapableDevice
		}
		return nil, decide.Engine{}, fmt.Errorf("decide: engine handshake: %w", err)
	}
	if hello.Engine == nil {
		proc.stop()
		return nil, decide.Engine{}, fmt.Errorf("%w: handshake carried no engine identity", decide.ErrEngine)
	}
	return proc, *hello.Engine, nil
}
