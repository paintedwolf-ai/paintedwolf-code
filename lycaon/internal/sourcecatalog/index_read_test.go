package sourcecatalog

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestIndexFilePagesKeepTheirGenerationAndBoundRows(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	for i := range TreeFilePageLimit + 7 {
		writeTreeTestFile(t, root.Path, fmt.Sprintf("dir/%04d.go", i), "source")
	}
	r := readyIndex(t, c, root)
	first, err := r.FilePage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, "", TreeFilePageLimit)
	testutil.FailErr(t, "first page", err)
	if len(first) != TreeFilePageLimit || r.RowsRead != TreeFilePageLimit {
		t.Fatalf("page=%d rows=%d", len(first), r.RowsRead)
	}
	writeTreeTestFile(t, root.Path, "dir/9999.go", "added")
	c.InvalidateRoot(root.Path, "dir/9999.go")
	second, err := r.FilePage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, first[len(first)-1].Path, TreeFilePageLimit)
	testutil.FailErr(t, "pinned next page", err)
	if len(second) != 7 || second[0].RootID != root.ID {
		t.Fatalf("next page=%+v", second)
	}
	for i, entry := range append(first, second...) {
		if entry.Path != fmt.Sprintf("dir/%04d.go", i) {
			t.Fatalf("path %d=%s", i, entry.Path)
		}
	}
}

func TestIndexPathCandidatesTreatQueryAsLiteralUnicode(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	for _, path := range []string{"Älpha.SQL", "alpha.sql", "a[b].txt", "a?b.txt", "a*b.txt"} {
		writeTreeTestFile(t, root.Path, path, "source")
	}
	r := readyIndex(t, c, root)
	for query, want := range map[string][]string{"äsql": {"Älpha.SQL"}, "[": {"a[b].txt"}, "?": {"a?b.txt"}, "*": {"a*b.txt"}} {
		var paths []string
		for _, tier := range []FilePathMatch{FileBasenamePrefix, FileBasenameContains, FilePathSubsequence} {
			found, err := r.MatchingFilePathsPage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, query, tier, "", 10)
			testutil.FailErr(t, "query paths", err)
			paths = append(paths, found...)
		}
		if !slices.Equal(paths, want) {
			t.Errorf("query=%q paths=%v want=%v", query, paths, want)
		}
	}
}

// TestIndexProjectionsShareOneGeneration: quick open and code search read the
// same rows through different predicates.
func TestIndexProjectionsShareOneGeneration(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "src/main.go", "source")
	writeTreeTestFile(t, root.Path, ".github/workflows/ci.yml", "workflow")
	r := readyIndex(t, c, root)

	all, err := r.FilePathsPage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, "", TreeFilePageLimit)
	testutil.FailErr(t, "all files", err)
	if !slices.Equal(all, []string{".github/workflows/ci.yml", "src/main.go"}) {
		t.Fatalf("all=%v", all)
	}
	visible, err := r.FilePathsPage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "visible files", err)
	if !slices.Equal(visible, []string{"src/main.go"}) {
		t.Fatalf("visible=%v", visible)
	}
	named, err := r.NamedFiles(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, []string{".github/workflows/ci.yml"})
	testutil.FailErr(t, "named files", err)
	if !slices.Equal(named, []string{".github/workflows/ci.yml"}) {
		t.Fatalf("named=%v", named)
	}
	addresses, err := r.FileAddressPage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, 0, "", 10)
	testutil.FailErr(t, "address page", err)
	if !slices.Equal(addresses, []string{"src/main.go", ".github/workflows/ci.yml"}) {
		t.Fatalf("addresses=%v want shortest first", addresses)
	}
}

func TestIndexLiteralPreparationSurvivesSearchCancellation(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "a.txt", "needle")
	writeTreeTestFile(t, root.Path, "b.txt", "unrelated")
	r := readyIndex(t, c, root)
	entries, err := r.FilePage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "read metadata", err)
	started, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	query := LiteralQuery{RootID: root.ID, IncludeKey: "test", Require: litprefilter.AnyOf("needle"), Open: func(entry Entry) (io.ReadCloser, error) {
		once.Do(func() { close(started) })
		<-release
		return literalTestOpener(root.Path)(entry)
	}}
	ctx, cancel := context.WithCancel(t.Context())
	page := c.IndexLiteralCandidates(ctx, r, query, entries)
	if len(page.Candidates) != 2 || !page.Warming {
		t.Fatalf("cold candidates=%d warming=%v", len(page.Candidates), page.Warming)
	}
	<-started
	cancel()
	releaseOnce.Do(func() { close(release) })
	r.store.mu.Lock()
	build := r.store.content[indexLiteralScopeKey(query)]
	r.store.mu.Unlock()
	if build == nil {
		t.Fatal("content preparation lost its scope")
	}
	<-build.done
	page = c.IndexLiteralCandidates(t.Context(), r, query, entries)
	if page.Warming || !slices.Equal(literalCandidatePaths(page.Candidates), []string{"a.txt"}) {
		t.Fatalf("warm candidates=%v warming=%v", literalCandidatePaths(page.Candidates), page.Warming)
	}
	writeTreeTestFile(t, root.Path, "b.txt", "needle!!!")
	c.InvalidateRoot(root.Path, "b.txt")
	page = c.IndexLiteralCandidates(t.Context(), r, query, entries)
	if !slices.Equal(literalCandidatePaths(page.Candidates), []string{"a.txt", "b.txt"}) {
		t.Fatalf("stale generation lost changed candidate: %v", literalCandidatePaths(page.Candidates))
	}
	testutil.FailErr(t, "join preparation", c.Drain(context.Background()))
}

func TestIndexLiteralScopesBoundAndDrainQueuedWorkers(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "a.txt", "source")
	reader := readyIndex(t, c, root)
	c.broker = backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{backgroundwork.ResourceIO: {Total: 1, PerLane: 1}})
	release, err := c.broker.Acquire(t.Context(), backgroundwork.Request{Lane: root.Path, Resources: []backgroundwork.Resource{backgroundwork.ResourceIO}})
	testutil.FailErr(t, "hold content admission", err)
	defer release()
	for i := range literalIndexCacheCap + 3 {
		c.prepareIndexLiterals(t.Context(), reader, LiteralQuery{IncludeKey: fmt.Sprint(i), Open: literalTestOpener(root.Path)})
	}
	reader.store.mu.Lock()
	count := len(reader.store.content)
	reader.store.mu.Unlock()
	if count != literalIndexCacheCap {
		t.Fatalf("active scopes=%d", count)
	}
	testutil.FailErr(t, "drain queued content", c.Drain(t.Context()))
	reader.store.mu.Lock()
	defer reader.store.mu.Unlock()
	for key, build := range reader.store.content {
		select {
		case <-build.done:
		default:
			t.Errorf("scope %s survived drain", key)
		}
	}
}

func TestIndexScopesKeepHumanSearchSeparateFromAgentMetadata(t *testing.T) {
	c, root := indexFixture(t)
	for _, name := range []string{"src/main.go", ".hidden.txt", ".paintedwolf/rules.yaml"} {
		writeIndexFile(t, root.Path, name, "needle")
	}
	reader := waitIndex(t, c, root)
	cases := []struct {
		scope FileScope
		want  []string
	}{
		{FileScope{Audience: HumanAudience, IncludeHidden: true}, []string{".hidden.txt", ".paintedwolf/rules.yaml", "src/main.go"}},
		{FileScope{Audience: AgentAudience, IncludeHidden: true}, []string{".hidden.txt", "src/main.go"}},
		{FileScope{Audience: HumanAudience}, []string{"src/main.go"}},
		{FileScope{Audience: AgentAudience}, []string{"src/main.go"}},
	}
	for _, tc := range cases {
		got, err := reader.FilePathsPage(t.Context(), tc.scope, "", 10)
		testutil.FailErr(t, "read scoped paths", err)
		count, err := reader.FileCount(t.Context(), tc.scope)
		testutil.FailErr(t, "read scoped count", err)
		if !slices.Equal(got, tc.want) || count != len(tc.want) {
			t.Fatalf("scope %+v: paths=%v count=%d want %v", tc.scope, got, count, tc.want)
		}
	}
}
