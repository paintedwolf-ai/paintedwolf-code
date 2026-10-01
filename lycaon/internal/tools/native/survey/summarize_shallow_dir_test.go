package survey

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSummarizeDirGatherStaysShallowUnderDenseSubtree(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/main.go", "package pkg\nfunc Main() {}\n")
	dense := filepath.Join(dir, "pkg", "dense")
	testutil.FailErr(t, "mkdir dense", os.MkdirAll(dense, 0o755))
	for i := 0; i < 4000; i++ {
		name := filepath.Join(dense, fmt.Sprintf("f%04d.dat", i))
		testutil.FailErr(t, "write dense", os.WriteFile(name, []byte("x"), 0o644))
	}

	g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
	res, err := g.Gather(context.Background(), summarize.Request{Path: "pkg", Task: "orient"})
	testutil.FailErr(t, "gather", err)
	if res.Subtree == nil {
		t.Fatal("expected subtree")
	}
	foundDense := false
	for _, c := range res.Subtree.Children {
		if c == nil {
			continue
		}
		if strings.Contains(c.Path, "/dense/") {
			t.Fatalf("shallow gather leaked dense leaf %q", c.Path)
		}
		if c.Path == "pkg/dense" {
			foundDense = true
			if c.Kind != summarize.SubtreeKindDir {
				t.Fatalf("dense child kind = %q, want dir", c.Kind)
			}
		}
	}
	if !foundDense {
		t.Fatalf("subtree children = %+v, want pkg/dense as immediate child", res.Subtree.Children)
	}
}
