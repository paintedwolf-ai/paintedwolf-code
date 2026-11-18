// Package repotest creates repository providers with test-scoped resources.
package repotest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// NewProvider releases analysis workers and catalog connections at cleanup.
func NewProvider(t testing.TB) repoinfo.Provider {
	t.Helper()
	catalog := sourcecatalog.New()
	provider := repoinfo.NewProvider(catalog.Trees, func(_ context.Context, path string) (repoinfo.CatalogRoot, bool, error) {
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

// AwaitBrief accepts settled summaries even when coverage has omissions.
func AwaitBrief(ctx context.Context, p repoinfo.Provider, projectDir string) (*repoinfo.Brief, error) {
	p.Warm(projectDir)
	for {
		brief, err := p.Brief(ctx, projectDir)
		if err != nil {
			return nil, err
		}
		if brief != nil && brief.Materialized && !brief.Refreshing {
			return brief, nil
		}
		select {
		case <-ctx.Done():
			return brief, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// BriefTextBytes counts the text rendered by the board's per-root layout renderer.
func BriefTextBytes(roots []api.BoardOrientationRoot) int {
	total := 0
	for _, root := range roots {
		total += len(strings.Join(packboard.RepoOrientationLines(root.Brief), "\n"))
	}
	return total
}
