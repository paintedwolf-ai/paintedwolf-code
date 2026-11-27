package pythonruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	experimentalsys "github.com/tetratelabs/wazero/experimental/sys"
	experimentalsysfs "github.com/tetratelabs/wazero/experimental/sysfs"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

type procExitContextKey struct{}

type procExitTracker struct {
	called atomic.Bool
	code   atomic.Uint32
}

func newProcExitListenerFactory() experimental.FunctionListenerFactory {
	return experimental.FunctionListenerFactoryFunc(func(def api.FunctionDefinition) experimental.FunctionListener {
		if def.Name() != "proc_exit" {
			return nil
		}
		return experimental.FunctionListenerFunc(func(ctx context.Context, _ api.Module, _ api.FunctionDefinition, params []uint64, _ experimental.StackIterator) {
			if tr, ok := ctx.Value(procExitContextKey{}).(*procExitTracker); ok && tr != nil {
				tr.called.Store(true)
				if len(params) > 0 {
					tr.code.Store(uint32(params[0]))
				}
			}
		})
	})
}

var (
	// ErrParkedBudgetExceeded is returned when parking a guest awaiting
	// approval would exceed aggregate parked-guest admission limits.
	ErrParkedBudgetExceeded = errors.New("parked guest aggregate budget exceeded")
	// ErrGuestAlreadyParked is returned when the same execution ID is parked twice.
	ErrGuestAlreadyParked = errors.New("execution already parked")
)

// Clock supplies wall and monotonic time to WASI guests. Production uses
// SystemClock; deterministic fixtures use FixedClock.
type Clock interface {
	Now() time.Time
	MonotonicNanos() int64
	WallResolutionNanos() int64
	MonotonicResolutionNanos() int64
}

// SystemClock wires WASI clocks to the host OS wall and monotonic clocks.
type SystemClock struct {
	start time.Time
}

// NewSystemClock returns a SystemClock anchored at creation time for monotonic
// readings.
func NewSystemClock() *SystemClock {
	return &SystemClock{start: time.Now()}
}

func (c *SystemClock) Now() time.Time { return time.Now() }

func (c *SystemClock) MonotonicNanos() int64 {
	if c == nil || c.start.IsZero() {
		return time.Now().UnixNano()
	}
	return time.Since(c.start).Nanoseconds()
}

func (c *SystemClock) WallResolutionNanos() int64      { return 1000 }
func (c *SystemClock) MonotonicResolutionNanos() int64 { return 1 }

// FixedClock provides a controllable deterministic clock for test fixtures.
type FixedClock struct {
	mu        sync.Mutex
	wall      time.Time
	monotonic int64
}

// NewFixedClock returns a FixedClock initialized to wall.
func NewFixedClock(wall time.Time) *FixedClock {
	return &FixedClock{wall: wall.UTC()}
}

func (c *FixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

func (c *FixedClock) MonotonicNanos() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.monotonic
}

func (c *FixedClock) WallResolutionNanos() int64      { return 1 }
func (c *FixedClock) MonotonicResolutionNanos() int64 { return 1 }

// Advance moves both wall and monotonic clocks forward by d.
func (c *FixedClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.wall = c.wall.Add(d)
	c.monotonic += d.Nanoseconds()
	c.mu.Unlock()
}

// Entropy supplies random bytes to WASI random_get.
type Entropy interface {
	Reader() io.Reader
}

// CryptoEntropy wires WASI random_get to Go's cryptographic random source.
type CryptoEntropy struct{}

func (CryptoEntropy) Reader() io.Reader { return rand.Reader }

// DeterministicEntropy supplies repeatable pseudo-random bytes for fixtures.
type DeterministicEntropy struct {
	mu  sync.Mutex
	rng *mathrand.Rand
}

// NewDeterministicEntropy creates a DeterministicEntropy from seed.
func NewDeterministicEntropy(seed int64) *DeterministicEntropy {
	return &DeterministicEntropy{rng: mathrand.New(mathrand.NewSource(seed))}
}

func (e *DeterministicEntropy) Reader() io.Reader { return e }

func (e *DeterministicEntropy) Read(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rng.Read(p)
}

// ExecutionMode identifies how an Invocation launches Python.
type ExecutionMode string

const (
	ModeCode   ExecutionMode = "code"
	ModeScript ExecutionMode = "script"
	ModeModule ExecutionMode = "module"
	ModeStdin  ExecutionMode = "stdin"
	ModeCLI    ExecutionMode = "cli"
)

// ResourceLimits bounds memory, guest/host work, output, staging, and wall
// time for a single invocation.
type ResourceLimits struct {
	MaxMemoryPages uint32        `json:"max_memory_pages,omitempty"`
	MaxGuestWork   uint64        `json:"max_guest_work,omitempty"`
	MaxHostWork    uint64        `json:"max_host_work,omitempty"`
	MaxOutputBytes int64         `json:"max_output_bytes,omitempty"`
	MaxStagedBytes int64         `json:"max_staged_bytes,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`
}

// DefaultResourceLimits returns enforceable production defaults for a Portable
// Python invocation: 256 MiB linear memory, 50M guest fuel, 100k host ops,
// 64 KiB inline output, 16 MiB staged bytes, and 120s timeout.
func DefaultResourceLimits() ResourceLimits {
	return ResourceLimits{
		MaxMemoryPages: 4096,
		MaxGuestWork:   50_000_000,
		MaxHostWork:    100_000,
		MaxOutputBytes: 65536,
		MaxStagedBytes: 16 * 1024 * 1024,
		Timeout:        120 * time.Second,
	}
}

// ResourceUsage records observed resource consumption for an invocation.
type ResourceUsage struct {
	GuestWork       uint64        `json:"guest_work,omitempty"`
	HostWork        uint64        `json:"host_work,omitempty"`
	PeakMemoryPages uint32        `json:"peak_memory_pages,omitempty"`
	StdoutBytes     int64         `json:"stdout_bytes,omitempty"`
	StderrBytes     int64         `json:"stderr_bytes,omitempty"`
	StagedBytes     int64         `json:"staged_bytes,omitempty"`
	Duration        time.Duration `json:"duration"`
}

// ParkedGuestBudget caps aggregate resources retained by guests suspended
// awaiting human approval.
type ParkedGuestBudget struct {
	MaxParkedGuests        int   `json:"max_parked_guests,omitempty"`
	MaxRetainedMemoryBytes int64 `json:"max_retained_memory_bytes,omitempty"`
	MaxStagedBytes         int64 `json:"max_staged_bytes,omitempty"`
	MaxOpenDescriptors     int   `json:"max_open_descriptors,omitempty"`
	MaxReviewLeases        int   `json:"max_review_leases,omitempty"`
}

// ParkedGuestReservation describes the resources retained by one parked guest.
type ParkedGuestReservation struct {
	ExecutionID         string
	RetainedMemoryBytes int64
	StagedBytes         int64
	OpenDescriptors     int
	ReviewLeases        int
}

// ParkedGuestSnapshot reports current aggregate parked-guest resource usage.
type ParkedGuestSnapshot struct {
	ParkedGuests        int
	RetainedMemoryBytes int64
	StagedBytes         int64
	OpenDescriptors     int
	ReviewLeases        int
}

// ParkedGuestTracker accounts for retained module memory, staged bytes, open
// descriptors, and review leases while guests are suspended awaiting approval.
type ParkedGuestTracker struct {
	budget ParkedGuestBudget
	mu     sync.Mutex
	active map[string]ParkedGuestReservation
	usage  ParkedGuestSnapshot
}

// NewParkedGuestTracker constructs a tracker enforcing budget.
func NewParkedGuestTracker(budget ParkedGuestBudget) *ParkedGuestTracker {
	return &ParkedGuestTracker{
		budget: budget,
		active: make(map[string]ParkedGuestReservation),
	}
}

// Park reserves aggregate resources for a suspended guest.
func (t *ParkedGuestTracker) Park(res ParkedGuestReservation) error {
	if strings.TrimSpace(res.ExecutionID) == "" {
		return errors.New("parked guest reservation missing execution_id")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.active[res.ExecutionID]; exists {
		return ErrGuestAlreadyParked
	}
	if t.budget.MaxParkedGuests > 0 && t.usage.ParkedGuests+1 > t.budget.MaxParkedGuests {
		return fmt.Errorf("%w: parked guests %d > %d", ErrParkedBudgetExceeded, t.usage.ParkedGuests+1, t.budget.MaxParkedGuests)
	}
	if t.budget.MaxRetainedMemoryBytes > 0 && t.usage.RetainedMemoryBytes+res.RetainedMemoryBytes > t.budget.MaxRetainedMemoryBytes {
		return fmt.Errorf("%w: retained memory %d > %d", ErrParkedBudgetExceeded, t.usage.RetainedMemoryBytes+res.RetainedMemoryBytes, t.budget.MaxRetainedMemoryBytes)
	}
	if t.budget.MaxStagedBytes > 0 && t.usage.StagedBytes+res.StagedBytes > t.budget.MaxStagedBytes {
		return fmt.Errorf("%w: staged bytes %d > %d", ErrParkedBudgetExceeded, t.usage.StagedBytes+res.StagedBytes, t.budget.MaxStagedBytes)
	}
	if t.budget.MaxOpenDescriptors > 0 && t.usage.OpenDescriptors+res.OpenDescriptors > t.budget.MaxOpenDescriptors {
		return fmt.Errorf("%w: open descriptors %d > %d", ErrParkedBudgetExceeded, t.usage.OpenDescriptors+res.OpenDescriptors, t.budget.MaxOpenDescriptors)
	}
	if t.budget.MaxReviewLeases > 0 && t.usage.ReviewLeases+res.ReviewLeases > t.budget.MaxReviewLeases {
		return fmt.Errorf("%w: review leases %d > %d", ErrParkedBudgetExceeded, t.usage.ReviewLeases+res.ReviewLeases, t.budget.MaxReviewLeases)
	}

	t.active[res.ExecutionID] = res
	t.usage.ParkedGuests++
	t.usage.RetainedMemoryBytes += res.RetainedMemoryBytes
	t.usage.StagedBytes += res.StagedBytes
	t.usage.OpenDescriptors += res.OpenDescriptors
	t.usage.ReviewLeases += res.ReviewLeases
	return nil
}

// Release deterministically releases a parked guest's reservation on
// settlement, cancellation, or restart.
func (t *ParkedGuestTracker) Release(executionID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	res, ok := t.active[executionID]
	if !ok {
		return false
	}
	delete(t.active, executionID)
	t.usage.ParkedGuests--
	t.usage.RetainedMemoryBytes -= res.RetainedMemoryBytes
	t.usage.StagedBytes -= res.StagedBytes
	t.usage.OpenDescriptors -= res.OpenDescriptors
	t.usage.ReviewLeases -= res.ReviewLeases
	return true
}

// ReleaseAll clears all parked reservations (e.g. on host restart or shutdown).
func (t *ParkedGuestTracker) ReleaseAll() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(t.active)
	clear(t.active)
	t.usage = ParkedGuestSnapshot{}
	return n
}

// Snapshot returns current aggregate parked-guest usage.
func (t *ParkedGuestTracker) Snapshot() ParkedGuestSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usage
}

// Invocation configures one fresh guest execution.
type Invocation struct {
	ExecutionID string
	Actor       string
	View        string
	Surface     string
	Mode        ExecutionMode
	Code        string
	Script      string
	Module      string
	CLI         string
	CWD         string
	Args        []string
	Env         map[string]string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Limits         ResourceLimits
	Workspace      WorkspaceAdapter
	Observer       OperationObserver
	StopLatch      *StopLatch
	ExtraSysMounts map[string]experimentalsys.FS
	HTTPBridgePath string
}

// Validate checks that exactly one execution mode is configured with its
// required payload.
func (inv Invocation) Validate() error {
	switch inv.Mode {
	case ModeCode:
		if inv.Code == "" {
			return errors.New("invocation mode=code requires non-empty Code")
		}
	case ModeScript:
		if strings.TrimSpace(inv.Script) == "" {
			return errors.New("invocation mode=script requires non-empty Script")
		}
	case ModeModule:
		if strings.TrimSpace(inv.Module) == "" {
			return errors.New("invocation mode=module requires non-empty Module")
		}
	case ModeStdin:
		// Stdin may be empty (EOF).
	case ModeCLI:
		if strings.TrimSpace(inv.CLI) == "" {
			return errors.New("invocation mode=cli requires non-empty CLI")
		}
	default:
		return fmt.Errorf("unsupported invocation mode %q", inv.Mode)
	}
	return nil
}

// BuildArgv constructs the isolated CPython argv for inv.
func BuildArgv(inv Invocation) ([]string, error) {
	if err := inv.Validate(); err != nil {
		return nil, err
	}
	argv := []string{"python3", "-I", "-B"}
	switch inv.Mode {
	case ModeCode:
		argv = append(argv, "-c", inv.Code)
	case ModeScript:
		argv = append(argv, inv.Script)
	case ModeModule:
		argv = append(argv, "-m", inv.Module)
	case ModeStdin:
		argv = append(argv, "-")
	case ModeCLI:
		argv = append(argv, "-m", inv.CLI)
	}
	argv = append(argv, inv.Args...)
	return argv, nil
}

// ExecutionResult captures the trusted host outcome of one guest execution.
type ExecutionResult struct {
	ExecutionID     string            `json:"execution_id"`
	Backend         BackendKind       `json:"backend,omitempty"`
	EngineTag       string            `json:"engine_tag,omitempty"`
	ExitCode        int               `json:"exit_code"`
	CleanCompletion bool              `json:"clean_completion"`
	Stop            *TerminalStop     `json:"stop,omitempty"`
	Usage           ResourceUsage     `json:"usage"`
	Settlement      *SettlementReport `json:"settlement,omitempty"`
	GuestStderr     string            `json:"guest_stderr,omitempty"`
	Diagnostics     []string          `json:"diagnostics,omitempty"`
}

// Runtime executes isolated Portable Python invocations against a compiled
// immutable WASI image.
type Runtime interface {
	Execute(ctx context.Context, inv Invocation) (*ExecutionResult, error)
	Info() RuntimeInfo
	Manifest() *ImageManifest
	ImportAudit() *ImportAuditReport
	ParkedGuests() *ParkedGuestTracker
	Close(ctx context.Context) error
}

// RuntimeConfig configures a Portable Python execution backend.
type RuntimeConfig struct {
	Backend                 BackendKind
	EngineDir               string
	EngineManifestPath      string
	EngineManifest          *BundledEngineManifest
	WasmBytes               []byte
	ImageDir                string
	Manifest                *ImageManifest
	ImportPolicy            []WasmImportSpec
	EnforceImportAudit      bool
	UseTrustedBootstrap     bool
	ScratchRoot             string
	WorkspaceMount          string
	DefaultMemoryLimitPages uint32
	Clock                   Clock
	Entropy                 Entropy
	ParkedBudget            ParkedGuestBudget
}

// WazeroRuntime hosts an immutable compiled WASI module and instantiates a
// fresh guest per Execute call.
type WazeroRuntime struct {
	backend             BackendKind
	rt                  wazero.Runtime
	wasmBytes           []byte
	compiled            wazero.CompiledModule
	meteredOnce         sync.Once
	compiledMetered     wazero.CompiledModule
	compiledMeteredErr  error
	manifest            *ImageManifest
	imageDir            string
	scratchRoot         string
	workspaceMount      string
	useTrustedBootstrap bool
	audit               *ImportAuditReport
	clock               Clock
	entropy             Entropy
	parked              *ParkedGuestTracker
	memLimitPgs         uint32
}

// NewWazeroRuntime compiles cfg.WasmBytes once, audits its actual Wasm imports,
// and prepares the reusable immutable runtime.
func NewWazeroRuntime(ctx context.Context, cfg RuntimeConfig) (*WazeroRuntime, error) {
	if len(cfg.WasmBytes) == 0 {
		return nil, errors.New("wazero runtime requires non-empty WasmBytes")
	}

	backend, err := resolveWazeroBackend(cfg.Backend)
	if err != nil {
		return nil, err
	}
	rtCfg := wazero.NewRuntimeConfigCompiler()
	if backend == BackendWazeroInterpreter {
		rtCfg = wazero.NewRuntimeConfigInterpreter()
	}
	rtCfg = rtCfg.WithCloseOnContextDone(true)
	if cfg.DefaultMemoryLimitPages > 0 {
		rtCfg = rtCfg.WithMemoryLimitPages(cfg.DefaultMemoryLimitPages)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, rtCfg)
	wasiCtx := experimental.WithFunctionListenerFactory(ctx, newProcExitListenerFactory())
	if _, err := wasi_snapshot_preview1.Instantiate(wasiCtx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("instantiate wasi_snapshot_preview1: %w", err)
	}

	compiled, err := rt.CompileModule(ctx, cfg.WasmBytes)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("compile wasm module: %w", err)
	}

	policy := cfg.ImportPolicy
	if len(policy) == 0 && cfg.Manifest != nil && len(cfg.Manifest.WasmImports) > 0 {
		policy = cfg.Manifest.WasmImports
	}
	if len(policy) == 0 {
		policy = DefaultWASIImportPolicy()
	}
	audit := AuditCompiledModule(compiled, policy)
	if cfg.EnforceImportAudit && !audit.Passed {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("wasm import audit failed: %d denied, %d undeclared", len(audit.Denied), len(audit.Undeclared))
	}

	clk := cfg.Clock
	if clk == nil {
		clk = NewSystemClock()
	}
	ent := cfg.Entropy
	if ent == nil {
		ent = CryptoEntropy{}
	}
	wsMount := cfg.WorkspaceMount
	if strings.TrimSpace(wsMount) == "" {
		wsMount = DefaultGuestWorkspaceMount
	}
	if strings.TrimSpace(cfg.ScratchRoot) != "" {
		if err := os.MkdirAll(cfg.ScratchRoot, 0o700); err != nil {
			_ = rt.Close(ctx)
			return nil, fmt.Errorf("create scratch root: %w", err)
		}
		// Sweep any orphaned per-invocation scratch directories left behind by
		// a previously crashed or abruptly terminated host process.
		_, _ = CleanOrphanedScratchDirs(cfg.ScratchRoot)
	}

	return &WazeroRuntime{
		backend:             backend,
		rt:                  rt,
		wasmBytes:           append([]byte(nil), cfg.WasmBytes...),
		compiled:            compiled,
		manifest:            cfg.Manifest,
		imageDir:            cfg.ImageDir,
		scratchRoot:         cfg.ScratchRoot,
		workspaceMount:      wsMount,
		useTrustedBootstrap: cfg.UseTrustedBootstrap,
		audit:               audit,
		clock:               clk,
		entropy:             ent,
		parked:              NewParkedGuestTracker(cfg.ParkedBudget),
		memLimitPgs:         cfg.DefaultMemoryLimitPages,
	}, nil
}

func (r *WazeroRuntime) meteredModule(_ context.Context) (wazero.CompiledModule, error) {
	r.meteredOnce.Do(func() {
		instrumented, err := InstrumentWasmFuel(r.wasmBytes)
		if err != nil {
			r.compiledMeteredErr = err
			return
		}
		r.compiledMetered, r.compiledMeteredErr = r.rt.CompileModule(context.Background(), instrumented)
	})
	return r.compiledMetered, r.compiledMeteredErr
}

// CleanOrphanedScratchDirs removes any leftover per-invocation scratch
// directories (matching "pw-py-scratch-*") in scratchRoot left behind by a
// previously crashed or lost host process.
func CleanOrphanedScratchDirs(scratchRoot string) (int, error) {
	scratchRoot = strings.TrimSpace(scratchRoot)
	if scratchRoot == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(scratchRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	var firstErr error
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "pw-py-scratch-") {
			continue
		}
		if rmErr := os.RemoveAll(filepath.Join(scratchRoot, name)); rmErr != nil {
			if firstErr == nil {
				firstErr = rmErr
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

// Info reports the actual instantiated wazero backend kind.
func (r *WazeroRuntime) Info() RuntimeInfo {
	return RuntimeInfo{Backend: r.backend}
}

// Manifest returns the image manifest associated with this runtime.
func (r *WazeroRuntime) Manifest() *ImageManifest { return r.manifest }

// ImportAudit returns the compiled module's import audit report.
func (r *WazeroRuntime) ImportAudit() *ImportAuditReport { return r.audit }

// ParkedGuests returns the aggregate parked-guest admission tracker.
func (r *WazeroRuntime) ParkedGuests() *ParkedGuestTracker { return r.parked }

// Close releases all parked guest reservations and closes the wazero runtime.
func (r *WazeroRuntime) Close(ctx context.Context) error {
	r.parked.ReleaseAll()
	return r.rt.Close(ctx)
}

// Execute instantiates a fresh guest module for inv, enforces configured
// output/time/memory/work boundaries, settles the workspace adapter, and
// releases all guest resources before returning.
func (r *WazeroRuntime) Execute(ctx context.Context, inv Invocation) (*ExecutionResult, error) {
	start := r.clock.Now()
	execCtx, cancel := context.WithCancel(ctx)
	if inv.Limits.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, inv.Limits.Timeout)
	}
	defer cancel()

	latch := NewStopLatch(cancel)
	latch.now = r.clock.Now
	if inv.StopLatch != nil {
		inv.StopLatch.RegisterSealer(func() {
			if stop := inv.StopLatch.Stop(); stop != nil {
				latch.Trigger(*stop)
			}
		})
		latch.RegisterSealer(func() {
			if stop := latch.Stop(); stop != nil {
				inv.StopLatch.Trigger(*stop)
			}
		})
	}
	meter := NewWorkMeter(inv.Limits.MaxGuestWork, inv.Limits.MaxHostWork, inv.Limits.MaxStagedBytes, latch)
	procExit := &procExitTracker{}
	execCtx = context.WithValue(execCtx, procExitContextKey{}, procExit)

	var (
		argv []string
		err  error
	)
	if r.useTrustedBootstrap {
		cwd := normalizeGuestCWD(r.workspaceMount, inv.CWD)
		argv, err = BuildBootstrapArgv(BootstrapConfig{
			Mode:           inv.Mode,
			Code:           inv.Code,
			Script:         inv.Script,
			Module:         inv.Module,
			CLI:            inv.CLI,
			CWD:            cwd,
			Args:           inv.Args,
			PackagesMount:  DefaultGuestPackagesMount,
			ScratchMount:   DefaultGuestScratchMount,
			HTTPBridgePath: inv.HTTPBridgePath,
		})
	} else {
		argv, err = BuildArgv(inv)
	}
	if err != nil {
		return nil, err
	}

	// Create a fresh program-private scratch directory per invocation and clean
	// it up deterministically after the guest module closes.
	invScratch, err := os.MkdirTemp(r.scratchRoot, "pw-py-scratch-*")
	if err != nil {
		return nil, fmt.Errorf("create per-invocation scratch dir: %w", err)
	}
	defer os.RemoveAll(invScratch)

	var stderrCapture bytes.Buffer
	stderrTarget := io.Writer(&stderrCapture)
	if inv.Stderr != nil {
		stderrTarget = io.MultiWriter(inv.Stderr, &stderrCapture)
	}
	sealedStdout := NewSealedWriter(inv.Stdout, latch, inv.Limits.MaxOutputBytes)
	sealedStderr := NewSealedWriter(stderrTarget, latch, inv.Limits.MaxOutputBytes)

	modCfg, wsFS := r.buildModuleConfig(execCtx, inv, argv, sealedStdout, sealedStderr, latch, meter, invScratch)

	if inv.ExecutionID != "" {
		defer r.parked.Release(inv.ExecutionID)
	}

	var (
		mod       api.Module
		runErr    error
		peakPages uint32
	)
	if inv.Limits.MaxGuestWork > 0 {
		targetMod, mErr := r.meteredModule(execCtx)
		if mErr != nil {
			return nil, fmt.Errorf("compile metered wasm module: %w", mErr)
		}
		// Instantiate without auto-running _start so we can seed __pw_fuel first.
		mod, runErr = r.rt.InstantiateModule(execCtx, targetMod, modCfg.WithStartFunctions())
		if runErr == nil && mod != nil {
			if g := mod.ExportedGlobal(FuelGlobalExportName); g != nil {
				if mg, ok := g.(api.MutableGlobal); ok {
					mg.Set(inv.Limits.MaxGuestWork)
				}
			}
			if startFn := mod.ExportedFunction("_start"); startFn != nil {
				_, runErr = startFn.Call(execCtx)
			}
			if g := mod.ExportedGlobal(FuelGlobalExportName); g != nil {
				rem := int64(g.Get())
				if rem < 0 {
					meter.SetGuestWork(inv.Limits.MaxGuestWork + 1)
					latch.Trigger(TerminalStop{
						Code:   StopWorkLimitExceeded,
						Reason: fmt.Sprintf("guest work fuel exhausted (limit %d)", inv.Limits.MaxGuestWork),
					})
				} else {
					meter.SetGuestWork(inv.Limits.MaxGuestWork - uint64(rem))
				}
			}
		}
	} else {
		mod, runErr = r.rt.InstantiateModule(execCtx, r.compiled, modCfg)
	}

	var unclosedWriteDescs int64
	if wsFS != nil {
		unclosedWriteDescs = wsFS.OpenWriteDescriptors()
	}

	if mod != nil {
		if mem := mod.Memory(); mem != nil {
			peakPages = mem.Size() / 65536
		}
		_ = mod.Close(context.Background())
	}

	res := &ExecutionResult{
		ExecutionID: inv.ExecutionID,
		Backend:     r.Info().Backend,
		GuestStderr: stderrCapture.String(),
		Usage: ResourceUsage{
			GuestWork:       meter.GuestWork(),
			HostWork:        meter.HostWork(),
			PeakMemoryPages: peakPages,
			StdoutBytes:     sealedStdout.WrittenBytes(),
			StderrBytes:     sealedStderr.WrittenBytes(),
			StagedBytes:     meter.StagedBytes(),
			Duration:        r.clock.Now().Sub(start),
		},
	}

	r.classifyOutcome(execCtx, latch, runErr, res)

	// Host-confirmed clean finalization requirements:
	// 1. In trusted bootstrap mode, _start must return cleanly without proc_exit (rejecting os._exit(0)).
	// 2. Zero stdout/stderr write or flush errors.
	// 3. Zero unclosed host-tracked workspace write descriptors.
	if r.useTrustedBootstrap && procExit.called.Load() {
		res.CleanCompletion = false
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("abrupt guest exit via proc_exit(%d) bypassed clean finalization", procExit.code.Load()))
	}
	if sealedStdout.HadError() || sealedStderr.HadError() {
		res.CleanCompletion = false
		res.Diagnostics = append(res.Diagnostics, "guest output stream reported write or seal error")
	}
	if unclosedWriteDescs > 0 {
		res.CleanCompletion = false
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("guest exited with %d unclosed workspace write descriptor(s)", unclosedWriteDescs))
	}

	if inv.Workspace != nil {
		report, settleErr := inv.Workspace.Settle(context.Background(), res.CleanCompletion)
		res.Settlement = report
		if settleErr != nil {
			res.CleanCompletion = false
			res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("workspace settlement failed: %v", settleErr))
		}
		if report != nil {
			if len(report.Conflicts) > 0 {
				res.CleanCompletion = false
				latch.Trigger(TerminalStop{
					Code:   StopRevisionConflict,
					Reason: fmt.Sprintf("workspace settlement detected %d revision conflict(s)", len(report.Conflicts)),
				})
				res.Stop = latch.Stop()
				res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("workspace settlement revision conflicts: %d", len(report.Conflicts)))
			}
			if report.PartialFailure != nil {
				res.CleanCompletion = false
				res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("workspace settlement partial failure: %v", report.PartialFailure))
			}
		}
	}

	return res, nil
}

func normalizeGuestCWD(workspaceMount, raw string) string {
	mount := path.Clean("/" + strings.TrimPrefix(strings.TrimSpace(workspaceMount), "/"))
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "." || trimmed == "./" {
		return mount
	}
	if trimmed == mount || strings.HasPrefix(trimmed, mount+"/") {
		cleaned := path.Clean(trimmed)
		if cleaned == mount || strings.HasPrefix(cleaned, mount+"/") {
			return cleaned
		}
		return mount
	}
	cleaned := path.Clean(mount + "/" + strings.TrimPrefix(trimmed, "/"))
	if cleaned == mount || strings.HasPrefix(cleaned, mount+"/") {
		return cleaned
	}
	return mount
}

func (r *WazeroRuntime) buildModuleConfig(
	execCtx context.Context,
	inv Invocation,
	argv []string,
	stdout, stderr io.Writer,
	latch *StopLatch,
	meter *WorkMeter,
	invScratch string,
) (wazero.ModuleConfig, *WorkspaceSysFS) {
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithArgs(argv...).
		WithStdout(stdout).
		WithStderr(stderr).
		WithRandSource(r.entropy.Reader()).
		WithWalltime(func() (sec int64, nsec int32) {
			now := r.clock.Now()
			return now.Unix(), int32(now.Nanosecond())
		}, sys.ClockResolution(r.clock.WallResolutionNanos())).
		WithNanotime(func() int64 {
			return r.clock.MonotonicNanos()
		}, sys.ClockResolution(r.clock.MonotonicResolutionNanos())).
		WithNanosleep(func(ns int64) {
			if ns <= 0 {
				return
			}
			timer := time.NewTimer(time.Duration(ns))
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-execCtx.Done():
			}
		})

	if inv.Stdin != nil {
		cfg = cfg.WithStdin(inv.Stdin)
	}
	for k, v := range inv.Env {
		cfg = cfg.WithEnv(k, v)
	}

	fsCfg := wazero.NewFSConfig()
	if sysCfg, ok := fsCfg.(experimentalsysfs.FSConfig); ok {
		fsCfg = sysCfg.WithSysFSMount(NullDeviceFS{}, "/dev")
	}
	if r.imageDir != "" && r.manifest != nil {
		if stdlibAbs, err := resolveInsideDir(r.imageDir, r.manifest.Interpreter.StdlibPath); err == nil {
			fsCfg = fsCfg.WithReadOnlyDirMount(stdlibAbs, "/usr/local/lib")
		}
		if r.manifest.PackagesRoot != "" {
			if pkgAbs, err := resolveInsideDir(r.imageDir, r.manifest.PackagesRoot); err == nil {
				fsCfg = fsCfg.WithReadOnlyDirMount(pkgAbs, DefaultGuestPackagesMount)
			}
		}
	}
	if invScratch != "" {
		fsCfg = fsCfg.WithDirMount(invScratch, DefaultGuestScratchMount)
	}
	var wsFS *WorkspaceSysFS
	if inv.Workspace != nil {
		wsFS = NewWorkspaceSysFS(WorkspaceSysFSConfig{
			Context:     execCtx,
			ExecutionID: inv.ExecutionID,
			Namespace:   NamespaceWorkspace,
			Adapter:     inv.Workspace,
			Observer:    inv.Observer,
			Latch:       latch,
			Meter:       meter,
		})
		if sysCfg, ok := fsCfg.(experimentalsysfs.FSConfig); ok {
			fsCfg = sysCfg.WithSysFSMount(wsFS, r.workspaceMount)
		}
	}
	if len(inv.ExtraSysMounts) > 0 {
		mounts := make([]string, 0, len(inv.ExtraSysMounts))
		for m := range inv.ExtraSysMounts {
			mounts = append(mounts, m)
		}
		sort.Strings(mounts)
		for _, m := range mounts {
			if fsys := inv.ExtraSysMounts[m]; fsys != nil {
				if sysCfg, ok := fsCfg.(experimentalsysfs.FSConfig); ok {
					fsCfg = sysCfg.WithSysFSMount(fsys, m)
				}
			}
		}
	}
	return cfg.WithFSConfig(fsCfg), wsFS
}

func (r *WazeroRuntime) classifyOutcome(execCtx context.Context, latch *StopLatch, runErr error, res *ExecutionResult) {
	if runErr == nil {
		if latch.Stopped() {
			res.Stop = latch.Stop()
			res.CleanCompletion = false
			return
		}
		res.ExitCode = 0
		res.CleanCompletion = true
		return
	}

	var exitErr *sys.ExitError
	if errors.As(runErr, &exitErr) {
		code := exitErr.ExitCode()
		if code == sys.ExitCodeDeadlineExceeded || code == sys.ExitCodeContextCanceled {
			latch.Trigger(TerminalStop{
				Code:   StopCancelled,
				Reason: runErr.Error(),
			})
			res.ExitCode = int(code)
			res.Stop = latch.Stop()
			res.CleanCompletion = false
			return
		}
		res.ExitCode = int(code)
		if code == 0 && !latch.Stopped() && execCtx.Err() == nil {
			res.CleanCompletion = true
			return
		}
		res.Stop = latch.Stop()
		res.CleanCompletion = false
		return
	}

	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) || execCtx.Err() != nil {
		latch.Trigger(TerminalStop{
			Code:   StopCancelled,
			Reason: runErr.Error(),
		})
		res.ExitCode = 1
		res.Stop = latch.Stop()
		res.CleanCompletion = false
		return
	}

	errMsg := runErr.Error()
	if strings.Contains(strings.ToLower(errMsg), "memory") || strings.Contains(strings.ToLower(errMsg), "out of bounds") {
		latch.Trigger(TerminalStop{
			Code:   StopMemoryLimitExceeded,
			Reason: errMsg,
		})
	}
	res.ExitCode = 1
	res.Stop = latch.Stop()
	res.CleanCompletion = false
	res.Diagnostics = append(res.Diagnostics, errMsg)
}

// ResolveImagePaths validates and returns absolute paths for the wasm binary
// and stdlib inside imageDir.
func ResolveImagePaths(imageDir string, m *ImageManifest) (wasmPath, stdlibPath, packagesPath string, err error) {
	if err = m.Validate(); err != nil {
		return "", "", "", err
	}
	absDir, err := filepath.Abs(imageDir)
	if err != nil {
		return "", "", "", err
	}
	wasmPath, err = resolveInsideDir(absDir, m.Interpreter.WasmPath)
	if err != nil {
		return "", "", "", err
	}
	stdlibPath, err = resolveInsideDir(absDir, m.Interpreter.StdlibPath)
	if err != nil {
		return "", "", "", err
	}
	if m.PackagesRoot != "" {
		packagesPath, err = resolveInsideDir(absDir, m.PackagesRoot)
		if err != nil {
			return "", "", "", err
		}
	}
	return wasmPath, stdlibPath, packagesPath, nil
}
