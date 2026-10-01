package summarize

import (
	"testing"
)

func TestCitedFileOmission(t *testing.T) {
	body := "See [routes](lycaon/internal/api/server.go) and `docs/naming.md`."
	cands := []Candidate{{
		RelPath: "README.md", Kind: KindFile, Body: body,
	}}
	omitted := citedFileOmission(cands)
	if len(omitted) != 2 {
		t.Fatalf("omitted = %v, want 2 paths", omitted)
	}
	if omitted[0] != "lycaon/internal/api/server.go" || omitted[1] != "docs/naming.md" {
		t.Fatalf("omitted = %v, unexpected paths", omitted)
	}

	// When cited files are in the gather set, no omission.
	cands = append(cands,
		Candidate{RelPath: "lycaon/internal/api/server.go", Kind: KindFile, Body: "package api"},
		Candidate{RelPath: "docs/naming.md", Kind: KindFile, Body: "# naming"},
	)
	if got := citedFileOmission(cands); len(got) != 0 {
		t.Fatalf("expected no omission when refs gathered, got %v", got)
	}
}
