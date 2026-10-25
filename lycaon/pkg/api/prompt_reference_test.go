package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPromptReferencePartStrictDiscriminatedUnion(t *testing.T) {
	var ref PromptReferencePart
	if err := json.Unmarshal([]byte(`{"kind":"path-file","project_id":"p","path":"src/main.go"}`), &ref); err != nil {
		t.Fatalf("unmarshal path-file: %v", err)
	}
	if ref.PathFile == nil || ref.PathFolder != nil || ref.Artifact != nil || ref.SearchHit != nil {
		t.Fatalf("decoded union = %#v", ref)
	}
	encoded, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("marshal path-file: %v", err)
	}
	if string(encoded) != `{"kind":"path-file","project_id":"p","path":"src/main.go"}` {
		t.Fatalf("marshal = %s", encoded)
	}
}

func TestPromptReferencePartRejectsCrossVariantFields(t *testing.T) {
	var ref PromptReferencePart
	err := json.Unmarshal([]byte(`{"kind":"path-file","project_id":"p","path":"src/main.go","artifact_id":"a"}`), &ref)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("cross-variant field error = %v", err)
	}
}

func TestPromptReferencePartExternalFile(t *testing.T) {
	var ref PromptReferencePart
	if err := json.Unmarshal([]byte(`{"kind":"external-file","project_id":"p","path":"/Users/test/notes.txt"}`), &ref); err != nil {
		t.Fatalf("unmarshal external-file: %v", err)
	}
	if ref.ExternalFile == nil || ref.PathFile != nil || ref.PathFolder != nil || ref.ExternalFolder != nil {
		t.Fatalf("decoded union = %#v", ref)
	}
	if ref.ProjectID() != "p" {
		t.Fatalf("ProjectID = %q want p", ref.ProjectID())
	}
	encoded, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("marshal external-file: %v", err)
	}
	if string(encoded) != `{"kind":"external-file","project_id":"p","path":"/Users/test/notes.txt"}` {
		t.Fatalf("marshal = %s", encoded)
	}
}

func TestPromptReferencePartExternalFolder(t *testing.T) {
	var ref PromptReferencePart
	if err := json.Unmarshal([]byte(`{"kind":"external-folder","project_id":"p","path":"/Users/test/docs"}`), &ref); err != nil {
		t.Fatalf("unmarshal external-folder: %v", err)
	}
	if ref.ExternalFolder == nil || ref.PathFile != nil || ref.ExternalFile != nil {
		t.Fatalf("decoded union = %#v", ref)
	}
	encoded, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("marshal external-folder: %v", err)
	}
	if string(encoded) != `{"kind":"external-folder","project_id":"p","path":"/Users/test/docs"}` {
		t.Fatalf("marshal = %s", encoded)
	}
}
