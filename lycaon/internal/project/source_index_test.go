package project

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBuildSourceIndexSummarizesAndRanks(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "mkdir source", os.MkdirAll(filepath.Join(root, "src", "models"), 0o755))
	for path, body := range map[string]string{
		"README.md":                  "read me",
		"AGENTS.md":                  "instructions",
		"src/models/account.go":      "package models",
		"src/models/account_test.go": "package models",
		"src/main.go":                "package main",
	} {
		testutil.FailErr(t, "write "+path, os.WriteFile(filepath.Join(root, path), []byte(body), 0o644))
	}
	p := &Project{ID: "p1", Roots: []Root{{ID: "r1", Path: root}}}
	catalog := sourceIndexTestCatalog(t)
	warmSourceIndex(t, catalog, p)
	snapshot := (&SourceIndexCache{catalog: catalog}).Snapshot(t.Context(), p)
	defer snapshot.Close()
	if snapshot.State != SourceIndexReady || snapshot.FileCount != 5 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if got := len(snapshot.Roots[0].Resources); got != 2 {
		t.Fatalf("resources = %d, want 2", got)
	}
	matches, err := SearchSourceIndex(t.Context(), snapshot, SourceQuery{Path: "account"}, SourcePathStyle{}, "r1", nil, 10)
	testutil.FailErr(t, "rank source paths", err)
	if len(matches) != 2 || matches[0].Path != "src/models/account.go" {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestSourceIndexServesLastReadyGenerationWhileRefreshing(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write first file", os.WriteFile(filepath.Join(root, "first.go"), []byte("package first"), 0o644))
	p := &Project{ID: "p1", Roots: []Root{{ID: "r1", Path: root}}}
	catalog := sourceIndexTestCatalog(t)
	warmSourceIndex(t, catalog, p)

	testutil.FailErr(t, "write second file", os.WriteFile(filepath.Join(root, "second.go"), []byte("package second"), 0o644))
	repochange.Advance(root)
	cache := &SourceIndexCache{catalog: catalog}
	snapshot := cache.Snapshot(t.Context(), p)
	defer snapshot.Close()
	if snapshot.State != SourceIndexReady || snapshot.FileCount == 0 {
		t.Fatalf("refreshing snapshot = %+v, want a usable ready generation", snapshot)
	}
	warmSourceIndex(t, catalog, p)
	updated := cache.Snapshot(t.Context(), p)
	defer updated.Close()
	if updated.State != SourceIndexReady || updated.FileCount != 2 {
		t.Fatalf("refreshed snapshot = %+v", updated)
	}
}

func sourceIndexTestCatalog(t *testing.T) *sourcecatalog.Catalog {
	t.Helper()
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	catalog := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	return catalog
}

// warmSourceIndex waits for each root to finish discovery.
func warmSourceIndex(t *testing.T, catalog *sourcecatalog.Catalog, p *Project) {
	t.Helper()
	for _, root := range p.Roots {
		until := time.Now().Add(time.Minute)
		for {
			reader, status, err := catalog.OpenIndex(t.Context(), p.ID, sourcecatalog.Root{ID: root.ID, Path: root.Path}, time.Second)
			testutil.FailErr(t, "discover source index", err)
			if reader != nil {
				testutil.FailErr(t, "close index reader", reader.Close())
				if status.Complete && !status.Refreshing {
					break
				}
			}
			if time.Now().After(until) {
				t.Fatalf("source index not ready: %+v", status)
			}
		}
	}
}

func TestSourceIndexContinuationRanksAcrossMetadataPages(t *testing.T) {
	catalog := sourceIndexTestCatalog(t)
	root := t.TempDir()
	for i := range sourcecatalog.TreeFilePageLimit + 17 {
		testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%04d.sql", i)), []byte("source"), 0o600))
	}
	p := &Project{ID: "project", Roots: []Root{{ID: "root", Path: root}}}
	warmSourceIndex(t, catalog, p)
	view := (&SourceIndexCache{catalog: catalog}).Snapshot(t.Context(), p)
	defer view.Close()
	var got []string
	var after *SourceIndexEntry
	for {
		page, err := SearchSourceIndex(t.Context(), view, SourceQuery{Path: "sql"}, SourcePathStyle{}, "", after, 71)
		testutil.FailErr(t, "rank page", err)
		if len(page) == 0 {
			break
		}
		for _, entry := range page {
			got = append(got, entry.Path)
		}
		after = &page[len(page)-1].SourceIndexEntry
	}
	if len(got) != sourcecatalog.TreeFilePageLimit+17 || len(slices.Compact(slices.Clone(got))) != len(got) {
		t.Fatalf("paths lost or repeated: %d", len(got))
	}
	if !slices.IsSorted(got) {
		t.Fatal("equal-ranked pages are out of order")
	}
}

func TestSourceIndexContinuationsCrossRootsAndRankingTiers(t *testing.T) {
	catalog := sourceIndexTestCatalog(t)
	p := &Project{ID: "project"}
	for _, id := range []string{"z", "a"} {
		root := t.TempDir()
		for _, path := range []string{"a.sql", "ba.sql", "a/zz"} {
			testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o700))
			testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, path), []byte("source"), 0o600))
		}
		p.Roots = append(p.Roots, Root{ID: id, Path: root})
	}
	warmSourceIndex(t, catalog, p)
	view := (&SourceIndexCache{catalog: catalog}).Snapshot(t.Context(), p)
	defer view.Close()
	for query, want := range map[string][]string{
		"a": {"a:a.sql", "z:a.sql", "a:ba.sql", "z:ba.sql", "a:a/zz", "z:a/zz"},
		"":  {"a:a/zz", "z:a/zz", "a:a.sql", "z:a.sql", "a:ba.sql", "z:ba.sql"},
	} {
		var got []string
		var after *SourceIndexEntry
		for range 7 {
			page, err := SearchSourceIndex(t.Context(), view, SourceQuery{Path: query}, SourcePathStyle{}, "", after, 1)
			testutil.FailErr(t, "read continuation", err)
			if len(page) == 0 {
				break
			}
			got = append(got, page[0].RootID+":"+page[0].Path)
			after = &page[0].SourceIndexEntry
		}
		if !slices.Equal(got, want) {
			t.Errorf("query=%q paths=%v want=%v", query, got, want)
		}
	}
}

func TestSourceIndexMatchesAnyPortionOfTheFullPath(t *testing.T) {
	catalog := sourceIndexTestCatalog(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "Painted")
	for _, path := range []string{
		"docs/operations/release.md",
		".task/before/docs/operations/release.md",
		"painted/docs/notes.md",
		"README.md",
	} {
		testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(path))), 0o700))
		testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte("source"), 0o600))
	}
	p := &Project{ID: "project", Roots: []Root{{ID: "root", Path: root}}}
	warmSourceIndex(t, catalog, p)
	view := (&SourceIndexCache{catalog: catalog}).Snapshot(t.Context(), p)
	defer view.Close()
	resolved, err := filepath.EvalSymlinks(root)
	testutil.FailErr(t, "resolve root", err)
	style := SourcePathStyle{Windows: filepath.Separator == '\\'}
	for _, tc := range []struct {
		name, raw string
		want      []string
	}{
		{"relative", "docs/operations/release.md", []string{"docs/operations/release.md", ".task/before/docs/operations/release.md"}},
		{"absolute", filepath.Join(root, "docs", "operations", "release.md"), []string{"docs/operations/release.md", ".task/before/docs/operations/release.md"}},
		{"absolute through links", filepath.Join(resolved, "docs", "operations", "release.md"), []string{"docs/operations/release.md", ".task/before/docs/operations/release.md"}},
		{"root folder and fuzzy rest", "painted/docs/oper", []string{"docs/operations/release.md", ".task/before/docs/operations/release.md"}},
		{"equal windows prefer shorter paths", "painted/docs/", []string{"painted/docs/notes.md", "docs/operations/release.md", ".task/before/docs/operations/release.md"}},
		{"partial root folder", "ted/readme", []string{"README.md"}},
		{"rooted", "/docs/operations", []string{"docs/operations/release.md", ".task/before/docs/operations/release.md"}},
		{"root folder alone lists its files", "painted/", []string{"README.md", "painted/docs/notes.md", "docs/operations/release.md", ".task/before/docs/operations/release.md"}},
		{"another checkout of the same tree", filepath.Join(parent, "Other", "docs", "operations", "release.md"), []string{"docs/operations/release.md", ".task/before/docs/operations/release.md"}},
		{"module path", "github.com/painted/wolf/docs/operations/release.md", []string{"docs/operations/release.md", ".task/before/docs/operations/release.md"}},
		{"nothing to relax to", "missing/nowhere.md", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matches, err := SearchSourceIndex(t.Context(), view, ParseSourceQuery(tc.raw, style), style, "", nil, 10)
			testutil.FailErr(t, "search", err)
			var got []string
			for _, match := range matches {
				got = append(got, match.Path)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("query %q = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSourceIndexHighlightsWhatEachReadingMatched(t *testing.T) {
	catalog := sourceIndexTestCatalog(t)
	root := filepath.Join(t.TempDir(), "repo")
	for _, path := range []string{"src/Model.go", "src/İx/Model.go", "docs/operations/release.md"} {
		testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(path))), 0o700))
		testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte("source"), 0o600))
	}
	p := &Project{ID: "project", Roots: []Root{{ID: "root", Path: root}}}
	warmSourceIndex(t, catalog, p)
	view := (&SourceIndexCache{catalog: catalog}).Snapshot(t.Context(), p)
	defer view.Close()
	for _, tc := range []struct{ query, path, want string }{
		{"mod", "src/Model.go", "Mod"},
		{"odel", "src/İx/Model.go", "odel"},
		{"src/mo", "src/Model.go", "src/Mo"},
		{"dopr", "docs/operations/release.md", "d|op|r"},
		{"docsrel", "docs/operations/release.md", "docs|rel"},
		{"repo/docs/rel", "docs/operations/release.md", "docs/|rel"},
		{"repo/", "src/Model.go", ""},
	} {
		matches, err := SearchSourceIndex(t.Context(), view, SourceQuery{Path: tc.query}, SourcePathStyle{}, "", nil, 10)
		testutil.FailErr(t, "search", err)
		index := slices.IndexFunc(matches, func(m SourceIndexMatch) bool { return m.Path == tc.path })
		if index < 0 {
			t.Fatalf("query %q missed %s: %+v", tc.query, tc.path, matches)
		}
		runes := []rune(matches[index].Path)
		var parts []string
		for _, span := range matches[index].Highlights {
			parts = append(parts, string(runes[span.Start:span.End]))
		}
		if got := strings.Join(parts, "|"); got != tc.want {
			t.Errorf("query %q highlighted %q in %s, want %q", tc.query, got, tc.path, tc.want)
		}
	}
}

func TestSourceIndexContinuationsCoverAnchoredReadingsOnce(t *testing.T) {
	catalog := sourceIndexTestCatalog(t)
	root := filepath.Join(t.TempDir(), "app")
	for _, path := range []string{"a.go", "app/a.go", "app/b/a.go", "x/app/a.go", "b.go"} {
		testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(path))), 0o700))
		testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte("source"), 0o600))
	}
	p := &Project{ID: "project", Roots: []Root{{ID: "root", Path: root}}}
	warmSourceIndex(t, catalog, p)
	view := (&SourceIndexCache{catalog: catalog}).Snapshot(t.Context(), p)
	defer view.Close()
	for query, want := range map[string][]string{
		// The root folder is named app, so each query also matches through it; equal windows order by length.
		"app/a": {"a.go", "app/a.go", "app/b/a.go", "x/app/a.go"},
		"app/":  {"a.go", "b.go", "app/a.go", "app/b/a.go", "x/app/a.go"},
	} {
		var got []string
		var after *SourceIndexEntry
		for range 8 {
			page, err := SearchSourceIndex(t.Context(), view, SourceQuery{Path: query}, SourcePathStyle{}, "", after, 1)
			testutil.FailErr(t, "read continuation", err)
			if len(page) == 0 {
				break
			}
			got = append(got, page[0].Path)
			after = &page[0].SourceIndexEntry
		}
		if !slices.Equal(got, want) {
			t.Errorf("query=%q paths=%v want=%v", query, got, want)
		}
	}
}

func TestSourceIndexResolvesCodeHostLinksInTheirCheckout(t *testing.T) {
	catalog := sourceIndexTestCatalog(t)
	root := t.TempDir()
	for _, path := range []string{"src/a.go", "x/src/a.go"} {
		testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(path))), 0o700))
		testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte("source"), 0o600))
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
		{"branch", "feature/x"},
		{"remote", "add", "origin", "git@github.com:Painted/Wolf.git"},
	} {
		out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...).CombinedOutput()
		testutil.FailErr(t, "git "+strings.Join(args, " ")+": "+string(out), err)
	}
	p := &Project{ID: "project", Roots: []Root{{ID: "root", Path: root}}}
	warmSourceIndex(t, catalog, p)
	view := (&SourceIndexCache{catalog: catalog}).Snapshot(t.Context(), p)
	defer view.Close()
	for _, tc := range []struct{ link, want string }{
		// The checkout's refs settle that the ref is feature/x, though x/src/a.go exists too.
		{"https://github.com/painted/wolf/blob/feature/x/src/a.go#L3", "src/a.go"},
		{"https://github.com/painted/wolf/blob/main/x/src/a.go", "x/src/a.go"},
		{"https://github.com/painted/wolf/blob/0123abc/x/src/a.go", "x/src/a.go"},
		// Another repository's link still matches by its path after the ref.
		{"https://github.com/someone/else/blob/main/src/a.go", "src/a.go"},
	} {
		query := ParseSourceQuery(tc.link, SourcePathStyle{})
		matches, err := SearchSourceIndex(t.Context(), view, query, SourcePathStyle{}, "", nil, 10)
		testutil.FailErr(t, "search", err)
		if len(matches) == 0 || matches[0].Path != tc.want {
			t.Errorf("link %s = %+v, want %s first", tc.link, matches, tc.want)
		}
	}
}

func TestSourceIndexReportsExistingPathsOutsideEveryRoot(t *testing.T) {
	catalog := sourceIndexTestCatalog(t)
	root := t.TempDir()
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, "inside.md"), []byte("source"), 0o600))
	elsewhere := t.TempDir()
	testutil.FailErr(t, "write outside", os.WriteFile(filepath.Join(elsewhere, "notes.md"), []byte("notes"), 0o600))
	p := &Project{ID: "project", Roots: []Root{{ID: "root", Path: root}}}
	warmSourceIndex(t, catalog, p)
	view := (&SourceIndexCache{catalog: catalog}).Snapshot(t.Context(), p)
	defer view.Close()
	style := SourcePathStyle{Windows: filepath.Separator == '\\'}
	for _, tc := range []struct {
		raw     string
		want    SourceOutsidePath
		outside bool
	}{
		{filepath.Join(elsewhere, "notes.md") + ":4", SourceOutsidePath{Path: filepath.Join(elsewhere, "notes.md")}, true},
		{elsewhere, SourceOutsidePath{Path: elsewhere, Directory: true}, true},
		{filepath.Join(root, "inside.md"), SourceOutsidePath{}, false},
		{filepath.Join(elsewhere, "missing.md"), SourceOutsidePath{}, false},
		{"notes.md", SourceOutsidePath{}, false},
	} {
		got, outside := view.OutsidePath(ParseSourceQuery(tc.raw, style), style)
		if outside != tc.outside || got != tc.want {
			t.Errorf("OutsidePath(%q) = %+v %t, want %+v %t", tc.raw, got, outside, tc.want, tc.outside)
		}
	}
}

func TestGitRemoteHashIdentifiesTheRepositoryNotItsSpelling(t *testing.T) {
	hash := func(remote string) string {
		dir := t.TempDir()
		for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
			out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
			testutil.FailErr(t, "git "+strings.Join(args, " ")+": "+string(out), err)
		}
		return gitRemoteHash(t.Context(), dir)
	}
	ssh, https := hash("git@github.com:Painted/Wolf.git"), hash("https://github.com/painted/wolf")
	if ssh == "" || ssh != https {
		t.Fatalf("hashes differ for one repository: %q %q", ssh, https)
	}
	if other := hash("https://github.com/painted/other"); other == ssh {
		t.Fatal("different repositories share a hash")
	}
}
