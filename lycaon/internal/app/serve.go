package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/decide"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/clisocket"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostlock"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/projectliveness"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
)

// ServeApp holds wired serve subsystems after Build.
type ServeApp struct {
	Server               *api.Server
	SessionMgr           *session.Manager
	SessionStore         session.Store
	ProjectLiveness      *projectliveness.Tracker
	CoordinatorRuntime   *coordinator.Runtime
	WorkflowMgr          *workflow.RunManager
	BlueprintMgr         *blueprint.Manager
	DelegationMgr        *delegation.Manager
	DelegationStore      orchestration.PipelineDelegationStore
	AgentRegistry        *orchestration.MemoryAgentRegistry
	WorkerQueue          worker.WorkerQueue
	ToolRegistry         *tools.DefaultRegistry
	SessionWorkflowStore workflow.SessionWorkflowStore
	CheckpointMgr        hitl.CheckpointManager
	VisualStore          visual.Store
	DB                   db.ReadHandle
	Events               events.ReplayHub
	ConfigRoot           string
	ListenAddr           string
	APIToken             string
	TokenGenerated       bool
	resources            *runtimeResources
	startup              startupprotocol.Sink
	upgradeRecoveryReady func() error

	// storeClaim stops the serve loop when the store path is replaced.
	storeClaim *hostlock.Claim
	// decider is the local decision engine, closed at shutdown when it owns a process.
	decider decide.Decider

	runners       []backgroundRunner
	runnersCancel context.CancelFunc
	runnersWG     sync.WaitGroup
	profileWG     sync.WaitGroup
	runnersActive bool

	projects clisocket.Projects
	eventPub *events.Publisher
}

// StartBackgroundWorkers starts registered runners and returns their stop function.
func (a *ServeApp) StartBackgroundWorkers(ctx context.Context) (context.CancelFunc, error) {
	if err := a.startRunners(ctx); err != nil {
		return nil, err
	}
	return a.stopRunners, nil
}

// MCPRegistry returns the MCP registry when wired.
func (a *ServeApp) MCPRegistry() *mcp.RegistryImpl {
	if a == nil || a.resources == nil {
		return nil
	}
	return a.resources.mcpRegistry
}

const (
	// serveDrainTimeout bounds the serve loop's runner and HTTP drain.
	serveDrainTimeout = 3 * time.Second
	// resourceReleaseTimeout bounds background waits and subsystem release.
	resourceReleaseTimeout = 3 * time.Second
)

// ShutdownBudget is this engine's worst-case ordered shutdown.
const ShutdownBudget = serveDrainTimeout + resourceReleaseTimeout + db.StoreShutdownFloor

// ShutdownBudgetSeconds is the desktop launcher's hard-stop delay.
const ShutdownBudgetSeconds = 9

// Close stops background work and releases managed resources.
func (a *ServeApp) Close() error {
	if a == nil {
		return nil
	}
	if a.SessionMgr != nil {
		a.SessionMgr.BeginEngineShutdown()
	}
	a.stopRunners()
	drainCtx, cancel := context.WithTimeout(context.Background(), resourceReleaseTimeout)
	defer cancel()
	if a.Server != nil {
		a.Server.StopBackground()
	}
	var drainErr error
	if a.SessionMgr != nil {
		drainErr = a.SessionMgr.WaitForEngineShutdown(drainCtx)
	}
	if a.Server != nil {
		a.Server.WaitForBackground(drainCtx)
	}
	// Store shutdown retains its reserved cleanup floor.
	err := errors.Join(drainErr, a.resources.Close(drainCtx))
	a.profileWG.Wait()
	a.DB = nil
	return err
}

var storeClaimCheckInterval = 5 * time.Second

// watchStoreClaim delivers the loss of the store claim.
func (a *ServeApp) watchStoreClaim(ctx context.Context) <-chan error {
	if a.storeClaim == nil {
		return nil
	}
	lost := make(chan error, 1)
	go func() {
		if err := a.storeClaim.Lost(ctx, storeClaimCheckInterval); errors.Is(err, hostlock.ErrStoreClaimLost) {
			lost <- err
		}
	}()
	return lost
}

// Run serves until shutdown or loss of safe store access.
func (a *ServeApp) Run(ctx context.Context) error {
	startupReady := false
	defer func() {
		if !startupReady && a.startup != nil {
			_ = a.startup.Failed("serve_failed")
		}
	}()
	if a.startup != nil {
		if err := a.startup.Phase(startupprotocol.PhaseBackgroundWork); err != nil {
			return fmt.Errorf("startup protocol: %w", err)
		}
	}
	stopPerformance := observability.StartRuntimeSampler(ctx, a.resources.performanceGauges)
	defer stopPerformance()
	if err := a.startRunners(ctx); err != nil {
		return err
	}

	if os.Getenv("LYCAON_DEV_CORS") == "1" && !configdir.IsDevelopmentChannel() {
		slog.WarnContext(ctx, "LYCAON_DEV_CORS ignored outside a development channel")
	}
	if a.TokenGenerated {
		if path, err := api.WriteAPITokenFile(a.APIToken); err != nil {
			slog.WarnContext(ctx, "could not write api.token", "err", err)
		} else if configdir.IsDevelopmentChannel() {
			fmt.Fprintf(os.Stderr, "Painted Wolf Code: API token written to %s (also set LYCAON_API_TOKEN to reuse)\n", path)
		}
	}

	if strings.TrimSpace(a.APIToken) == "" || a.APIToken == api.TestAPIToken {
		return fmt.Errorf("refusing to serve without a real API token (set LYCAON_API_TOKEN)")
	}
	if err := a.startProfileServer(ctx); err != nil {
		return err
	}

	if a.startup != nil {
		if err := a.startup.Phase(startupprotocol.PhaseBinding); err != nil {
			return fmt.Errorf("startup protocol: %w", err)
		}
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", a.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.ListenAddr, err)
	}
	a.ListenAddr = listener.Addr().String()
	if a.upgradeRecoveryReady != nil {
		if err := a.upgradeRecoveryReady(); err != nil {
			_ = listener.Close()
			return fmt.Errorf("complete upgrade recovery: %w", err)
		}
	}

	host, port, err := api.ParseListenHostPort(a.ListenAddr)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("listen address: %w", err)
	}
	if err := api.WriteDaemonManifest(host, port, os.Getpid()); err != nil {
		slog.WarnContext(ctx, "could not write daemon.json", "err", err)
	}

	if a.projects != nil && a.eventPub != nil {
		sock, err := clisocket.Listen(ctx, a.projects, a.eventPub)
		if err != nil {
			slog.WarnContext(ctx, "cli socket unavailable", "err", err)
		} else {
			a.resources.setCLISocket(sock)
			slog.InfoContext(ctx, "cli socket listening", "path", sock.Path())
		}
	}
	if a.resources != nil && a.resources.mcpRegistry != nil {
		a.resources.mcpRegistry.SetAPIAccess(a.APIToken)
		if err := a.resources.mcpRegistry.Resync(ctx); err != nil {
			slog.WarnContext(ctx, "mcp resync after listen", "err", err)
		}
	}

	a.resources.setHTTPServer(&http.Server{
		Handler:           a.Server,
		ReadHeaderTimeout: 10 * time.Second,
	})

	errChan := make(chan error, 1)
	go func() {
		slog.InfoContext(ctx, "server listening", "addr", a.ListenAddr)
		if err := a.resources.httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()
	if a.startup != nil {
		if err := a.startup.Ready(port); err != nil {
			_ = listener.Close()
			return fmt.Errorf("startup protocol: %w", err)
		}
	}
	startupReady = true

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigChan)

	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	parentGone := watchParentExit(watchCtx)
	claimLost := a.watchStoreClaim(watchCtx)

	var runErr error
	select {
	case err := <-errChan:
		runErr = fmt.Errorf("server error: %w", err)
	case <-a.storeFailed():
		runErr = a.resources.db.Failure()
		slog.ErrorContext(ctx, "store integrity failed; stopping the engine", "error", runErr)
	case err := <-claimLost:
		slog.ErrorContext(ctx, "store claim lost; stopping the engine", "error", err)
		runErr = err
	case sig := <-sigChan:
		slog.InfoContext(ctx, "shutdown signal received", "signal", sig.String())
	case <-parentGone:
		slog.InfoContext(ctx, "shutdown after spawning process exited")
	case <-ctx.Done():
		slog.InfoContext(ctx, "shutdown context canceled")
	}

	stopWatch()
	if parentGone != nil {
		<-parentGone
	}

	// Settle active turns before background cancellation.
	a.SessionMgr.BeginEngineShutdown()
	if closer, ok := a.decider.(io.Closer); ok {
		_ = closer.Close()
	}

	a.Server.StopBackground()

	// One deadline covers runner and HTTP draining.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), serveDrainTimeout)
	defer cancel()
	a.stopRunnersWithin(shutdownCtx)                                                      //nolint:contextcheck // shutdown outlives the canceled run ctx
	if err := a.resources.httpServer.Shutdown(shutdownCtx); err != nil && runErr == nil { //nolint:contextcheck // shutdown outlives the canceled run ctx
		runErr = err
	}
	return runErr
}
