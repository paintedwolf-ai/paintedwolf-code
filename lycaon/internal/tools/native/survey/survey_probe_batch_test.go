package survey

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestProbeGrepBatchReadsEachCandidateOnce(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a.go":    "alpha\n",
		"b.go":    "beta\n",
		"both.go": "alpha beta\n",
	}
	for name, body := range files {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(root, name), []byte(body), 0o644))
	}
	records, stats, err := ProbeGrepBatchRecords(context.Background(), nativefixture.Boundary(t), sourcecatalog.New(), nativefixture.Context(root), ".", []SurveyGrepSpec{
		{Label: "alpha", Pattern: "alpha"},
		{Label: "beta", Pattern: "beta"},
	}, 20)
	testutil.FailErr(t, "batch grep", err)
	if len(records) != 2 || len(records[0].Records) != 2 || len(records[1].Records) != 2 {
		t.Fatalf("records = %#v", records)
	}
	if records[0].MatchCount != 2 || records[1].MatchCount != 2 {
		t.Fatalf("match counts = %#v", records)
	}
	if stats.FilesOpened != len(files) {
		t.Fatalf("files opened = %d want %d (one shared pass)", stats.FilesOpened, len(files))
	}
}

func TestProbeGrepBatchStructuralCapBoundsSampleNotCoverage(t *testing.T) {
	root := t.TempDir()
	for i := range 20 {
		name := filepath.Join(root, fmt.Sprintf("%03d.go", i))
		body := []byte("package sample\nimport \"fmt\"\nfunc f() { fmt.Println(1) }\n")
		if err := os.WriteFile(name, body, 0o644); err != nil {
			testutil.FailErr(t, "write fixture", err)
		}
	}
	results, _, err := ProbeGrepBatchRecords(context.Background(), nativefixture.Boundary(t), sourcecatalog.New(), nativefixture.Context(root), ".", []SurveyGrepSpec{
		{Label: "calls", Pattern: `fmt.Println($A)`},
	}, 5)
	testutil.FailErr(t, "structural survey", err)
	if len(results) != 1 || results[0].MatchCount != 20 || len(results[0].Records) != 5 {
		t.Fatalf("result = %#v, want exact count 20 and sample 5", results)
	}
}

func TestProbeGrepBatchCapBoundsSampleNotCoverage(t *testing.T) {
	root := t.TempDir()
	for i := range 100 {
		name := filepath.Join(root, fmt.Sprintf("%03d.txt", i))
		testutil.FailErr(t, "write fixture", os.WriteFile(name, []byte("needle\n"), 0o644))
	}
	results, stats, err := ProbeGrepBatchRecords(context.Background(), nativefixture.Boundary(t), sourcecatalog.New(), nativefixture.Context(root), ".", []SurveyGrepSpec{
		{Label: "needle", Pattern: "needle"},
	}, 10)
	testutil.FailErr(t, "batch grep", err)
	if len(results) != 1 || results[0].MatchCount != 100 || len(results[0].Records) != 10 {
		t.Fatalf("result = %#v, want exact count 100 and sample 10", results)
	}
	if stats.FilesOpened != 100 {
		t.Fatalf("files opened = %d want 100 full-scope files", stats.FilesOpened)
	}
}

func TestProbeFindCapBoundsSampleNotCoverage(t *testing.T) {
	root := t.TempDir()
	for i := range 100 {
		name := filepath.Join(root, fmt.Sprintf("%03d.go", i))
		testutil.FailErr(t, "write fixture", os.WriteFile(name, []byte("package sample\n"), 0o644))
	}
	records, total, err := ProbeFindRecords(context.Background(), nativefixture.Boundary(t), sourcecatalog.New(),
		nativefixture.Context(root), ".", "*.go", "go_files", 10)
	testutil.FailErr(t, "find probe", err)
	if total != 100 || len(records) != 10 {
		t.Fatalf("total=%d sample=%d, want exact count 100 and sample 10", total, len(records))
	}
}
