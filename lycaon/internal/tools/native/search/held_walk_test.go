package search

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFindMetadataOpenWorkDoesNotGrowWithSiblingCount(t *testing.T) {
	opensByAttempt := map[int]int64{}
	for _, siblings := range []int{1, 200, 1000} {
		t.Run(fmt.Sprint(siblings), func(t *testing.T) {
			logs := discoveryCapture(t)
			files := map[string]string{}
			for i := range siblings {
				files[fmt.Sprintf("dir/file-%03d", i)] = "data"
			}
			h := newHarness(t, files)
			h.owners.Catalog = discoveryCatalog(t)
			for attempt := range 2 {
				result := h.exec("", "find", ".", "-name", "file-*", "-size", "4c")
				if result.err != nil || result.stderr != "" || strings.Count(result.stdout, "\n") != siblings {
					t.Fatalf("attempt=%d result=%+v", attempt, result)
				}
				work := logs.records[result.execution]
				if siblings == 1 {
					opensByAttempt[attempt] = work["relative_descriptor_opens"]
				}
				if work["directory_handles"] != 2 || work["relative_descriptor_opens"] != opensByAttempt[attempt] || work["held_directory_peak"] != 2 || work["held_directory_remaining"] != 0 || work["entry_metadata_reads"] != int64(siblings+2) {
					t.Fatalf("attempt=%d work=%v", attempt, work)
				}
			}
		})
	}
}

func TestFindHeldAncestorsHaveBoundedPeakAndReleaseOnReturn(t *testing.T) {
	logs := discoveryCapture(t)
	path := strings.Repeat("d/", heldTraversalDirectories+8) + "leaf"
	h := newHarness(t, map[string]string{path: "data"})
	h.owners.Catalog = discoveryCatalog(t)
	for attempt := range 2 {
		result := h.exec("", "find", ".", "-name", "leaf", "-type", "f")
		result.expect(t, "bounded deep traversal", "./"+path+"\n", "", 0)
		work := logs.records[result.execution]
		if work["held_directory_peak"] != heldTraversalDirectories+1 || work["held_directory_remaining"] != 0 {
			t.Fatalf("attempt=%d work=%v", attempt, work)
		}
	}
}

type closedAfterLine struct {
	output string
	closed bool
}

func (w *closedAfterLine) Write(p []byte) (int, error) {
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	w.output += string(p)
	if len(p) > 0 {
		w.closed = true
	}
	return len(p), nil
}

func TestFindConsumerCloseStopsSiblingMetadataAndReleasesParents(t *testing.T) {
	logs := discoveryCapture(t)
	files := map[string]string{}
	for i := range 200 {
		files[fmt.Sprintf("dir/file-%03d", i)] = "data"
	}
	h := newHarness(t, files)
	h.owners.Catalog = discoveryCatalog(t)
	request := requestFor(h, "find", ".", "-name", "file-*")
	output := &closedAfterLine{}
	streams := streamsFor()
	streams.Output = output
	_, err := h.owners.Invoke(t.Context(), h.tc, mustPaths(t, h), h.dispatch, request, streams)
	if !errors.Is(err, io.ErrClosedPipe) || output.output != "./dir/file-000\n" {
		t.Fatalf("output=%q err=%v", output.output, err)
	}
	work := logs.records[request.Invocation.Identity.ExecutionID]
	if work["entry_metadata_reads"] != 3 || work["directory_handles"] != 2 || work["held_directory_remaining"] != 0 {
		t.Fatalf("closed consumer work=%v", work)
	}
}

func TestFindExecCannotRetainAuthorityAfterAncestorLeavesRoot(t *testing.T) {
	logs := discoveryCapture(t)
	h := newHarness(t, map[string]string{"tree/a.tmp": "data", "tree/b.tmp": "data"})
	h.owners.Catalog = discoveryCatalog(t)
	moved := filepath.Join(t.TempDir(), "moved")
	h.dispatch.status = func([]string) int {
		testutil.FailErr(t, "move ancestor during child execution", os.Rename(filepath.Join(h.root, "tree"), moved))
		return 0
	}
	result := h.exec("", "find", "tree", "-name", "*.tmp", "-exec", "echo", "{}", ";")
	if result.err != nil {
		result.refused(t, "held ancestor left attached root", "SURVEY_PATH_ESCAPE")
	} else {
		result.expect(t, "fresh path no longer exists", "argv: echo|tree/a.tmp\n", "", 0)
	}
	if result.stdout != "argv: echo|tree/a.tmp\n" || len(h.dispatch.requests) != 1 {
		t.Fatalf("later child executed: %+v requests=%d", result, len(h.dispatch.requests))
	}
	work := logs.records[result.execution]
	if work["held_directory_remaining"] != 0 {
		t.Fatalf("refusal retained parents: %v", work)
	}
}

func TestFindDeleteReleasesHeldDirectoriesAfterDepthFirstActions(t *testing.T) {
	logs := discoveryCapture(t)
	h := newHarness(t, map[string]string{"tree/a": "data", "tree/sub/b": "data"})
	h.owners.Catalog = discoveryCatalog(t)
	result := h.exec("", "find", "tree", "-delete")
	result.expect(t, "depth first held deletion", "", "", 0)
	if fmt.Sprint(h.removals.removed) != "[tree/a tree/sub/b tree/sub tree]" {
		t.Fatalf("deletion order=%v", h.removals.removed)
	}
	work := logs.records[result.execution]
	if work["held_directory_remaining"] != 0 || work["held_directory_peak"] != 2 {
		t.Fatalf("delete lifecycle=%v", work)
	}
}
