// Package repotest creates repository providers with test-scoped resources.
package repotest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

// NewProvider releases analysis workers and catalog connections at cleanup.
func NewProvider(t testing.TB) repoinfo.Provider {
	t.Helper()
	catalog := sourcecatalog.New()
	provider := repoinfo.NewProvider(catalog, func(_ context.Context, path string) (repoinfo.CatalogRoot, bool, error) {
		root, err := filepath.Abs(path)
		return repoinfo.CatalogRoot{ProjectID: root, RootID: root}, err == nil, err
	}, "")
	t.Cleanup(func() {
		testutil.FailErr(t, "close repository provider", provider.Close())
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		testutil.FailErr(t, "drain repository catalog", catalog.Drain(ctx))
	})
	return provider
}
