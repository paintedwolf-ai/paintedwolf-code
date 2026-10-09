package persistence

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	"log/slog"
	"path/filepath"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/hostlock"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/version"
	"github.com/lycaon/lycaon/internal/webindex"
)

type Runtime struct {
	ctx          context.Context
	startup      startupprotocol.Sink
	logger       *slog.Logger
	resources    ResourceLifetime
	Revision     uint64
	Retention    db.RetentionConfig
	Database     *db.Store
	Path         string
	Directory    string
	Claim        *hostlock.Claim
	Sessions     *store.SQL
	Projects     *project.SQLRegistry
	SourceLedger *sourceledger.Store
	WebIndex     *webindex.Store
	UpgradeReady func() error
}

func (b *Runtime) Open(ctx context.Context, dbPath string, startup startupprotocol.Sink, logger *slog.Logger, resources ResourceLifetime) error {
	b.ctx, b.startup, b.logger, b.resources = ctx, startup, logger, resources
	var err error
	// Hold the store lease across restore, schema setup, and serving.
	claim, err := hostlock.AcquireStore(dbPath)
	if err != nil {
		return err
	}
	b.Claim = claim
	b.resources.Track("instance-lock", 140, func(context.Context) error { claim.Release(); return nil })

	configDir := filepath.Dir(dbPath)
	if err := backup.CleanupInterruptedTransfers(configDir); err != nil {
		b.logger.Warn("could not clean interrupted backup transfers", "error", err)
	}
	if err := backup.ApplyPending(configDir); err != nil {
		return fmt.Errorf("apply staged restore before open: %w", err)
	}
	switch {
	case db.FreshEnabled():
		if err := localdata.ResetStoreCoupled(dbPath); err != nil {
			return fmt.Errorf("fresh development state: %w", err)
		}
		b.logger.Info("store-coupled development state wiped (LYCAON_DB_FRESH)", "path", dbPath)
	case db.FreshRequested():
		b.logger.Warn("LYCAON_DB_FRESH ignored on the production channel", "path", dbPath)
	}
	db.SetRunningAppVersion(version.Version)
	b.Database, err = b.openUpgradeableStore(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	b.resources.SetDB(b.Database)
	if err := claim.BindStore(); err != nil {
		return err
	}
	b.Path = dbPath
	b.Directory = filepath.Dir(dbPath)
	b.logger.Info("sqlite store", "path", dbPath)
	// Co-locate the cache with its store data root.
	indexPath := filepath.Join(filepath.Dir(dbPath), "web-index.db")
	if idx, ierr := webindex.Open(indexPath); ierr != nil {
		b.logger.Warn("web index unavailable", "path", indexPath, "error", ierr)
	} else {
		b.WebIndex = idx
		b.resources.Track("web-index", 100, func(context.Context) error { return idx.Close() })
		b.logger.Info("web index", "path", indexPath)
	}
	b.Retention = db.DefaultRetention()
	b.Revision, err = db.BumpStoreRevision(b.ctx, b.Database)
	if err != nil {
		return fmt.Errorf("store revision: %w", err)
	}

	b.Sessions = store.NewSQL(b.Database)
	b.Projects = project.NewSQLRegistry(b.Database)
	wireAgentPolicyRoots(b.ctx, b.Projects)
	b.SourceLedger = sourceledger.New(b.Database, filepath.Join(b.Directory, enginepaths.SourceContentDirName))
	b.SourceLedger.SetStoreGuard(b.Claim)
	b.SourceLedger.SetGitReader(gitStateReader{mgr: git.NewManager()})
	b.trackSourceStorage()
	return nil
}

func (b *Runtime) trackSourceStorage() {
	if b.SourceLedger == nil || b.SourceLedger.SnapshotStore() == nil {
		return
	}
	baselines := b.SourceLedger.BaselineStore()
	snapshots := b.SourceLedger.SnapshotStore()
	b.resources.Track("worker-baselines", 81, func(context.Context) error { return baselines.Close() })
	b.resources.Track("source-snapshots", 82, func(context.Context) error { return snapshots.Close() })
}

type ResourceLifetime interface {
	Track(string, int, func(context.Context) error)
	SetDB(*db.Store)
}

// wireAgentPolicyRoots protects the agent policy of every registered project,
// which other sessions load while this invocation is rooted elsewhere. A
// failed read keeps the last roots it saw.
func wireAgentPolicyRoots(ctx context.Context, registry *project.SQLRegistry) {
	var last atomic.Pointer[[]string]
	confine.SetAgentPolicyRootsSource(func() []string {
		paths, err := registry.RootPaths(context.WithoutCancel(ctx))
		if err != nil {
			if kept := last.Load(); kept != nil {
				return *kept
			}
			return nil
		}
		last.Store(&paths)
		return paths
	})
}
