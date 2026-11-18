// Package logoutline classifies log record streams and parses individual lines
// into typed Records. Each format registers one RecordParser and one classifier
// matcher; BuildDigest folds parsed records into a Digest.
package logoutline

type lineMatcher func(line []byte) bool

type classifierEntry struct {
	format      LogFormat
	floor       float64
	specificity int
	match       lineMatcher
}

var (
	parsers     = map[LogFormat]RecordParser{}
	classifiers []classifierEntry
)

func RegisterParser(p RecordParser) {
	if p == nil {
		return
	}
	parsers[p.Format()] = p
}

// RegisterClassifierMatcher registers a head-sample line matcher for Classify.
func RegisterClassifierMatcher(format LogFormat, floor float64, specificity int, match lineMatcher) {
	if match == nil || format == "" {
		return
	}
	classifiers = append(classifiers, classifierEntry{
		format:      format,
		floor:       floor,
		specificity: specificity,
		match:       match,
	})
}

func ParserFor(format LogFormat) (RecordParser, bool) {
	p, ok := parsers[format]
	return p, ok
}

// Classify sniffs a head sample and returns the best-matching format with a
// 0..1 confidence. Below the dispatch threshold the caller falls through.
func Classify(sample []byte) (LogFormat, float64) {
	lines := headSampleLines(sample)
	if len(lines) == 0 {
		return FormatNone, 0
	}
	type scored struct {
		format LogFormat
		ratio  float64
		spec   int
	}
	var best *scored
	for _, c := range classifiers {
		ratio := lineMatchRatio(lines, c.match)
		if ratio < c.floor {
			continue
		}
		candidate := scored{format: c.format, ratio: ratio, spec: c.specificity}
		if best == nil ||
			candidate.ratio > best.ratio ||
			(candidate.ratio == best.ratio && candidate.spec > best.spec) {
			copy := candidate
			best = &copy
		}
	}
	if best == nil {
		return FormatNone, 0
	}
	return best.format, best.ratio
}
