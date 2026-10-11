package processes

import (
	"context"
	"github.com/lycaon/lycaon/internal/userpath"
	"log/slog"
)

type ResourceLifetime interface {
	Track(string, int, func(context.Context) error)
}
type Runtime struct {
	Path      userpath.Snapshot
	logger    *slog.Logger
	resources ResourceLifetime
}

func New(logger *slog.Logger, resources ResourceLifetime) *Runtime {
	return &Runtime{logger: logger, resources: resources}
}
