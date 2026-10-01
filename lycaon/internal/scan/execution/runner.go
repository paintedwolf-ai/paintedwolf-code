package execution

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/observability"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

const scannersDisabledReason = "security scanners disabled"

// maxPublishAttempts bounds snapshot publication retries.
const maxPublishAttempts = 5

const terminalCallbackTimeout = 30 * time.Second

// Runner claims pending code scans and executes them through the scanner registry.
type Runner struct {
	Store    *scanbase.SQLStore
	Registry scanbase.CodeScannerRegistry
	Ingester scanbase.ScanResultIngester
	Events   *events.Publisher
	// DataDir roots host evidence, keyed by project ID or by project path.
	DataDir string
	// OnTerminal observes committed terminal scans.
	OnTerminal func(ctx context.Context, scan api.CodeScan)
	// OnDelta observes what a completed scan introduced and fixed for its
	// series, after the history is recorded.
	OnDelta func(ctx context.Context, scan api.CodeScan, introduced, fixed []api.SecurityFinding)
	// ReconcileTerminal retries unacknowledged terminal deliveries.
	ReconcileTerminal func(ctx context.Context) error
	MaxRunning        int
	ReconcileInterval time.Duration
	RetryDelay        time.Duration
	ResultSpillBytes  int
	// ChunkFiles bounds the files one engine invocation receives; zero runs
	// every scan as one invocation.
	ChunkFiles int
	// FileTimeout bounds one file for engines that read files one at a time.
	FileTimeout time.Duration
	Snapshots   *sourcesnapshot.Store
	Coordinator *scanbase.CoordinatorImpl
	Settings    *settings.SecurityScannersStore
	Broker      *backgroundwork.Broker
	sem         chan struct{}
	inflight    sync.WaitGroup
	claimMu     sync.Mutex
	claims      map[string]*scanExecution

	publishMu       sync.Mutex
	publishAttempts map[string]int
	publishAfter    map[string]time.Time
}

type scanExecution struct {
	claimToken string
	cancel     context.CancelFunc
	// preempted is the reason another request stopped this execution.
	preempted string
}

// PreemptedCode records a scan the scan plane stopped because work it asked
// for later covers everything the scan would have.
const PreemptedCode = "SCAN_PREEMPTED"

// Preempt stops one scan the scan plane no longer needs: a running scan is
// canceled with the reason, a pending one fails before any runner claims it.
// Either way the scan settles through the ordinary terminal path.
func (r *Runner) Preempt(ctx context.Context, scanID, reason string) {
	if r == nil || r.Store == nil {
		return
	}
	r.claimMu.Lock()
	execution := r.claims[scanID]
	if execution != nil {
		execution.preempted = reason
		execution.cancel()
	}
	r.claimMu.Unlock()
	if execution != nil {
		return
	}
	won, err := r.Store.FinalizePendingFailure(ctx, scanID, PreemptedCode, reason)
	if err != nil || !won {
		if err != nil {
			slog.WarnContext(ctx, "preempt pending scan", "scan_id", scanID, "error", err)
		}
		return
	}
	if rec, err := r.Store.Get(ctx, scanID); err == nil && rec != nil {
		r.notifyTerminal(ctx, rec)
	}
}

func (r *Runner) preemptionReason(scanID string) string {
	r.claimMu.Lock()
	defer r.claimMu.Unlock()
	if execution := r.claims[scanID]; execution != nil {
		return execution.preempted
	}
	return ""
}

// NewRunner builds a scan runner with bounded concurrency.
func NewRunner(store *scanbase.SQLStore, reg scanbase.CodeScannerRegistry, ingester scanbase.ScanResultIngester, cfg scancfg.RunnerConfig, pub *events.Publisher) *Runner {
	defaults := scancfg.DefaultRunnerConfig()
	n := cfg.Runner.MaxConcurrency
	if n <= 0 {
		n = defaults.Runner.MaxConcurrency
	}
	spill := cfg.Runner.ResultSpillBytes
	if spill <= 0 {
		spill = defaults.Runner.ResultSpillBytes
	}
	chunk := cfg.Runner.ChunkFiles
	if chunk <= 0 {
		chunk = defaults.Runner.ChunkFiles
	}
	return &Runner{
		Store:             store,
		Registry:          reg,
		Ingester:          ingester,
		Events:            pub,
		MaxRunning:        n,
		ReconcileInterval: cfg.ReconcileInterval(),
		RetryDelay:        time.Duration(cfg.Runner.RetryDelayMs) * time.Millisecond,
		ResultSpillBytes:  spill,
		ChunkFiles:        chunk,
		FileTimeout:       time.Duration(fileTimeoutMs(cfg, defaults)) * time.Millisecond,
		sem:               make(chan struct{}, n),
		claims:            make(map[string]*scanExecution),
		Broker:            backgroundwork.Process(),
	}
}

// Run services committed work and waits for demand or a recovery deadline.
func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.Store == nil || r.Registry == nil {
		return errors.New("scan runner: store and registry required")
	}
	defer r.inflight.Wait()
	for ctx.Err() == nil {
		changed := r.Settings.Changed()
		err := r.serviceWork(ctx)
		if ctx.Err() != nil {
			break
		}
		delay := r.nextWake(ctx)
		if err != nil {
			slog.WarnContext(ctx, "service scan work", "error", err)
			delay = r.retryDelay()
		}
		waitForWork(ctx, r.Store.QueueChanged.Wake(), changed, delay)
	}
	return ctx.Err()
}

func (r *Runner) serviceWork(ctx context.Context) error {
	// Renew live claims before the expiry sweep after scheduling pauses.
	r.renewActiveClaims(ctx)
	if recovered, err := r.Store.RecoverExpiredClaims(ctx); err == nil {
		for i := range recovered {
			r.notifyTerminal(ctx, &recovered[i])
		}
	} else {
		return err
	}
	reconcileErr := r.reconcileTerminal(ctx)
	if r.Settings != nil && !r.Settings.Effective().Enabled {
		canceled, err := r.Store.CancelPendingScans(ctx, scannersDisabledReason)
		if err != nil {
			return errors.Join(reconcileErr, err)
		}
		for i := range canceled {
			r.notifyTerminal(ctx, &canceled[i])
		}
		return reconcileErr
	}
	r.publishWarming(ctx)
	for {
		select {
		case r.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		default:
			return reconcileErr
		}
		running, err := r.Store.CountRunning(ctx)
		if err != nil || running >= r.MaxRunning {
			<-r.sem
			return errors.Join(reconcileErr, err)
		}
		scan, err := r.Store.ClaimNext(ctx)
		if errors.Is(err, scanbase.ErrNoPendingScans) {
			<-r.sem
			return reconcileErr
		}
		if err != nil {
			<-r.sem
			return errors.Join(reconcileErr, err)
		}
		execCtx, execution := r.registerExecution(ctx, scan)
		r.inflight.Add(1)
		go func(job *api.CodeScan, runCtx context.Context, registered *scanExecution) {
			defer r.inflight.Done()
			defer func() {
				<-r.sem
				r.Store.QueueChanged.Notify()
			}()
			defer observability.GuardPanic("scan.runner")
			defer r.unregisterExecution(job.ID, registered)
			r.execute(runCtx, job)
		}(scan, execCtx, execution)
	}
}

func (r *Runner) reconcileTerminal(ctx context.Context) error {
	if r == nil || r.ReconcileTerminal == nil {
		return nil
	}
	callbackCtx, cancel := terminalCallbackContext(ctx)
	defer cancel()
	return r.ReconcileTerminal(callbackCtx)
}

func fileTimeoutMs(cfg, defaults scancfg.RunnerConfig) int {
	if cfg.Runner.FileTimeoutMs > 0 {
		return cfg.Runner.FileTimeoutMs
	}
	return defaults.Runner.FileTimeoutMs
}
