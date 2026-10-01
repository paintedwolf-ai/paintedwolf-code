package sourcecomparison

import (
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/aymanbagabas/go-udiff"
	"github.com/aymanbagabas/go-udiff/lcs"
	"github.com/lycaon/lycaon/pkg/api"
)

func changeAreas(comparison udiff.UnifiedDiff) []api.SourceReaderChangeArea {
	areas := make([]api.SourceReaderChangeArea, 0, min(6, len(comparison.Hunks)))
	for _, hunk := range comparison.Hunks[:min(6, len(comparison.Hunks))] {
		area := api.SourceReaderChangeArea{AfterLine: hunk.ToLine}
		for _, line := range hunk.Lines {
			switch line.Kind {
			case udiff.Insert:
				area.Added++
			case udiff.Delete:
				area.Removed++
			default:
			}
		}
		areas = append(areas, area)
	}
	return areas
}

func lineComparison(before, after string) (udiff.UnifiedDiff, error) {
	if before == after {
		return udiff.UnifiedDiff{}, nil
	}
	// Each nonempty line consumes at least one byte and at most one token ID.
	if len(before) > math.MaxInt32 || len(after) > math.MaxInt32-len(before) {
		return udiff.UnifiedDiff{}, fmt.Errorf("compare source lines: content exceeds token capacity")
	}
	ids := make(map[string]rune)
	var lastID rune
	encode := func(text string) ([]rune, []int) {
		parts := lines(text)
		tokens, offsets := make([]rune, len(parts)), make([]int, len(parts)+1)
		for index, part := range parts {
			id, found := ids[part]
			if !found {
				lastID++
				id = lastID
				ids[part] = id
			}
			tokens[index] = id
			offsets[index+1] = offsets[index] + len(part)
		}
		return tokens, offsets
	}
	a, oldOffsets := encode(before)
	b, newOffsets := encode(after)
	changes := lcs.DiffRunes(a, b)
	edits := make([]udiff.Edit, len(changes))
	for index, change := range changes {
		edits[index] = udiff.Edit{Start: oldOffsets[change.Start], End: oldOffsets[change.End], New: after[newOffsets[change.ReplStart]:newOffsets[change.ReplEnd]]}
	}
	return udiff.ToUnifiedDiff("", "", before, edits, 0)
}

// Counts prepares line changes without syntax, inline spans, or presentation rows.
func Counts(before, after string) (added, removed int, err error) {
	comparison, err := lineComparison(normalize(before), normalize(after))
	if err != nil {
		return 0, 0, err
	}
	added, removed = lineCounts(comparison)
	return added, removed, nil
}

func lineCounts(comparison udiff.UnifiedDiff) (added, removed int) {
	for _, hunk := range comparison.Hunks {
		for _, line := range hunk.Lines {
			switch line.Kind {
			case udiff.Insert:
				added++
			case udiff.Delete:
				removed++
			default:
			}
		}
	}
	return
}

func fragmentLength(text string) int {
	length := min(len(text), 4096)
	for length < len(text) && !utf8.RuneStart(text[length]) {
		length--
	}
	return length
}

func fragmentCount(text string) int {
	count := 1
	for length := fragmentLength(text); length < len(text); length = fragmentLength(text) {
		count++
		text = text[length:]
	}
	return count
}
