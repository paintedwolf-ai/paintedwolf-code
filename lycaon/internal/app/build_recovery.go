package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/db"
)

// buildRecoveryApp serves recovery routes without opening the refused store.
func buildRecoveryApp(ctx context.Context, cfg Config, b *serveBuilder, openErr error) (*ServeApp, error) {
	dbPath, err := cfg.resolvedDBPath()
	if err != nil {
		return nil, err
	}
	addr, err := cfg.resolvedListenAddr()
	if err != nil {
		return nil, fmt.Errorf("listen address: %w", err)
	}
	token, generated, err := resolveServeAPIToken()
	if err != nil {
		return nil, fmt.Errorf("api token: %w", err)
	}

	var incompatible *db.StoreIncompatibleError
	_ = errors.As(openErr, &incompatible)

	logger := b.logger
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
		resources:      b.resources,
		// Recovery emits readiness so the shell can render restore controls.
		startup: cfg.Startup,
	}, nil
}
