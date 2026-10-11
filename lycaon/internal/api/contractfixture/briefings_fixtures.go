package contractfixture

import (
	"context"
	"log/slog"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/filebriefing"
)

func WithTestFileBriefings(t *testing.T, store filebriefing.Store, cfg filebriefing.Config) TestDeps {
	t.Helper()
	return func(d *hostapi.Dependencies) {
		var enabled filebriefing.Enablement
		if d.Core.Settings != nil {
			enabled = d.Core.Settings.FileSummaries
		}
		service := filebriefing.NewService(t.Context(), filebriefing.Dependencies{
			Store: store, Config: cfg, Generator: filebriefing.NewModelGenerator(d.Providers.LLM, d.Providers.CostTracker), Events: d.Host.Events, Settings: enabled, Logger: slog.Default(),
		})
		t.Cleanup(func() { service.Stop(); service.Wait(context.Background()) })
		d.Source.FileBriefings = service
	}
}
