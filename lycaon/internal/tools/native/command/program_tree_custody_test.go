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

// An owner that reads a store a network read filled, a Git repository's
// object store after a quiet fetch, delivers that custody to the streams it
// writes, and the files it fills from the store inherit it.
func TestContentFlowCarriesTheStoresCustody(t *testing.T) {
	root := t.TempDir()
	run := &programRun{tc: tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: root, IsPrimary: true}}}}}
	const origin = "https://git.example.test/org/repo.git"
	objects := filepath.Join(root, "repo", ".git")
	fetch := run.beginOpTracker()
	fetch.addExchange(evidence.ExchangeProvenance{External: true, ObservedURLs: []string{origin}, WrittenTrees: []string{objects}})
	run.finishOpTracker(t.Context(), fetch, outputStreamClass{}, false, outputStreamClass{}, false)

	show := run.beginOpTracker()
	run.observeContentFlow(t.Context(), show, evidence.ContentFlow{Source: objects})
	if urls := run.lookupCustodyURLs(t.Context(), show.readPaths); !slices.Equal(urls, []string{origin}) {
		t.Fatalf("a read of the fetched store carries custody %v", urls)
	}
	run.finishOpTracker(t.Context(), show, outputStreamClass{}, false, outputStreamClass{}, false)

	merged := filepath.Join(root, "repo", "src", "main.go")
	merge := run.beginOpTracker()
	run.observeContentFlow(t.Context(), merge, evidence.ContentFlow{Source: objects, Files: []string{merged}})
	run.finishOpTracker(t.Context(), merge, outputStreamClass{}, false, outputStreamClass{}, false)
	if urls := run.lookupCustodyURLs(t.Context(), []string{run.normalizeProvenancePath("", merged)}); !slices.Equal(urls, []string{origin}) {
		t.Fatalf("a file merged from the fetched store: custody %v", urls)
	}

	local := run.beginOpTracker()
	run.observeContentFlow(t.Context(), local, evidence.ContentFlow{Source: filepath.Join(root, "own", ".git"), Files: []string{filepath.Join(root, "own", "a.go")}})
	run.finishOpTracker(t.Context(), local, outputStreamClass{}, false, outputStreamClass{}, false)
	if urls := run.lookupCustodyURLs(t.Context(), []string{run.normalizeProvenancePath("", filepath.Join(root, "own", "a.go"))}); len(urls) != 0 {
		t.Fatalf("a store no network read filled gave custody %v", urls)
	}
}
