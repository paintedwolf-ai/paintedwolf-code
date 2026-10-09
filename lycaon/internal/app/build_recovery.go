package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/app/configuration"
	"log/slog"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/db"
)

// buildRecoveryApp serves recovery routes without opening the refused store.
func buildRecoveryApp(ctx context.Context, cfg configuration.Config, b *serveBuilder, openErr error) (*ServeApp, error) {
	dbPath, err := cfg.ResolveDBPath()
	if err != nil {
		return nil, err
	}
	addr, err := cfg.ResolveListenAddr()
	if err != nil {
		return nil, fmt.Errorf("listen address: %w", err)
	}
	token, generated, err := resolveServeAPIToken()
	if err != nil {
		return nil, fmt.Errorf("api token: %w", err)
	}

	var incompatible *db.StoreIncompatibleError
	_ = errors.As(openErr, &incompatible)

	logger := b.startup.logger
	if logger == nil {
		logger = slog.Default()
	}
	reason := db.RecoveryReasonIntegrityFailed
	storeVer := 0
	detail := openErr.Error()
	if incompatible != nil {
		reason = incompatible.Reason
		storeVer = incompatible.StoreSchemaVersion
		detail = incompatible.Detail
	}
	logger.WarnContext(ctx, "store incompatible — starting recovery mode",
		"recovery_reason", reason,
		"schema_version", db.SchemaVersion,
		"store_schema_version", storeVer,
		"detail", detail,
	)

	dataDir := filepath.Dir(dbPath)
	srv := api.NewRecoveryServer(ctx, api.RecoveryServerOpts{
		DBPath:       dbPath,
		DataDir:      dataDir,
		APIToken:     token,
		Incompatible: incompatible,
		Logger:       logger,
	})

	return &ServeApp{
		Server:         srv,
		ConfigRoot:     dataDir,
		ListenAddr:     addr,
		APIToken:       token,
		TokenGenerated: generated,
		resources:      b.startup.resources,
		// Recovery emits readiness so the shell can render restore controls.
		startup: cfg.Startup,
	}, nil
}
