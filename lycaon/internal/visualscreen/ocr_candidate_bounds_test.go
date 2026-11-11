package visualscreen

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTesseractAlternativeWorkStopsAtCandidateBudget(t *testing.T) {
	var line tsvLine
	for word := range 256 {
		id := fmt.Sprint(word)
		line.addWord(id, fmt.Sprintf("word%04d", word), 90)
		for alternative := range 64 {
			line.addWord(id, fmt.Sprintf("alt%05d", alternative), 80)
		}
	}
	for slot, alternatives := range line.wordAlts {
		if len(alternatives) > maxOCRCandidates-1 {
			t.Fatalf("word %d retained %d alternatives", slot, len(alternatives))
		}
	}
	original := strings.Join(line.words, " ")
	var candidates []string
	var err error
	allocations := testing.AllocsPerRun(1, func() { candidates, err = line.candidates() })
	testutil.FailErr(t, "produce admitted recognition alternatives", err)
	if allocations > 128 {
		t.Fatalf("candidate cap failed to bound materialization: %g allocations", allocations)
	}
	if len(candidates) != maxOCRCandidates-1 {
		t.Fatalf("candidate count=%d", len(candidates))
	}
	if strings.Join(line.words, " ") != original {
		t.Fatal("repair changed primary recognition")
	}
	seen := map[string]bool{original: true}
	for _, candidate := range candidates {
		if seen[candidate] || len(candidate) > len(original) {
			t.Fatal("candidate duplicated recognition or exceeded its retained span bound")
		}
		seen[candidate] = true
	}
}
