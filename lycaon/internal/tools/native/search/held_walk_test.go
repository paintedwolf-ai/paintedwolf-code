package search

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestFindReachedMetadataUsesOneHeldParent(t *testing.T) {
	logs := discoveryCapture(t)
	files := map[string]string{}
	for i := range 200 {
		files[fmt.Sprintf("dir/file-%03d", i)] = "data"
	}
	h := newHarness(t, files)
	h.owners.Catalog = discoveryCatalog(t)
	for attempt := range 2 {
		result := h.exec("", "find", ".", "-name", "file-*", "-size", "4c")
		if result.err != nil || result.stderr != "" || strings.Count(result.stdout, "\n") != 200 {
			t.Fatalf("attempt=%d result=%+v", attempt, result)
		}
		work := logs.records[result.execution]
		if work["directory_handles"] != 2 || work["relative_descriptor_opens"] != 2 || work["held_directory_peak"] != 2 || work["held_directory_remaining"] != 0 {
			t.Fatalf("attempt=%d work=%v", attempt, work)
		}
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
