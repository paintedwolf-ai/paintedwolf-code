package reproducedsource

import (
	"strings"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/jsonfence"
)

const minSubstantiveRunes = 4

// Candidate is a session-touched file slice paired with its display path.
type Candidate struct {
	DisplayPath string
	Content     string
}

// Result holds the reproduced-source observation facts. Zero values mean no match.
type Result struct {
	Lines     int
	FileLines int
	Path      string
	StartLine int
	EndLine   int
}

type normLine struct {
	lineNum     int
	text        string
	substantive bool
}

type parsedFence struct {
	lines          []normLine
	substantivePos map[string][]int
}

// ReportFenceSpan returns the span of a closed top-level trailing json report fence.
func ReportFenceSpan(doc citationmd.Document) (citationmd.Span, bool) {
	if len(doc.Zones) == 0 {
		return citationmd.Span{}, false
	}
	if doc.Body.Start < 0 || doc.Body.End > len(doc.Source) || doc.Body.Start > doc.Body.End {
		return citationmd.Span{}, false
	}
	last := doc.Zones[len(doc.Zones)-1]
	if last.Kind != citationmd.ZoneFence || !atLineStart(doc.Source, last.Span.Start) {
		return citationmd.Span{}, false
	}
	body := doc.Source[doc.Body.Start:doc.Body.End]
	_, block, closed := jsonfence.Trailing(body)
	if !closed || !strings.EqualFold(block.Language, "json") {
		return citationmd.Span{}, false
	}
	return last.Span, true
}

// HasMeasurableFence reports whether doc contains at least one non-excluded code
// block with a line of four or more non-space characters.
func HasMeasurableFence(doc citationmd.Document, excluded []citationmd.Span) bool {
	return len(extractFences(doc, excluded)) > 0
}

// Measure finds the longest consecutive run of normalized non-blank lines shared
// between any non-excluded code block in doc and any candidate file.
func Measure(doc citationmd.Document, excluded []citationmd.Span, files []Candidate) Result {
	if len(files) == 0 {
		return Result{}
	}
	fences := extractFences(doc, excluded)
	if len(fences) == 0 {
		return Result{}
	}

	type parsedCandidate struct {
		path  string
		lines []normLine
	}
	candidates := make([]parsedCandidate, len(files))
	for i, f := range files {
		candidates[i] = parsedCandidate{
			path:  f.DisplayPath,
			lines: normalizeFileLines(f.Content),
		}
	}

	type bestMatch struct {
		lines      int
		fileLines  int
		path       string
		startLine  int
		endLine    int
		fenceIdx   int
		candIdx    int
		candStart  int
		fenceStart int
	}
	var best bestMatch

	better := func(cand bestMatch) bool {
		if cand.lines != best.lines {
			return cand.lines > best.lines
		}
		if cand.fenceIdx != best.fenceIdx {
			return cand.fenceIdx < best.fenceIdx
		}
		if cand.candIdx != best.candIdx {
			return cand.candIdx < best.candIdx
		}
		if cand.candStart != best.candStart {
			return cand.candStart < best.candStart
		}
		return cand.fenceStart < best.fenceStart
	}

	for fenceIdx, fence := range fences {
		for candIdx, cand := range candidates {
			if len(cand.lines) == 0 || min(len(fence.lines), len(cand.lines)) < best.lines {
				continue
			}
			for cj, cl := range cand.lines {
				if !cl.substantive {
					continue
				}
				fPositions, ok := fence.substantivePos[cl.text]
				if !ok {
					continue
				}
				for _, fj := range fPositions {
					fStart, cStart := fj, cj
					alreadyCovered := false
					for fStart > 0 && cStart > 0 && fence.lines[fStart-1].text == cand.lines[cStart-1].text {
						if fence.lines[fStart-1].substantive {
							// An earlier substantive line in the same run already anchored it.
							alreadyCovered = true
							break
						}
						fStart--
						cStart--
					}
					if alreadyCovered {
						continue
					}
					fEnd, cEnd := fj+1, cj+1
					for fEnd < len(fence.lines) && cEnd < len(cand.lines) && fence.lines[fEnd].text == cand.lines[cEnd].text {
						fEnd++
						cEnd++
					}
					runLen := cEnd - cStart
					trial := bestMatch{
						lines:      runLen,
						fileLines:  len(cand.lines),
						path:       cand.path,
						startLine:  cand.lines[cStart].lineNum,
						endLine:    cand.lines[cEnd-1].lineNum,
						fenceIdx:   fenceIdx,
						candIdx:    candIdx,
						candStart:  cStart,
						fenceStart: fStart,
					}
					if better(trial) {
						best = trial
					}
				}
			}
		}
	}

	if best.lines == 0 {
		return Result{}
	}
	return Result{
		Lines:     best.lines,
		FileLines: best.fileLines,
		Path:      best.path,
		StartLine: best.startLine,
		EndLine:   best.endLine,
	}
}

func extractFences(doc citationmd.Document, excluded []citationmd.Span) []parsedFence {
	if len(doc.Zones) == 0 {
		return nil
	}
	allExcluded := excluded
	if reportSpan, ok := ReportFenceSpan(doc); ok {
		allExcluded = append(append([]citationmd.Span(nil), excluded...), reportSpan)
	}
	var out []parsedFence
	for _, z := range doc.Zones {
		if z.Kind != citationmd.ZoneFence && z.Kind != citationmd.ZoneIndented {
			continue
		}
		if z.Span.Start < 0 || z.Span.End > len(doc.Source) || z.Span.Start >= z.Span.End {
			continue
		}
		if spanOverlaps(z.Span, allExcluded) {
			continue
		}
		raw := doc.Source[z.Span.Start:z.Span.End]
		var interior []string
		if z.Kind == citationmd.ZoneFence {
			lang, bodyLines := splitFencedZone(raw)
			if isExcludedFenceLang(lang) {
				continue
			}
			interior = bodyLines
		} else {
			interior = splitIndentedZone(raw)
		}
		lines, pos := normalizeFenceLines(interior)
		if len(pos) == 0 {
			continue
		}
		out = append(out, parsedFence{lines: lines, substantivePos: pos})
	}
	return out
}

func spanOverlaps(target citationmd.Span, spans []citationmd.Span) bool {
	for _, s := range spans {
		if s.Start < target.End && target.Start < s.End {
			return true
		}
	}
	return false
}

func isExcludedFenceLang(lang string) bool {
	return strings.EqualFold(lang, "diff") || strings.EqualFold(lang, "patch")
}
