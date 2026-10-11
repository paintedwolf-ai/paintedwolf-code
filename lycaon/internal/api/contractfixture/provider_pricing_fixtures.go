package contractfixture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/pricing"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

type ApiStubFeed struct {
	Doc *modelfeed.Document
}

func (s *ApiStubFeed) Document(context.Context) (*modelfeed.Document, error) { return s.Doc, nil }

func (s *ApiStubFeed) Refresh(context.Context) (*modelfeed.Document, error) { return s.Doc, nil }

func (s *ApiStubFeed) Snapshot() (*modelfeed.Document, string, bool) {
	return s.Doc, string(pricing.StatusOK), s.Doc != nil
}

func PricingNoNetworkFetch(context.Context, string) ([]byte, error) {
	return nil, errors.New("no network in pricing settings tests")
}

// withTestPricingHost serves pricing through a host over the settings pricing store.

func WithTestPricingHost(t *testing.T) TestDeps {
	t.Helper()
	return func(d *hostapi.Dependencies) {
		if d.Core.Settings == nil || d.Core.Settings.Pricing == nil {
			t.Fatal("settings pricing store missing")
		}
		mem := costtest.NewTracker(t, cost.NoopPricer{})
		body, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "internal", "pricing", "testdata", "models-dev", "api.json"))
		testutil.FailErr(t, "read models-dev fixture", err)
		doc, err := modelfeed.ParseDocument(body)
		testutil.FailErr(t, "ParseDocument", err)
		host := &settings.PricingHost{
			Store:     d.Core.Settings.Pricing,
			Catalog:   pricing.SourcesConfig{},
			CacheDir:  filepath.Join(t.TempDir(), "pricing-cache"),
			Tracker:   mem,
			GetBytes:  PricingNoNetworkFetch,
			ModelFeed: &ApiStubFeed{Doc: doc},
		}
		// Catalog rows come from the store; registry only needs Sources when tracking turns on.
		for _, ent := range d.Core.Settings.Pricing.Catalog() {
			host.Catalog.Sources = append(host.Catalog.Sources, pricing.SourceConfig{
				ID:    ent.ID,
				Kind:  ent.Kind,
				Label: ent.Label,
				URL:   ent.URL,
			})
		}
		testutil.FailErr(t, "SyncFromStore", host.SyncFromStore(t.Context()))
		d.Host.Pricing = host
		t.Cleanup(func() {
			host.Close()
		})
	}
}
