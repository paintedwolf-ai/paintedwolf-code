package search

import (
	"testing"
)

func TestProjectArtifactShape(t *testing.T) {
	capture := ProjectArtifactInput{
		ID:             "art_capture",
		Hash:           "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		Mime:           "image/png",
		Source:         "capture",
		Caption:        "Home page",
		EvidenceHandle: "page#3",
		SessionID:      "sess_1",
		WorkflowRunID:  "run_1",
		ToolCallID:     "call_1",
		CreatedAt:      "2026-07-14T15:05:10Z",
	}
	out := ProjectArtifact("proj-1", capture)
	if len(out) != 1 {
		t.Fatalf("rows = %d", len(out))
	}
	got := out[0]
	if got.HitKind != HitKindArtifact || got.Source != SourceArtifact {
		t.Fatalf("got %+v", got)
	}
	if got.Handle != capture.Hash {
		t.Fatalf("handle should be content hash, got %q", got.Handle)
	}
	if got.SourceRef != capture.ID {
		t.Fatalf("source_ref = %q want artifact id", got.SourceRef)
	}
	if got.CheckID != capture.EvidenceHandle {
		t.Fatalf("check_id = %q", got.CheckID)
	}
	if got.Kind != capture.Source || got.Snippet != capture.Caption {
		t.Fatalf("kind/snippet = %q/%q", got.Kind, got.Snippet)
	}
	for _, v := range []string{got.Handle, got.Path, got.SourceRef} {
		if len(v) > 0 && (v[0] == '/' || v[0] == '~') {
			t.Fatalf("path leak: %q", v)
		}
	}
}
