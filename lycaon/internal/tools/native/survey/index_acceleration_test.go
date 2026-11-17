package survey

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

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

	warmSurveyContent(t, catalog, dir, snapshot.Entries)

	tool := testSummarizeTool(t, dir)
	tool.Catalog = catalog
	raw, err := tool.Run(context.Background(), map[string]any{"path": ".", "pattern": "UniqueBloomTargetToken"}, nativefixture.Context(dir))
	testutil.FailErr(t, "run summarize pattern", err)
	res := decodeSummarizeResponse(t, raw)
	if res.Coverage.MatchesObserved != 1 {
		t.Fatalf("matches=%d want 1", res.Coverage.MatchesObserved)
	}
}

func warmSurveyContent(t *testing.T, catalog *sourcecatalog.Catalog, root string, entries []sourcecatalog.Entry) bool {
	t.Helper()
	observable := true
	for _, entry := range entries {
		if entry.IsDir || entry.IsSymlink {
			continue
		}
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(entry.Path)))
		testutil.FailErr(t, "open fixture content", err)
		info, err := file.Stat()
		testutil.FailErr(t, "stat fixture content", err)
		version := fspath.ObserveFile(info)
		observable = observable && version.Known && version.Links > 0
		observation := catalog.Literals.BeginRead(root, entry, version)
		_, err = io.Copy(io.Discard, observation.Wrap(t.Context(), file))
		testutil.FailErr(t, "observe fixture stream", err)
		testutil.FailErr(t, "close fixture stream", file.Close())
		observation.Commit()
		observation.Close()
	}
	return observable
}
