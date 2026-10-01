package contract

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestEvidenceExplorerOpenShapeFacet(t *testing.T) {
	t.Parallel()
	ev := evidence.Ledger{
		Handles: map[string]evidence.Record{
			"read#1": {
				Handle: "read#1",
				Kind:   "read",
				Shape:  evidence.ShapeFileRegion,
				Path:   "src/main.go",
			},
			"novel#1": {
				Handle: "novel#1",
				Kind:   "novel",
				Shape:  "custom_timeline",
				Body:   []string{"event stream payload"},
			},
		},
	}
	var grounding api.CitationGrounding
	guidance.StampEvidenceRecords(&grounding, ev)
	wire := grounding.EvidenceRecords
	facets := guidance.DistinctEvidenceFacetValues(wire, "shape")
	if len(facets) != 2 {
		t.Fatalf("shape facets = %v want file_region + custom_timeline", facets)
	}
	foundNovel := false
	for _, shape := range facets {
		if shape == "custom_timeline" {
			foundNovel = true
		}
	}
	if !foundNovel {
		t.Fatal("novel shape must surface as an open facet without Explorer-specific code")
	}
}

func TestEvidenceExplorerOpenKindFacet(t *testing.T) {
	t.Parallel()
	records := []api.CitationGroundingEvidenceRecord{
		{Kind: "read", Shape: evidence.ShapeFileRegion},
		{Kind: "mcp_custom_tool", Shape: evidence.ShapeOpaque},
	}
	kinds := guidance.DistinctEvidenceFacetValues(records, "kind")
	if len(kinds) != 2 {
		t.Fatalf("kind facets = %v want open discovery", kinds)
	}
}

func TestBoardPageGroundingShapeVerifierParity(t *testing.T) {
	t.Parallel()
	ev := evidence.Ledger{
		Handles: map[string]evidence.Record{
			"board_page#1": {
				Handle:     "board_page#1",
				Kind:       "board_page",
				Shape:      evidence.ShapeFileRegion,
				Path:       "docs/notes.md",
				Body:       []string{"7\tverbatim finding from read tool"},
				LineRanges: []evidence.LineRange{{Start: 7, End: 7}},
			},
		},
	}
	ok, reason := evidence.VerifyHandle(ev, "board_page#1", evidence.Claim{
		Handle:  "board_page#1",
		Line:    7,
		Excerpt: "verbatim finding",
	})
	if !ok {
		t.Fatalf("board write grounding must use shape verifiers: %q", reason)
	}
	ok, _ = evidence.VerifyHandle(ev, "board_page#1", evidence.Claim{
		Handle:  "board_page#1",
		Line:    7,
		Excerpt: "invented text",
	})
	if ok {
		t.Fatal("board grounding must reject fabricated excerpts via shape registry")
	}
}

func TestTrustTierForUnknownShapeDefaultsOpaque(t *testing.T) {
	t.Parallel()
	if evidence.FidelityForShape("custom_timeline") != evidence.FidelityOpaque {
		t.Fatalf("unknown shape trust tier = %q want opaque for reviewer visibility",
			evidence.FidelityForShape("custom_timeline"))
	}
}

// A shape that falls through to the default is indistinguishable from a novel
// MCP shape, so every locked shape must be classified explicitly.
func TestTrustTierClassifiesEveryLockedShape(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		evidence.ShapeFileRegion:      evidence.FidelityStructured,
		evidence.ShapeArtifact:        evidence.FidelityStructured,
		evidence.ShapeVisual:          evidence.FidelityStructured,
		evidence.ShapePageGeometry:    evidence.FidelityStructured,
		evidence.ShapeStructuredEvent: evidence.FidelityStructured,
		evidence.ShapeURL:             evidence.FidelityScraped,
		evidence.ShapeSurfaceSnapshot: evidence.FidelityScraped,
		evidence.ShapeCommand:         evidence.FidelityOpaque,
		evidence.ShapeOpaque:          evidence.FidelityOpaque,
	}
	for _, shape := range evidence.ShapeIDs() {
		expected, ok := want[shape]
		if !ok {
			t.Fatalf("shape %q has no declared trust tier — classify it in evidence.FidelityForShape", shape)
		}
		if got := evidence.FidelityForShape(shape); got != expected {
			t.Fatalf("fidelity for %q = %q want %q", shape, got, expected)
		}
	}
}

// A second mapping beside this one drifts silently, since nothing renders it.
func TestFidelityHasOneImplementation(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var offenders []string
	err := filepath.WalkDir(filepath.Join(root, "lycaon"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "lycaon/internal/evidence/fidelity.go" {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(body), "func FidelityForShape(") {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		contractcheck.FailErr(t, "walk repo", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("FidelityForShape must live only in internal/evidence; found in %v", offenders)
	}
}
