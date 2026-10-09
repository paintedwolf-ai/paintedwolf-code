package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/clisocket"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
)

type runtimeResources struct {
	lifecycle *resourcelifecycle.Registry

	cliSocket     *clisocket.Server
	httpServer    *http.Server
	profileServer *http.Server
	mcpRegistry   *mcp.RegistryImpl
	db            *db.Store
}

func newRuntimeResources() *runtimeResources {
	return &runtimeResources{lifecycle: resourcelifecycle.New()}
}

func (r *runtimeResources) track(name string, order int, cleanup func(context.Context) error) {
	if r == nil || r.lifecycle == nil || cleanup == nil {
		return
	}
	_ = r.lifecycle.Track(resourcelifecycle.DeviceScope(), name, order, func(ctx context.Context, _ resourcelifecycle.Scope) error {
		return cleanup(ctx)
	})
}

// capture records resources acquired by a build phase.
func (r *runtimeResources) capture(b *serveBuilder) {
	if r == nil || b == nil {
		return
	}
	if b.egressBrokerBound {
		// The front door closes after the processes that use it.
		r.track("egress-broker", 55, func(context.Context) error { return confine.StopEgressBroker() })
	}
	if b.refusalWatchStarted {
		// Refusal reports stop after the processes they describe.
		r.track("refusal-watch", 55, func(context.Context) error { confine.StopRefusalWatch(); return nil })
	}
	if controller := b.previewCtrl; controller != nil {
		r.track("preview", 30, func(context.Context) error { controller.Close(); return nil })
	}
	if controller := b.hostPower; controller != nil {
		r.track("host-power", 25, func(context.Context) error { return controller.Close() })
	}
	if registry := b.pageRegistry; registry != nil {
		r.track("browser-pages", 40, func(ctx context.Context) error { registry.Close(ctx); return nil })
	}
	if host := b.pricingHost; host != nil {
		// Pricing fetches read the model feed, so they stop before the service.
		r.track("pricing", 44, func(context.Context) error { host.Close(); return nil })
	}
	if service := b.llmSvc; service != nil {
		r.track("llm-service", 45, service.Close)
	}
	if registry := b.heldCalls; registry != nil {
		r.track("held-calls", 49, func(ctx context.Context) error {
			closeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return registry.Close(closeCtx)
		})
	}
	if registry := b.bgRegistry; registry != nil {
		r.track("background-processes", 50, func(ctx context.Context) error {
			closeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return registry.Lifecycle.Close(closeCtx)
		})
	}
	if pool := b.browserPool; pool != nil {
		r.track("browser-pool", 60, func(context.Context) error { pool.Close(); return nil })
	}
	if provider := b.repoProvider; provider != nil {
		r.track("repo-provider", 70, func(context.Context) error { return provider.Close() })
	}
	if unbinds := append([]func(){}, b.sourceFeedUnbinds...); len(unbinds) > 0 {
		r.track("source-feeds", 80, func(context.Context) error {
			for i := len(unbinds) - 1; i >= 0; i-- {
				unbinds[i]()
			}
			return nil
		})
	}
	if ledger := b.sourceLedger; ledger != nil && ledger.SnapshotStore() != nil {
		r.track("worker-baselines", 81, func(context.Context) error { return ledger.BaselineStore().Close() })
		snapshots := ledger.SnapshotStore()
		r.track("source-snapshots", 82, func(context.Context) error { return snapshots.Close() })
	}
	r.track("source-watchers", 85, func(context.Context) error {
		repochange.CloseWatchers()
		return nil
	})
	if warmer := b.webWarmer; warmer != nil {
		r.track("web-warmer", 90, func(context.Context) error { warmer.Close(); return nil })
	}
	if index := b.webIndex; index != nil {
		r.track("web-index", 100, func(context.Context) error { return index.Close() })
	}
	if registry := b.mcpReg; registry != nil {
		r.mcpRegistry = registry
		r.track("mcp", 110, func(context.Context) error { return registry.Close() }) //nolint:contextcheck // Close has no context.
	}
	if outbox := b.eventOutbox; outbox != nil {
		r.track("event-outbox", 120, func(context.Context) error { return outbox.Close() })
	}
	if publisher := b.eventPub; publisher != nil {
		r.track("event-publisher", 125, publisher.Close)
	}
	if database := b.db; database != nil {
		r.setDB(database)
	}
	r.track("debug-captures", 150, func(context.Context) error {
		observability.CloseDebugCaptures()
		return nil
	})
	if claim := b.storeClaim; claim != nil {
		r.track("instance-lock", 140, func(context.Context) error { claim.Release(); return nil })
	}
}

func (r *runtimeResources) setCLISocket(server *clisocket.Server) {
	if r == nil || server == nil {
		return
	}
	r.cliSocket = server
	r.track("cli-socket", 10, func(context.Context) error { return server.Close() })
}

func (r *runtimeResources) setHTTPServer(server *http.Server) {
	if r == nil || server == nil {
		return
	}
	r.httpServer = server
	r.track("http-server", 20, func(ctx context.Context) error {
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		err := server.Shutdown(shutdownCtx)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
}

func (r *runtimeResources) setProfileServer(server *http.Server, listener net.Listener) {
	if r == nil || server == nil || listener == nil {
		return
	}
	r.profileServer = server
	r.track("profile-server", 15, func(ctx context.Context) error {
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		err := server.Shutdown(shutdownCtx)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
}

func (r *runtimeResources) setDB(database *db.Store) {
	if r == nil || database == nil {
		return
	}
	r.db = database
	r.track("database", 130, database.Shutdown)
}

func (r *runtimeResources) Close(ctx context.Context) error {
	if r == nil || r.lifecycle == nil {
		return nil
	}
	return r.lifecycle.Dispose(ctx, resourcelifecycle.DeviceScope())
}

func (r *runtimeResources) performanceGauges() map[string]int64 {
	if r == nil || r.db == nil {
		return nil
	}
	stats := r.db.Stats()
	analysis := fileoutline.ProcessCacheStats()
	return map[string]int64{
		"db_reader_open":            int64(stats.Reader.OpenConnections),
		"db_reader_in_use":          int64(stats.Reader.InUse),
		"db_reader_idle":            int64(stats.Reader.Idle),
		"db_reader_wait_count":      stats.Reader.WaitCount,
		"db_reader_wait_ns":         stats.Reader.WaitDuration.Nanoseconds(),
		"db_writer_open":            int64(stats.Writer.OpenConnections),
		"db_writer_in_use":          int64(stats.Writer.InUse),
		"db_writer_wait_count":      stats.Writer.WaitCount,
		"db_writer_wait_ns":         stats.Writer.WaitDuration.Nanoseconds(),
		"source_analysis_entries":   int64(analysis.Entries),
		"source_analysis_bytes":     analysis.Bytes,
		"source_analysis_hits":      analysis.Hits,
		"source_analysis_misses":    analysis.Misses,
		"source_analysis_joins":     analysis.Joins,
		"source_analysis_evictions": analysis.Evictions,
		"source_analysis_bypasses":  analysis.Bypasses,
	}
}
