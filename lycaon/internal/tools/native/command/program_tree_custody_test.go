package command

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
)

// A tree a network read filled earlier in the program holds custody for the
// files a later operation reads from it, until the program replaces one.
func TestProgramTreeCustodyReachesLaterReads(t *testing.T) {
	root := t.TempDir()
	run := &programRun{tc: tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: root, IsPrimary: true}}}}}
	const origin = "https://git.example.test/org/repo.git"
	clone := run.beginOpTracker()
	clone.addExchange(evidence.ExchangeProvenance{External: true, ObservedURLs: []string{origin}, WrittenTrees: []string{filepath.Join(root, "repo")}})
	run.finishOpTracker(t.Context(), clone, outputStreamClass{}, false, outputStreamClass{}, false)

	inside := run.normalizeProvenancePath("", filepath.Join(root, "repo", "src", "main.go"))
	if urls := run.lookupCustodyURLs(t.Context(), []string{inside}); !slices.Equal(urls, []string{origin}) {
		t.Fatalf("file under the cloned tree: custody %v", urls)
	}
	outside := run.normalizeProvenancePath("", filepath.Join(root, "other.go"))
	if urls := run.lookupCustodyURLs(t.Context(), []string{outside}); len(urls) != 0 {
		t.Fatalf("file outside the tree held custody %v", urls)
	}
	run.clearCustodyFile(inside)
	if urls := run.lookupCustodyURLs(t.Context(), []string{inside}); len(urls) != 0 {
		t.Fatalf("replaced file kept the tree's custody %v", urls)
	}
}
