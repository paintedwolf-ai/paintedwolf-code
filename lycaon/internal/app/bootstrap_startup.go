package app

import (
	"context"
	"github.com/lycaon/lycaon/internal/app/configuration"
	"log/slog"

	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/observability"
)

type startupBootstrap struct {
	ctx       context.Context
	cfg       configuration.Config
	logger    *slog.Logger
	resources *runtimeResources
	recovery  *bootrecovery.Registry
	addr      string
}

func (b *startupBootstrap) closeFailedBuild(ctx context.Context) {
	_ = b.resources.Close(ctx)
}

func (b *startupBootstrap) initObservability() error {
	var err error
	b.logger = observability.NewServeLogger()
	slog.SetDefault(b.logger)

	b.addr, err = b.cfg.ResolveListenAddr()
	if err != nil {
		return err
	}
	return nil
}
