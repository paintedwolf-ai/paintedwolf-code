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
