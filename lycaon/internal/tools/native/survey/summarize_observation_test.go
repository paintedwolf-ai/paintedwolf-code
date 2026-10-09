package survey

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
)

func TestSummarizeNoMaterialRetainsScopeAndOmissions(t *testing.T) {
	dir := t.TempDir()
	roots := []string{"empty.txt", "binary.dat"}
	writeFile(t, dir, roots[0], "ordinary text\n")
	writeFile(t, dir, roots[1], "matching\x00binary\n")
	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	_, err := g.Gather(t.Context(), summarize.Request{Paths: roots, Pattern: "matching"})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SUMMARIZE_NO_MATERIAL" {
		t.Fatalf("gather: %v", err)
	}
	if !reflect.DeepEqual(reject.Data["paths"], roots) || reject.Data["path"] != "" {
		t.Fatalf("multi-root scope collapsed: %+v", reject.Data)
	}
	if reject.Data["summary_skipped_count"] != 1 || !reflect.DeepEqual(reject.Data["summary_skipped_paths"], []string{"binary.dat"}) {
		t.Fatalf("uninspected material lost: %+v", reject.Data)
	}
}

func TestSummarizeOmissionObservationIsBoundedAndCoverageIsIncomplete(t *testing.T) {
	g := testSummarizeGatherer(t, t.TempDir(), summarize.DefaultCaps())
	for i := 14; i >= 0; i-- {
		g.noteSkippedPath(fmt.Sprintf("path-%02d", i))
	}
	g.noteSkippedPath("path-00")
	g.sourceLimited = true
	data := g.noMaterialData(summarize.Request{Path: "scope"})
	paths := data["summary_skipped_paths"].([]string)
	if data["summary_skipped_count"] != 15 || len(paths) != 10 || paths[0] != "path-00" || data["summary_source_limited"] != true {
		t.Fatalf("unbounded or inaccurate omission: %+v", data)
	}
	result := summarize.Result{Coverage: summarize.Coverage{Complete: true}}
	g.stampWork(&result)
	if result.Coverage.Complete || len(result.Pack.Gaps) == 0 {
		t.Fatalf("omissions claimed complete coverage: %+v", result)
	}
}

type failingSummaryReader struct{ err error }

func (r failingSummaryReader) Read([]byte) (int, error) { return 0, r.err }

func TestSummarizePatternReadFailureIsNotNoMatch(t *testing.T) {
	cause := io.ErrUnexpectedEOF
	line := newSummarizePatternLine(bufio.NewReader(failingSummaryReader{err: cause}))
	line.drain()
	if !errors.Is(line.readErr, cause) || line.invalid {
		t.Fatalf("read failure became non-text or no match: %+v", line)
	}
}
