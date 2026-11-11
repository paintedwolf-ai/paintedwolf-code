package bialy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/inference"
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

// exitNoGPU is the engine's exit status when this machine has no device it runs
// on: on Linux, no hardware Vulkan GPU (EXIT_NO_GPU in native/src/protocol.rs).
const exitNoGPU = 4

// ErrNoGPU reports that this machine has no GPU the engine runs on. Like
// ErrNoCapableDevice it holds for the life of the host, and decisions fall back.
var ErrNoGPU = fmt.Errorf("%w: no GPU the engine runs on", decide.ErrUnavailable)

// Client serializes concurrent requests through one supervised engine process.
type Client struct {
	cfg Config

	mu      sync.Mutex
	proc    *process
	seq     atomic.Int64
	started bool
	engine  decide.Engine
	// starting is closed when the start in flight finishes; nil when none is.
	starting    chan struct{}
	startCancel context.CancelFunc
	startErr    error
	// generation advances on Close, so a start that finishes after it stops its
	// own process instead of installing it.
	generation int
	incapable  bool
	noGPU      bool
	// fault is the last handshake failure the hardware does not explain, and
	// faultHold is when the engine may launch again; faults in a row lengthen
	// the hold. Both clear once an engine answers.
	fault     error
	faultHold time.Time
	faults    int
	now       func() time.Time
}

const (
	// faultHoldFirst is the hold after one handshake fault.
	faultHoldFirst = 30 * time.Second
	// faultHoldMax caps the hold however many faults follow.
	faultHoldMax = 10 * time.Minute
)

// faultHoldFor doubles the hold with each fault in a row, up to the cap.
func faultHoldFor(faults int) time.Duration {
	hold := faultHoldFirst
	for i := 1; i < faults && hold < faultHoldMax; i++ {
		hold *= 2
	}
	return min(hold, faultHoldMax)
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// SetClockForTest replaces the clock the relaunch hold is measured on.
func (c *Client) SetClockForTest(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// Reason says why the engine cannot answer; empty when it can.
type Reason string

const (
	ReasonDisabled        Reason = "disabled"
	ReasonBinaryMissing   Reason = "binary_missing"
	ReasonModelMissing    Reason = "model_missing"
	ReasonNoCapableDevice Reason = "no_capable_device"
	ReasonNoGPU           Reason = "no_gpu"
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
	if c.noGPU {
		return ReasonNoGPU
	}
	if c.incapable {
		return ReasonNoCapableDevice
	}
	if c.fault != nil && !c.started {
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

// Speed is the reference batch time the engine measured when it chose its
// device; zero before the handshake or for a pinned device.
func (c *Client) Speed() time.Duration {
	return time.Duration(c.Engine().ProbeMS * float64(time.Millisecond))
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
	return err
}

// Close stops the engine process.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	proc, starting, cancel := c.proc, c.starting, c.startCancel
	c.proc = nil
	c.started = false
	c.generation++
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if proc != nil {
		proc.stop()
	}
	if starting != nil {
		<-starting
	}
	return nil
}

func (c *Client) exchange(ctx context.Context, proc *process, req request) (response, error) {
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
	if c.noGPU {
		c.mu.Unlock()
		return nil, ErrNoGPU
	}
	if c.incapable {
		c.mu.Unlock()
		return nil, ErrNoCapableDevice
	}
	if c.starting == nil && c.fault != nil && c.clock().Before(c.faultHold) {
		err := c.fault
		c.mu.Unlock()
		return nil, err
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
		startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), startTimeout)
		c.startCancel = cancel
		go func(done chan struct{}, generation int) {
			defer cancel()
			c.start(startCtx, bin, done, generation)
		}(c.starting, c.generation)
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
	c.startCancel = nil
	if generation != c.generation {
		if proc != nil {
			proc.stop()
		}
		c.startErr = fmt.Errorf("%w: the client closed during start", decide.ErrUnavailable)
		return
	}
	c.startErr = err
	if errors.Is(err, ErrNoGPU) {
		c.noGPU = true
		log.Info("decision engine has no GPU to run on; decisions fall back")
		return
	}
	if errors.Is(err, ErrNoCapableDevice) {
		c.incapable = true
		log.Info("decision engine has no device fast enough; decisions fall back")
		return
	}
	if err != nil {
		c.faults++
		c.fault = err
		hold := faultHoldFor(c.faults)
		c.faultHold = c.clock().Add(hold)
		var exit *EngineExitError
		if errors.As(err, &exit) {
			log.Warn("decision engine exited during handshake; decisions fall back", "status", exit.Status, "stderr", exit.Stderr, "faults", c.faults, "hold", hold)
		} else {
			log.Warn("decision engine handshake failed; decisions fall back", "error", err, "faults", c.faults, "hold", hold)
		}
		return
	}
	c.fault, c.faults = nil, 0
	c.engine = engine
	c.proc = proc
	c.started = true
	log.Info("decision engine ready", "engine", engine.Name, "base_model", engine.Model, "device", engine.Device, "adapter", engine.Adapter, "head", engine.Head)
}

func (c *Client) handshake(ctx context.Context, bin string) (*process, decide.Engine, error) {
	grant, err := c.cfg.Resources.acquire(ctx)
	if err != nil {
		return nil, decide.Engine{}, err
	}
	proc, err := startProcess(ctx, bin, c.cfg.Args(), grant)
	if err != nil {
		grant.release()
		return nil, decide.Engine{}, err
	}
	helloCtx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	hello, err := proc.roundTrip(helloCtx, request{ID: c.seq.Add(1), Method: methodHello})
	if err != nil {
		proc.stop()
		switch proc.exitCode {
		case exitNoCapableDevice:
			return nil, decide.Engine{}, ErrNoCapableDevice
		case exitNoGPU:
			return nil, decide.Engine{}, ErrNoGPU
		}
		return nil, decide.Engine{}, fmt.Errorf("decide: engine handshake: %w", err)
	}
	if hello.Engine == nil {
		proc.stop()
		return nil, decide.Engine{}, fmt.Errorf("%w: handshake carried no engine identity", decide.ErrEngine)
	}
	if grant != nil {
		device, err := c.cfg.Resources.Devices.Bind(hello.PhysicalDevice)
		if err != nil || device != grant.device {
			proc.stop()
			return nil, decide.Engine{}, fmt.Errorf("decide: runtime physical device mismatch: %w", inference.ErrIdentity)
		}
		proc.device = device
		grant.permit.Complete()
	}
	return proc, *hello.Engine, nil
}
