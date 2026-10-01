package repoinfo

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

type emptinessIndex struct {
	*sourcecatalog.Catalog
	files sourcecatalog.RootFiles
}

func (s emptinessIndex) RootFileCount(ctx context.Context, _ string, _ sourcecatalog.Root, _ sourcecatalog.FileScope, wait time.Duration) (sourcecatalog.RootFiles, error) {
	if wait > 0 {
		<-ctx.Done()
		return sourcecatalog.RootFiles{}, ctx.Err()
	}
	return s.files, nil
}

func TestKnownEmptyUsesAvailableCurrentMeasurement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files sourcecatalog.RootFiles
		empty bool
	}{
		{name: "cold"},
		{name: "incomplete", files: sourcecatalog.RootFiles{}},
		{name: "refreshing", files: sourcecatalog.RootFiles{}},
		{name: "measured empty", files: sourcecatalog.RootFiles{Measured: true}, empty: true},
		{name: "occupied", files: sourcecatalog.RootFiles{Count: 1, Measured: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newProviderWithAnalyze(nil)
			defer func() { _ = p.Close() }()
			p.catalog = emptinessIndex{files: tc.files}
			p.resolveCatalogRoot = func(context.Context, string) (CatalogRoot, bool, error) {
				return CatalogRoot{ProjectID: "p", RootID: "r"}, true, nil
			}
			ctx := testutil.BoundedContext(t, time.Second)
			empty, err := p.KnownEmpty(ctx, t.TempDir())
			testutil.FailErr(t, "read available emptiness", err)
			if empty != tc.empty {
				t.Fatalf("empty=%v, want %v", empty, tc.empty)
			}
		})
	}
}
