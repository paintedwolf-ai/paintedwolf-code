package guidance_test

import (
	"github.com/lycaon/lycaon/internal/evidence"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDistinctEvidenceFacetValues_discoversNovelShape(t *testing.T) {
	records := []api.CitationGroundingEvidenceRecord{
		{Kind: "read", Shape: evidence.ShapeFileRegion},
		{Kind: "customfetch", Shape: evidence.ShapeOpaque},
		{Kind: "timeline", Shape: "custom_timeline"},
	}
	shapes := guidance.DistinctEvidenceFacetValues(records, "shape")
	if len(shapes) != 3 {
		t.Fatalf("shape facets = %v want 3 open values", shapes)
	}
	if shapes[0] != "custom_timeline" || shapes[1] != evidence.ShapeFileRegion || shapes[2] != evidence.ShapeOpaque {
		t.Fatalf("shape facets = %v want sorted open set incl. custom_timeline", shapes)
	}
	kinds := guidance.DistinctEvidenceFacetValues(records, "kind")
	if len(kinds) != 3 {
		t.Fatalf("kind facets = %v want 3", kinds)
	}
}

func TestBoardGroundingUsesShapeVerifiers(t *testing.T) {
	ev := evidence.Ledger{
		Handles: map[string]evidence.Record{
			"record_finding#1": {
				Handle:     "record_finding#1",
				Kind:       "record_finding",
				Shape:      evidence.ShapeFileRegion,
				Path:       "docs/board/page.md",
				Body:       []string{hostmarker.FormatNumberedLines([]string{"observed board excerpt"}, 12)},
				LineRanges: []evidence.LineRange{{Start: 12, End: 12}},
			},
		},
	}
	if !evidence.ExcerptMatchesHandle(ev, "record_finding#1", "docs/board/page.md", 12, "observed board excerpt") {
		t.Fatal("board grounding must verify via shape registry, not kind-specific logic")
	}
	if evidence.ExcerptMatchesHandle(ev, "record_finding#1", "docs/board/page.md", 12, "fabricated excerpt") {
		t.Fatal("fabricated excerpt must fail shape verification")
	}
}
