package search

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

// indexedHarness observes the fixture tree and builds every file's Bloom
// filter, as the structured grep tool's index warm-up does.
func indexedHarness(t *testing.T, files map[string]string) (*harness, *sourcecatalog.Catalog) {
	t.Helper()
	h := newHarness(t, files)
	catalog := sourcecatalog.New()
	snapshot, err := catalog.Observe(context.Background(), "project", []sourcecatalog.Root{{ID: "root", Path: h.root}})
	testutil.FailErr(t, "observe", err)
	_, err = catalog.Literals.LiteralCandidates(context.Background(), snapshot, sourcecatalog.LiteralQuery{
		RootID: "root", Base: ".", Require: litprefilter.AnyOf("warm-up"),
		Open: func(entry sourcecatalog.Entry) (io.ReadCloser, error) {
			return os.Open(filepath.Join(h.root, filepath.FromSlash(entry.Path)))
		},
	})
	testutil.FailErr(t, "build blooms", err)
	return h, catalog
}

func indexCorpus() map[string]string {
	files := map[string]string{"src/match.go": "package src\n// NeedleToken here\n", "src/deep/also.txt": "x NeedleToken y\n", "bin.dat": "NeedleToken\x00\n"}
	for i := range 30 {
		files[fmt.Sprintf("src/noise_%02d.txt", i)] = "plain text without the token\n"
	}
	return files
}

// TestIndexedAndLiveSearchesAgree runs every accelerated form over the same
// generation with and without the catalog: identical ordered bytes and
// statuses, while the indexed run reads (and pins) only files the index
// could not rule out.
func TestIndexedAndLiveSearchesAgree(t *testing.T) {
	h, catalog := indexedHarness(t, indexCorpus())
	for _, argv := range [][]string{
		{"grep", "-rn", "NeedleToken", "src"},
		{"grep", "-rl", "NeedleToken", "."},
		{"grep", "-rc", "NeedleToken", "src"},
		{"grep", "-rL", "NeedleToken", "src"},
		{"grep", "-rF", "-e", "NeedleToken", "-e", "absent", "src"},
		{"grep", "-rE", "Needle(Token|Thing)", "src"},
		{"grep", "-rq", "NeedleToken", "src"},
		{"grep", "-r", "NoSuchTokenAnywhere", "src"},
		{"rg", "NeedleToken"},
		{"rg", "-c", "NeedleToken"},
		{"rg", "-l", "NeedleToken", "src"},
	} {
		t.Run(fmt.Sprint(argv), func(t *testing.T) {
			h.owners.Catalog = nil
			live := h.exec("", argv...)
			livePins := h.pins.paths(live.execution)
			h.owners.Catalog = catalog
			indexed := h.exec("", argv...)
			indexedPins := h.pins.paths(indexed.execution)
			if live.err != nil || indexed.err != nil || live.stdout != indexed.stdout || live.stderr != indexed.stderr || live.result != indexed.result {
				t.Fatalf("live %q %q %+v %v\nindexed %q %q %+v %v", live.stdout, live.stderr, live.result, live.err, indexed.stdout, indexed.stderr, indexed.result, indexed.err)
			}
			if len(indexedPins) >= len(livePins) && argv[1] != "-rq" {
				t.Fatalf("index pruned nothing: live pins %d indexed %d", len(livePins), len(indexedPins))
			}
		})
	}
}

// TestIndexSpeaksOnlyForItsGeneration changes a pruned file after indexing:
// its filter no longer matches the file's metadata, so the search reads it.
// Forms the index cannot decide never prune.
func TestIndexSpeaksOnlyForItsGeneration(t *testing.T) {
	h, catalog := indexedHarness(t, indexCorpus())
	h.owners.Catalog = catalog
	changed := filepath.Join(h.root, "src", "noise_07.txt")
	testutil.FailErr(t, "rewrite", os.WriteFile(changed, []byte("now holds NeedleToken\n"), 0o644))
	later := fixtureTime.Add(time.Hour)
	testutil.FailErr(t, "touch", os.Chtimes(changed, later, later))
	h.exec("", "grep", "-rl", "NeedleToken", "src").expect(t, "stale filter", "src/deep/also.txt\nsrc/match.go\nsrc/noise_07.txt\n", "", 0)

	all := len(indexCorpus()) - 1 // bin.dat lies outside src
	for _, argv := range [][]string{
		{"grep", "-rv", "NeedleToken", "src"},
		{"grep", "-rP", "Needle(?=Token)", "src"},
		{"grep", "-rw", "NeedleToken", "src"},
	} {
		r := h.exec("", argv...)
		if got := len(h.pins.paths(r.execution)); got != all {
			t.Fatalf("%v read %d of %d files", argv, got, all)
		}
	}
	if !slices.Contains(h.pins.paths(h.exec("", "grep", "-rl", "NeedleToken", "src").execution), "src/noise_07.txt") {
		t.Fatal("changed file was not read")
	}
}
