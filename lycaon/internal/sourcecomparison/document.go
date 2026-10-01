// Package sourcecomparison prepares immutable, bounded source presentations.
package sourcecomparison

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/lycaon/lycaon/internal/pagedview"
	"sort"
	"strings"
	"sync"

	"github.com/aymanbagabas/go-udiff"
	"github.com/lycaon/lycaon/pkg/api"
)

const ContextLines = 3

// Document shares text and syntax; secret annotations belong to projections.
type Document struct {
	Summary               api.SourceComparisonSummary
	Before, After         api.SourceComparisonSide
	Attribution           *api.SourceComparisonAttribution
	beforeText, afterText string
	rows                  []planRow
	groups                []changeGroup
	beforeRows, afterRows []int
	inline                inlineCache
	preparation           sync.Once
	unified               udiff.UnifiedDiff
	comparable            bool
	decorationMu          sync.Mutex
	decorated             bool
	decorationWork        pagedview.Preparation[bool, decorationPlans]
	beforePlan, afterPlan decoratedSide
}

func Hash(text string) string { sum := sha256.Sum256([]byte(text)); return hex.EncodeToString(sum[:]) }
func normalize(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}
func lines(s string) []string {
	if s == "" {
		return nil
	}
	out := strings.SplitAfter(s, "\n")
	if out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}
func width(s string) int {
	units := 0
	for _, r := range s {
		units++
		if r > 0xffff {
			units++
		}
	}
	return units
}

func New(before, after api.SourceComparisonSide, attribution *api.SourceComparisonAttribution) (*Document, error) {
	before.SecretScreen, after.SecretScreen = nil, nil
	a, b := normalize(before.Content), normalize(after.Content)
	unified, err := lineComparison(a, b)
	if err != nil {
		return nil, err
	}
	d := &Document{Before: before, After: after, beforeText: a, afterText: b, unified: unified, Attribution: attribution, comparable: comparisonSideKnown(before) && comparisonSideKnown(after)}
	d.Summary = api.SourceComparisonSummary{Before: side(before, a), After: side(after, b)}
	d.Summary.ChangeAreas = []api.SourceReaderChangeArea{}
	if d.comparable {
		d.Summary.Added, d.Summary.Removed = lineCounts(unified)
		d.Summary.ChangeAreas = changeAreas(unified)
		d.Summary.ChangeAreaCount = len(unified.Hunks)
	}
	for _, line := range lines(a) {
		d.Summary.Rows += fragmentCount(line)
	}
	for _, hunk := range unified.Hunks {
		for _, line := range hunk.Lines {
			if line.Kind == udiff.Insert {
				d.Summary.Rows += fragmentCount(line.Content)
			}
		}
	}
	return d, nil
}

// prepare builds coordinates without materializing per-row presentation details.
func (d *Document) prepare() { d.preparation.Do(d.prepareRows) }

func comparisonSideKnown(side api.SourceComparisonSide) bool {
	return side.Availability == "" || side.Availability == "available" || side.Availability == "absent"
}

func side(s api.SourceComparisonSide, text string) api.SourceReaderSide {
	sha := s.Sha256
	if sha == "" {
		sha = Hash(s.Content)
	}
	return api.SourceReaderSide{Path: s.Path, SHA256: sha, Lines: len(lines(text)), Availability: s.Availability}
}

func screenRange(screen *api.SecretScreen, start, length int) *api.SecretScreen {
	if screen == nil {
		return nil
	}
	out := *screen
	out.Spans = nil
	for _, span := range screen.Spans {
		if span.End <= start || span.Start >= start+length {
			continue
		}
		span.Start = max(0, span.Start-start)
		span.End = min(length, span.End-start)
		out.Spans = append(out.Spans, span)
	}
	return &out
}
func authors(ranges []api.SourceAttributedText, start, length int) []api.SourceContributor {
	var out []api.SourceContributor
	seen := make(map[api.SourceContributor]bool)
	for _, r := range ranges {
		if !r.Visible || int(r.Index) >= start+length || int(r.Index+r.Length) <= start {
			continue
		}
		for _, author := range r.Contributors {
			if !seen[author] {
				out = append(out, author)
				seen[author] = true
			}
		}
	}
	return out
}

type screenOffset struct {
	position, index int
	end             bool
}

func normalizedScreen(screen *api.SecretScreen, text string) *api.SecretScreen {
	if screen == nil || !strings.Contains(text, "\r\n") {
		return screen
	}
	out := *screen
	out.Spans = append(out.Spans[:0:0], screen.Spans...)
	offsets := make([]screenOffset, 0, len(out.Spans)*2)
	for i, span := range out.Spans {
		offsets = append(offsets, screenOffset{position: max(0, span.Start), index: i}, screenOffset{position: max(0, span.End), index: i, end: true})
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i].position < offsets[j].position })
	source, normalized, next := 0, 0, 0
	apply := func(position int) {
		for next < len(offsets) && offsets[next].position <= position {
			offset := offsets[next]
			if offset.end {
				out.Spans[offset.index].End = normalized
			} else {
				out.Spans[offset.index].Start = normalized
			}
			next++
		}
	}
	previous := rune(0)
	for _, r := range text {
		apply(source)
		source++
		if r != '\n' || previous != '\r' {
			normalized++
		}
		previous = r
	}
	// Beyond-end offsets clamp to the final normalized position.
	apply(int(^uint(0) >> 1))
	return &out
}

// DisplayRow resolves a right-hand source coordinate to its paired display row.
func (d *Document) DisplayRow(index int) int {
	d.prepare()
	if index >= 0 && index < len(d.rows) && d.rows[index].Kind == "insert" {
		if peer := d.rows[index].peer; peer >= 0 {
			return peer
		}
	}
	return index
}

// RowAtLine resolves original coordinates without sending an index of the whole document.
func (d *Document) RowAtLine(line int, side string) int {
	d.prepare()
	rows := d.afterRows
	if side == "before" {
		rows = d.beforeRows
	}
	if len(rows) == 0 || line > len(rows) {
		return max(0, len(d.rows)-1)
	}
	return rows[max(0, line-1)]
}
