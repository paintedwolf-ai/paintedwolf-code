package secretmatch

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOCRDoubletRepairsFollowEveryCatalogClass(t *testing.T) {
	catalog, err := bundledOCRConfusions()
	testutil.FailErr(t, "load OCR repair catalog", err)
	for _, class := range catalog.classes {
		for _, first := range class {
			for _, second := range class {
				if first == second {
					continue
				}
				original := "[" + string(first) + string(second) + "]"
				candidates, err := OCRDoubletCandidates(original, 1)
				testutil.FailErr(t, "repair cataloged glyph pair", err)
				if len(candidates) != 1 || candidates[0] != "["+string(catalog.canonical[first])+"]" || utf8.RuneCountInString(candidates[0]) != 3 {
					t.Fatalf("catalog pair %q repaired as %q", original, candidates)
				}
			}
		}
	}
	custom, err := parseOCRConfusions([]byte("version: 1\nclasses:\n  - glyphs: ['3', '#']\n    reason: fixture optical confusion\n"))
	testutil.FailErr(t, "load distinct fixture repair policy", err)
	if got := custom.doubletCandidates("[#3]", 1); len(got) != 1 || got[0] != "[3]" {
		t.Fatalf("repair ignored catalog: %q", got)
	}
	if got, changed := normalizeOCRBoundaries(custom, "#abc"); !changed || got != "3abc" {
		t.Fatalf("boundary repair ignored catalog: %q, changed=%v", got, changed)
	}
	if got, changed := normalizeOCRBoundaries(ocrConfusionCatalog{}, "/abc |abc !abc"); changed || got != "/abc |abc !abc" {
		t.Fatalf("uncataloged punctuation changed: %q", got)
	}
}

func TestOCRRepairOutputAndAllocationWorkStayBounded(t *testing.T) {
	input := strings.Repeat("0O ", 4096)
	for _, limit := range []int{-1, 0, 1, 4, 1000000} {
		candidates, err := OCRDoubletCandidates(input, limit)
		testutil.FailErr(t, "produce bounded OCR candidates", err)
		if len(candidates) > max(0, min(limit, maxOCRRepairCandidates)) {
			t.Fatalf("candidate count=%d exceeds limit=%d", len(candidates), limit)
		}
		seen := map[string]bool{input: true}
		for _, candidate := range candidates {
			if seen[candidate] || len(candidate) > len(input) {
				t.Fatal("repair duplicated recognition or expanded retained evidence")
			}
			seen[candidate] = true
		}
	}
	var err error
	allocations := testing.AllocsPerRun(1, func() { _, err = OCRDoubletCandidates(input, 1000000) })
	testutil.FailErr(t, "measure bounded OCR repair work", err)
	if allocations > 128 {
		t.Fatalf("repair materialized work beyond candidate admission: %g allocations", allocations)
	}
}
