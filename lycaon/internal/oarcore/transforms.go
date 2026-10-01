package oarcore

// Accumulated transforms use original-content coordinates.

import (
	"math"
	"sort"
	"strings"
)

// RedactionPlaceholder is the omitted redaction value ([OAR-OPS-13]).
const RedactionPlaceholder = "[REDACTED]"

// Span is a half-open range of Unicode code point offsets ([OAR-OPS-19]).
type Span struct {
	Start int
	End   int
}

// readSpans validates half-open transform spans ([OAR-OPS-19]).
func readSpans(value any, length int) ([]Span, error) {
	var members []any
	switch t := value.(type) {
	case nil:
		return nil, nil
	case []map[string]any:
		members = make([]any, len(t))
		for i, m := range t {
			members[i] = m
		}
	case []any:
		members = t
	default:
		return nil, raise("[OAR-FACT-26] transform target must be list<map>")
	}
	out := make([]Span, 0, len(members))
	for _, member := range members {
		m, ok := asObject(member)
		if !ok {
			return nil, raise("a transform span is not an object")
		}
		start, okStart := asNonNegInt(m["start"])
		end, okEnd := asNonNegInt(m["end"])
		// Span bounds are non-negative and ordered ([OAR-OPS-19]).
		if !okStart {
			return nil, raise("a transform span carries no non-negative integer \"start\"")
		}
		if !okEnd {
			return nil, raise("a transform span carries no non-negative integer \"end\"")
		}
		if start > end {
			return nil, raise("a transform span has a start beyond its end")
		}
		// [OAR-OPS-19] A span whose range falls outside the content is ignored.
		if end > length {
			continue
		}
		out = append(out, Span{Start: start, End: end})
	}
	return out, nil
}

func asNonNegInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		if n < 0 {
			return 0, false
		}
		return n, true
	case int64:
		if n < 0 {
			return 0, false
		}
		return int(n), true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n != math.Trunc(n) || n >= float64(math.MaxInt) {
			return 0, false
		}
		return int(n), true
	default:
		return 0, false
	}
}

// mergeSpans merges overlaps without merging adjacent spans ([OAR-OPS-20]).
func mergeSpans(spans []Span) []Span {
	var points, ranges []Span
	for _, span := range spans {
		if span.Start == span.End {
			points = append(points, span)
		} else {
			ranges = append(ranges, span)
		}
	}
	sort.SliceStable(ranges, func(i, j int) bool {
		if ranges[i].Start != ranges[j].Start {
			return ranges[i].Start < ranges[j].Start
		}
		return ranges[i].End < ranges[j].End
	})
	var merged []Span
	for _, span := range ranges {
		if len(merged) > 0 && span.Start < merged[len(merged)-1].End {
			if span.End > merged[len(merged)-1].End {
				merged[len(merged)-1].End = span.End
			}
		} else {
			merged = append(merged, span)
		}
	}
	merged = append(merged, points...)
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Start != merged[j].Start {
			return merged[i].Start < merged[j].Start
		}
		return merged[i].End < merged[j].End
	})
	return merged
}

// piece retains original coordinates through rewrites.
type piece struct {
	start  int
	end    int
	text   *string // nil means "take from original[start:end]"
	insert bool    // [OAR-OPS-21] true for text an annotate inserted
}

func pieceText(p piece, original []rune) string {
	if p.text != nil {
		return *p.text
	}
	return string(original[p.start:p.end])
}

// splitAt introduces a piece boundary at an original offset, when one does not
// exist.
func splitAt(pieces *[]piece, at int) {
	ps := *pieces
	for i := range ps {
		p := ps[i]
		if p.text == nil && p.start < at && at < p.end {
			//nolint:gocritic // The pieces slice header is replaced in place.
			*pieces = append(ps[:i], append([]piece{
				{start: p.start, end: at, text: nil},
				{start: at, end: p.end, text: nil},
			}, ps[i+1:]...)...)
			return
		}
	}
}

func rewrite(pieces *[]piece, span Span, replacement func(string) string, original []rune) {
	if span.Start == span.End {
		insertAt(pieces, span.Start, replacement(""))
		return
	}
	splitAt(pieces, span.Start)
	splitAt(pieces, span.End)
	var current strings.Builder
	out := make([]piece, 0, len(*pieces)+1)
	placed := false
	replacementIndex := 0
	for _, p := range *pieces {
		overlap := p.start < span.End && span.Start < p.end
		if p.insert {
			overlap = span.Start <= p.start && p.start < span.End
		}
		if overlap {
			if !placed {
				replacementIndex = len(out)
				out = append(out, piece{})
				placed = true
			}
			current.WriteString(pieceText(p, original))
		} else {
			out = append(out, p)
		}
	}
	text := replacement(current.String())
	if placed {
		out[replacementIndex] = piece{start: span.Start, end: span.End, text: &text}
	}
	*pieces = out
}

func replacementFor(spec *TransformSpec) func(string) string {
	// Redact uses the standard placeholder by default ([OAR-OPS-21]).
	if spec.Action == "redact" {
		replacement := RedactionPlaceholder
		if spec.HasReplacement {
			replacement = spec.Replacement
		}
		return func(string) string { return replacement }
	}
	replacement := spec.Replacement
	return func(string) string { return replacement }
}

// insertAt inserts after an original-content offset ([OAR-OPS-21]).
func insertAt(pieces *[]piece, at int, text string) {
	splitAt(pieces, at)
	ps := *pieces
	index := len(ps)
	for i, p := range ps {
		if p.start >= at && (!p.insert || p.start != at) {
			index = i
			break
		}
	}
	t := text
	//nolint:gocritic // The pieces slice header is replaced in place.
	*pieces = append(ps[:index], append([]piece{{start: at, end: at, text: &t, insert: true}}, ps[index:]...)...)
}

// SkippedTransform is a span ApplyTransforms left alone because an earlier
// transform already rewrote it.
type SkippedTransform struct {
	Rule   string `json:"rule"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Reason string `json:"reason"`
}

// ApplyTransforms applies occurrence-ordered transforms ([OAR-OPS-16]).
func ApplyTransforms(content string, transforms []*TransformSpec, targetFacts []any, ruleIDs []string) (string, []SkippedTransform) {
	skipped := make([]SkippedTransform, 0)
	original := []rune(content)
	pieces := []piece{{start: 0, end: len(original), text: nil}}
	// Offsets retain original-content coordinates ([OAR-OPS-16]).
	var rewritten []Span

	for index, spec := range transforms {
		// Targets resolve to content or reported spans ([OAR-OPS-14]).
		var spans []Span
		if spec.Target == "content" {
			spans = []Span{{Start: 0, End: len(original)}}
		} else {
			var value any
			if index < len(targetFacts) {
				value = targetFacts[index]
			}
			raw, err := readSpans(value, len(original))
			if err != nil {
				// Spans were validated before the decision resolved.
				raw = nil
			}
			spans = mergeSpans(raw)
		}
		annotating := spec.Action == "annotate"
		make := replacementFor(spec)
		var applied []Span
		// Spans apply from highest start offset ([OAR-OPS-20]).
		ordered := append([]Span(nil), spans...)
		for i := 0; i < len(ordered); i++ {
			for j := i + 1; j < len(ordered); j++ {
				if ordered[j].Start > ordered[i].Start {
					ordered[i], ordered[j] = ordered[j], ordered[i]
				}
			}
		}
		for _, span := range ordered {
			// Rewritten spans are skipped; annotations remain zero-width.
			if !annotating && span.Start < span.End {
				skip := false
				for _, r := range rewritten {
					if r.Start < r.End && span.Start < r.End && r.Start < span.End {
						skip = true
						break
					}
				}
				if skip {
					rule := ""
					if index < len(ruleIDs) {
						rule = ruleIDs[index]
					}
					skipped = append(skipped, SkippedTransform{Rule: rule, Start: span.Start, End: span.End, Reason: "overlap"})
					continue
				}
			}
			if annotating {
				insertAt(&pieces, span.End, spec.Replacement)
			} else {
				rewrite(&pieces, span, make, original)
			}
			applied = append(applied, span)
		}
		// Annotations record zero-width rewrites ([OAR-OPS-21]).
		if annotating {
			for _, span := range applied {
				rewritten = append(rewritten, Span{Start: span.End, End: span.End})
			}
		} else {
			rewritten = append(rewritten, applied...)
		}
	}

	var out string
	for _, p := range pieces {
		out += pieceText(p, original)
	}
	return out, skipped
}

// ReportTransform projects a TransformSpec into the shape fixtures assert
// ([OAR-CONF-21]): action and target always, replacement only when present.
func ReportTransform(spec *TransformSpec) map[string]any {
	out := map[string]any{
		"action": spec.Action,
		"target": spec.Target,
	}
	if spec.HasReplacement {
		out["replacement"] = spec.Replacement
	}
	return out
}
