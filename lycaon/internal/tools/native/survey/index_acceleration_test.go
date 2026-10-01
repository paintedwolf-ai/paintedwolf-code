package survey

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestListDir_CatalogFastPathAndParity(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		name := filepath.Join(dir, fmt.Sprintf("file_%02d.txt", i))
		testutil.FailErr(t, "write file", os.WriteFile(name, []byte("content\n"), 0o644))
	}
	testutil.FailErr(t, "mkdir", os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	testutil.FailErr(t, "write subfile", os.WriteFile(filepath.Join(dir, "sub", "subfile.txt"), []byte("sub\n"), 0o644))

	catalog := sourcecatalog.New()
	_, err := catalog.Observe(context.Background(), "test-proj", []sourcecatalog.Root{{ID: "r1", Path: dir}})
	testutil.FailErr(t, "observe root", err)

	toolWithCat := &ListDirTool{Boundary: nativefixture.Boundary(t), Catalog: catalog}
	toolWithoutCat := &ListDirTool{Boundary: nativefixture.Boundary(t)}

	outCat, err := toolWithCat.Run(context.Background(), map[string]any{"path": ".", "max_depth": 1}, nativefixture.Context(dir))
	testutil.FailErr(t, "run list_dir with cat", err)

	outDisk, err := toolWithoutCat.Run(context.Background(), map[string]any{"path": ".", "max_depth": 1}, nativefixture.Context(dir))
	testutil.FailErr(t, "run list_dir without cat", err)

	var respCat, respDisk listDirResponse
	testutil.FailErr(t, "unmarshal cat", json.Unmarshal([]byte(outCat), &respCat))
	testutil.FailErr(t, "unmarshal disk", json.Unmarshal([]byte(outDisk), &respDisk))

	if respCat.TotalEntries != respDisk.TotalEntries {
		t.Fatalf("total_entries mismatch: cat=%d disk=%d", respCat.TotalEntries, respDisk.TotalEntries)
	}
	if len(respCat.Entries) != len(respDisk.Entries) {
		t.Fatalf("entries len mismatch: cat=%d disk=%d", len(respCat.Entries), len(respDisk.Entries))
	}
	for i := range respCat.Entries {
		if respCat.Entries[i].Name != respDisk.Entries[i].Name {
			t.Errorf("entry[%d].Name mismatch: cat=%s disk=%s", i, respCat.Entries[i].Name, respDisk.Entries[i].Name)
		}
		if respCat.Entries[i].Type != respDisk.Entries[i].Type {
			t.Errorf("entry[%d].Type mismatch: cat=%s disk=%s", i, respCat.Entries[i].Type, respDisk.Entries[i].Type)
		}
	}
}

func TestStat_CatalogDirectoryEntryCount(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "folder")
	testutil.FailErr(t, "mkdir", os.Mkdir(sub, 0o755))
	for i := 0; i < 7; i++ {
		testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(sub, fmt.Sprintf("item_%d.txt", i)), []byte("data\n"), 0o644))
	}

	catalog := sourcecatalog.New()
	_, err := catalog.Observe(context.Background(), "test-proj", []sourcecatalog.Root{{ID: "r1", Path: dir}})
	testutil.FailErr(t, "observe root", err)

	tool := &StatTool{Boundary: nativefixture.Boundary(t), Catalog: catalog}
	out, err := tool.Run(context.Background(), map[string]any{"paths": []any{"folder"}}, nativefixture.Context(dir))
	testutil.FailErr(t, "run stat", err)

	var resp statResponse
	testutil.FailErr(t, "unmarshal stat", json.Unmarshal([]byte(out), &resp))
	if len(resp.Results) != 1 {
		t.Fatalf("results len=%d want 1", len(resp.Results))
	}
	if resp.Results[0].EntryCount == nil || *resp.Results[0].EntryCount != 7 {
		t.Fatalf("entry_count=%v want 7", resp.Results[0].EntryCount)
	}
}

func TestWc_ParallelLineCountParity(t *testing.T) {
	dir := t.TempDir()
	totalLinesExpected := 0
	for i := 0; i < 50; i++ {
		lines := (i % 10) + 1
		totalLinesExpected += lines
		content := ""
		for j := 0; j < lines; j++ {
			content += fmt.Sprintf("line %d in file %d\n", j, i)
		}
		testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, fmt.Sprintf("f_%02d.txt", i)), []byte(content), 0o644))
	}

	catalog := sourcecatalog.New()
	tool := &WcTool{Boundary: nativefixture.Boundary(t), Catalog: catalog}

	out, err := tool.Run(context.Background(), map[string]any{"paths": []any{"."}, "recursive": true}, nativefixture.Context(dir))
	testutil.FailErr(t, "run wc", err)

	var resp wcResponse
	testutil.FailErr(t, "unmarshal wc", json.Unmarshal([]byte(out), &resp))
	if len(resp.Results) != 1 {
		t.Fatalf("results len=%d want 1", len(resp.Results))
	}
	if resp.Results[0].Lines == nil || *resp.Results[0].Lines != totalLinesExpected {
		t.Fatalf("lines=%v want %d", resp.Results[0].Lines, totalLinesExpected)
	}
	if resp.Results[0].Files == nil || *resp.Results[0].Files != 50 {
		t.Fatalf("files=%v want 50", resp.Results[0].Files)
	}
}

func TestRead_ReferencesResolvedFromCatalog(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write doc", os.WriteFile(filepath.Join(dir, "README.md"), []byte("See `target.go` for implementation details.\n"), 0o644))
	testutil.FailErr(t, "write target", os.WriteFile(filepath.Join(dir, "target.go"), []byte("package main\n"), 0o644))

	catalog := sourcecatalog.New()
	_, err := catalog.Observe(context.Background(), "test-proj", []sourcecatalog.Root{{ID: "r1", Path: dir}})
	testutil.FailErr(t, "observe root", err)

	tool := &ReadTool{Boundary: nativefixture.Boundary(t), Catalog: catalog}
	out, err := tool.Run(context.Background(), map[string]any{"path": "README.md"}, nativefixture.Context(dir))
	testutil.FailErr(t, "run read", err)

	var resp ReadResponse
	testutil.FailErr(t, "unmarshal read", json.Unmarshal([]byte(out), &resp))
	if resp.Path != "README.md" {
		t.Fatalf("path=%s want README.md", resp.Path)
	}
}

func TestSummarize_PatternBloomFilterPruning(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		name := filepath.Join(dir, "noise_"+strconv.Itoa(i)+".txt")
		testutil.FailErr(t, "write noise", os.WriteFile(name, []byte("plain text with no match token\n"), 0o644))
	}
	testutil.FailErr(t, "write match", os.WriteFile(filepath.Join(dir, "match.txt"), []byte("here is the UniqueBloomTargetToken!\n"), 0o644))

	catalog := sourcecatalog.New()
	snapshot, err := catalog.Observe(context.Background(), "test-proj", []sourcecatalog.Root{{ID: "r1", Path: dir}})
	testutil.FailErr(t, "observe", err)

	// Populate Bloom filters.
	opener := func(entry sourcecatalog.Entry) (io.ReadCloser, error) {
		return os.Open(filepath.Join(dir, filepath.FromSlash(entry.Path)))
	}
	_, err = catalog.LiteralCandidates(context.Background(), snapshot, sourcecatalog.LiteralQuery{
		RootID: "r1", Base: ".", Require: litprefilter.AnyOf("UniqueBloomTargetToken"), Open: opener,
	})
	testutil.FailErr(t, "populate blooms", err)

	tool := testSummarizeTool(t, dir)
	tool.Catalog = catalog
	raw, err := tool.Run(context.Background(), map[string]any{"path": ".", "pattern": "UniqueBloomTargetToken"}, nativefixture.Context(dir))
	testutil.FailErr(t, "run summarize pattern", err)
	res := decodeSummarizeResponse(t, raw)
	if res.Coverage.MatchesObserved != 1 {
		t.Fatalf("matches=%d want 1", res.Coverage.MatchesObserved)
	}
}
