package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCatalogSourceIndexAnswersFromTheHostCatalog(t *testing.T) {
	first, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve root", err)
	second, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve root", err)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(first, "pkg"), 0o755))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(first, "pkg", "a.go"), []byte("package pkg\n"), 0o644))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(second, "b.go"), []byte("package b\n"), 0o644))
	testutil.FailErr(t, "link", os.Symlink("pkg/a.go", filepath.Join(first, "link.go")))

	index := newCatalogSourceIndex("project-index", []adapter.PreparedRoot{
		{RootID: "first", SourcePath: first}, {RootID: "second", SourcePath: second},
	})
	_, err = sourcecatalog.Process().Observe(t.Context(), "project-index", index.roots)
	testutil.FailErr(t, "warm catalog", err)

	all, err := index.QueryIndex(t.Context(), consumer.SourceIndexQuery{})
	testutil.FailErr(t, "query all", err)
	kinds := map[layeredfs.QualifiedPath]layeredfs.EntryKind{}
	for _, entry := range all.Entries {
		if entry.ObservedAt.IsZero() {
			t.Fatalf("entry %v has no observation time", entry.Path)
		}
		kinds[entry.Path] = entry.Kind
	}
	for path, want := range map[layeredfs.QualifiedPath]layeredfs.EntryKind{
		{RootID: "first", Rel: "pkg"}:      layeredfs.EntryDirectory,
		{RootID: "first", Rel: "pkg/a.go"}: layeredfs.EntryRegular,
		{RootID: "first", Rel: "link.go"}:  layeredfs.EntrySymlink,
		{RootID: "second", Rel: "b.go"}:    layeredfs.EntryRegular,
	} {
		if kinds[path] != want {
			t.Fatalf("%v kind = %q, want %q (%v)", path, kinds[path], want, kinds)
		}
	}

	scoped, err := index.QueryIndex(t.Context(), consumer.SourceIndexQuery{RootIDs: []string{"second"}})
	testutil.FailErr(t, "query root", err)
	if len(scoped.Entries) != 1 || scoped.Entries[0].Path.RootID != "second" {
		t.Fatalf("root filter returned %+v", scoped.Entries)
	}
	prefixed, err := index.QueryIndex(t.Context(), consumer.SourceIndexQuery{RootIDs: []string{"first"}, PathPrefix: "pkg"})
	testutil.FailErr(t, "query prefix", err)
	if len(prefixed.Entries) != 1 || prefixed.Entries[0].Path.Rel != "pkg/a.go" {
		t.Fatalf("prefix returned %+v", prefixed.Entries)
	}
	subrooted, err := index.QueryIndex(t.Context(), consumer.SourceIndexQuery{RootIDs: []string{"first"}, Subroots: map[string]string{"first": "pkg"}})
	testutil.FailErr(t, "query subroot", err)
	if len(subrooted.Entries) != 1 {
		t.Fatalf("subroot returned %+v", subrooted.Entries)
	}
	bounded, err := index.QueryIndex(t.Context(), consumer.SourceIndexQuery{MaxResults: 1})
	testutil.FailErr(t, "query bounded", err)
	if len(bounded.Entries) != 1 || !bounded.Truncated {
		t.Fatalf("bound returned %d entries truncated=%v", len(bounded.Entries), bounded.Truncated)
	}
}

func TestCatalogCoverageNeverClaimsCompletenessItDoesNotHave(t *testing.T) {
	for name, c := range map[string]struct {
		snapshot sourcecatalog.Snapshot
		want     consumer.IndexCoverage
	}{
		"warming and empty":   {sourcecatalog.Snapshot{State: sourcecatalog.StateWarming}, consumer.CoverageAbsent},
		"warming with a part": {sourcecatalog.Snapshot{State: sourcecatalog.StateWarming, Entries: []sourcecatalog.Entry{{}}}, consumer.CoveragePartial},
		"walk budget ran out": {sourcecatalog.Snapshot{State: sourcecatalog.StateReady, RootBoundary: true}, consumer.CoveragePartial},
		"refreshing":          {sourcecatalog.Snapshot{State: sourcecatalog.StateReady, Refreshing: true}, consumer.CoverageStale},
		"overtaken by writes": {sourcecatalog.Snapshot{State: sourcecatalog.StateReady, Moving: true}, consumer.CoverageStale},
		"settled":             {sourcecatalog.Snapshot{State: sourcecatalog.StateReady}, consumer.CoverageComplete},
	} {
		if got := catalogCoverage(c.snapshot); got != c.want {
			t.Errorf("%s: coverage = %s, want %s", name, got, c.want)
		}
	}
}
