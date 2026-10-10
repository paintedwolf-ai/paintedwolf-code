package app

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/clisocket"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
	"net"
	"net/http"
	"time"
)

type runtimeResources struct {
	lifecycle *resourcelifecycle.Registry

	cliSocket     *clisocket.Server
	httpServer    *http.Server
	profileServer *http.Server
	mcpRegistry   *mcp.Runtime
	db            *db.Store
}

func newRuntimeResources() *runtimeResources {
	resources := &runtimeResources{lifecycle: resourcelifecycle.New()}
	releaseWatchers := repochange.AcquireWatcherLifetime()
	resources.Track("source-watchers", 85, func(context.Context) error { releaseWatchers(); return nil })
	resources.Track("debug-captures", 150, func(context.Context) error { observability.CloseDebugCaptures(); return nil })
	return resources
}

func (r *runtimeResources) Track(name string, order int, cleanup func(context.Context) error) {
	if r == nil || r.lifecycle == nil || cleanup == nil {
		return
	}
	_ = r.lifecycle.Track(resourcelifecycle.DeviceScope(), name, order, func(ctx context.Context, _ resourcelifecycle.Scope) error {
		return cleanup(ctx)
	})
}

func (r *runtimeResources) setMCP(registry *mcp.Runtime) {
	r.mcpRegistry = registry
	r.Track("mcp", 110, func(context.Context) error { return registry.Close() })
}

func (r *runtimeResources) setCLISocket(server *clisocket.Server) {
	if r == nil || server == nil {
		return
	}
	r.cliSocket = server
	r.Track("cli-socket", 10, func(context.Context) error { return server.Close() })
}

func (r *runtimeResources) setHTTPServer(server *http.Server) {
	if r == nil || server == nil {
		return
	}
	r.httpServer = server
	r.Track("http-server", 20, func(ctx context.Context) error {
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
	r.Track("profile-server", 15, func(ctx context.Context) error {
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		err := server.Shutdown(shutdownCtx)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
}

func (r *runtimeResources) SetDB(database *db.Store) {
	if r == nil || database == nil {
		return
	}
	r.db = database
	r.Track("database", 130, database.Shutdown)
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
